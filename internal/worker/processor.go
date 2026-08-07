package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/httpapi/v2"
	"minimax-h3-tc/internal/monitor"
	"minimax-h3-tc/internal/upstream/gradio"
)

type Store interface {
	ActiveForUpstream(context.Context, string) (domain.Task, error)
	ClaimNext(context.Context, string) (domain.Task, error)
	SaveBaseline(context.Context, string, string, []string) error
	MarkRunning(context.Context, string, string) error
	MarkReconciling(context.Context, string, string) error
	MarkSucceeded(context.Context, string, string, string, string, string) error
	MarkFailed(context.Context, string, string, string, string) error
	Requeue(context.Context, string, string) error
	LatestFinishedForUpstream(context.Context, string) (domain.AdminTaskSummary, error)
}

type GradioClient interface {
	Call(context.Context, string, []any) ([]any, error)
}

type Processor struct {
	Store    Store
	Client   GradioClient
	Upstream config.UpstreamConfig
	Profiles map[string]config.GenerationProfile
	Logger   *slog.Logger
	Cache    *monitor.Cache
	Gate     *sync.Mutex
}

func (p *Processor) ProcessOne(ctx context.Context) error {
	if p.Gate != nil {
		p.Gate.Lock()
		defer p.Gate.Unlock()
	}
	if p.Logger == nil {
		p.Logger = slog.Default()
	}
	active, err := p.Store.ActiveForUpstream(ctx, p.Upstream.ID)
	if err == nil {
		p.markCurrent(active)
		return p.resume(ctx, active)
	}
	if !errors.Is(err, domain.ErrTaskNotFound) {
		return err
	}
	task, err := p.Store.ClaimNext(ctx, p.Upstream.ID)
	if err != nil {
		return err
	}
	p.markCurrent(task)
	p.Logger.InfoContext(ctx, "上游任务已领取", "task_id", task.TaskID, "api_key_id", task.APIKeyID, "upstream_id", p.Upstream.ID, "stage", "claim")
	var request v2.CreateRequest
	if err := json.Unmarshal([]byte(task.RequestJSON), &request); err != nil {
		return p.fail(ctx, task, "invalid_persisted_request", "持久化请求无法解析")
	}
	validated, err := v2.ValidateCreate(request, p.Profiles)
	if err != nil {
		return p.fail(ctx, task, "invalid_persisted_request", "持久化请求不再符合配置")
	}
	arguments, err := gradio.BuildArguments(validated, p.Profiles[validated.Resolution])
	if err != nil {
		return p.fail(ctx, task, "upstream_mapping_error", "无法映射上游参数")
	}

	baselineResult, err := p.Client.Call(ctx, p.Upstream.CheckAPIName, []any{})
	if err != nil {
		p.markPollFailure()
		p.Logger.WarnContext(ctx, "读取 Gallery 基线失败，任务重新排队", "task_id", task.TaskID, "upstream_id", p.Upstream.ID, "stage", "baseline", "error_code", "upstream_unavailable")
		if err := p.Store.Requeue(ctx, task.TaskID, p.Upstream.ID); err != nil {
			return err
		}
		p.clearCurrent()
		return nil
	}
	p.mergeObservation(gradio.ParseObservation(baselineResult))
	if len(baselineResult) == 0 {
		return p.fail(ctx, task, "upstream_protocol_error", "上游未返回 Gallery")
	}
	baseline := gradio.GalleryURLs(baselineResult[0])
	if err := p.Store.SaveBaseline(ctx, task.TaskID, p.Upstream.ID, baseline); err != nil {
		return err
	}

	p.Logger.InfoContext(ctx, "开始向私有服务提交任务", "task_id", task.TaskID, "upstream_id", p.Upstream.ID, "stage", "submit")
	if _, err := p.Client.Call(ctx, p.Upstream.SubmitAPIName, arguments); err != nil {
		if errors.Is(err, gradio.ErrRequestRejected) {
			return p.fail(ctx, task, "upstream_rejected", "私有服务拒绝任务参数")
		}
		p.Logger.WarnContext(ctx, "提交结果未知，进入恢复轮询", "task_id", task.TaskID, "upstream_id", p.Upstream.ID, "stage", "submit", "error_code", "submit_unknown")
		if markErr := p.Store.MarkReconciling(ctx, task.TaskID, p.Upstream.ID); markErr != nil {
			return markErr
		}
	} else if err := p.Store.MarkRunning(ctx, task.TaskID, p.Upstream.ID); err != nil {
		return err
	}

	return p.poll(ctx, task, validated, baseline)
}

func (p *Processor) resume(ctx context.Context, task domain.Task) error {
	if task.GalleryBeforeJSON == "" {
		if task.Status == domain.StatusDispatching {
			if err := p.Store.Requeue(ctx, task.TaskID, p.Upstream.ID); err != nil {
				return err
			}
			p.clearCurrent()
			return nil
		}
		return p.fail(ctx, task, "recovery_context_missing", "恢复任务缺少 Gallery 基线")
	}
	var baseline []string
	if err := json.Unmarshal([]byte(task.GalleryBeforeJSON), &baseline); err != nil {
		return p.fail(ctx, task, "recovery_context_invalid", "Gallery 基线无法解析")
	}
	var request v2.CreateRequest
	if err := json.Unmarshal([]byte(task.RequestJSON), &request); err != nil {
		return p.fail(ctx, task, "invalid_persisted_request", "持久化请求无法解析")
	}
	validated, err := v2.ValidateCreate(request, p.Profiles)
	if err != nil {
		return p.fail(ctx, task, "invalid_persisted_request", "持久化请求不再符合配置")
	}
	if err := p.Store.MarkReconciling(ctx, task.TaskID, p.Upstream.ID); err != nil {
		return err
	}
	p.Logger.InfoContext(ctx, "恢复上次未完成任务", "task_id", task.TaskID, "upstream_id", p.Upstream.ID, "stage", "reconcile")
	return p.poll(ctx, task, validated, baseline)
}

func (p *Processor) poll(ctx context.Context, task domain.Task, validated v2.ValidatedRequest, baseline []string) error {
	pollInterval := p.Upstream.PollInterval
	if pollInterval <= 0 {
		pollInterval = 3 * time.Second
	}
	for {
		result, err := p.Client.Call(ctx, p.Upstream.CheckAPIName, []any{})
		if err != nil {
			p.markPollFailure()
			if ctx.Err() != nil {
				_ = p.Store.MarkReconciling(context.Background(), task.TaskID, p.Upstream.ID)
				return ctx.Err()
			}
			p.Logger.WarnContext(ctx, "轮询私有服务失败", "task_id", task.TaskID, "upstream_id", p.Upstream.ID, "stage", "poll", "error_code", "upstream_poll_error")
			if err := wait(ctx, pollInterval); err != nil {
				return err
			}
			continue
		}
		observation := gradio.ParseObservation(result)
		p.mergeObservation(observation)
		if len(result) == 0 {
			return p.fail(ctx, task, "upstream_protocol_error", "上游未返回 Gallery")
		}
		current := gradio.GalleryURLs(result[0])
		newCount := countNew(baseline, current)
		if newCount == 1 {
			internalURL, err := gradio.UniqueNewVideo(baseline, result[0])
			if err != nil {
				return p.fail(ctx, task, "result_ambiguous", "无法识别唯一生成视频")
			}
			publicURL, err := gradio.RewritePublicURL(internalURL, p.Upstream.BaseURL, p.Upstream.PublicBaseURL)
			if err != nil {
				return p.fail(ctx, task, "result_url_invalid", "生成视频地址无法公开映射")
			}
			if err := p.Store.MarkSucceeded(ctx, task.TaskID, p.Upstream.ID, internalURL, publicURL, validated.Ratio); err != nil {
				return err
			}
			if err := p.markFinished(ctx); err != nil {
				return err
			}
			p.Logger.InfoContext(ctx, "视频生成任务完成", "task_id", task.TaskID, "api_key_id", task.APIKeyID, "upstream_id", p.Upstream.ID, "stage", "complete")
			return nil
		}
		if newCount > 1 {
			return p.fail(ctx, task, "result_ambiguous", "检测到多个新增视频")
		}
		if observation.Status == gradio.ObservationFailed {
			return p.fail(ctx, task, "upstream_failed", "私有服务报告生成失败")
		}
		if err := wait(ctx, pollInterval); err != nil {
			return err
		}
	}
}

func (p *Processor) fail(ctx context.Context, task domain.Task, code, message string) error {
	p.Logger.ErrorContext(ctx, "视频生成任务失败", "task_id", task.TaskID, "api_key_id", task.APIKeyID, "upstream_id", p.Upstream.ID, "stage", "failed", "error_code", code)
	if err := p.Store.MarkFailed(ctx, task.TaskID, p.Upstream.ID, code, message); err != nil {
		return err
	}
	return p.markFinished(ctx)
}

func countNew(before, current []string) int {
	old := make(map[string]struct{}, len(before))
	for _, value := range before {
		old[value] = struct{}{}
	}
	count := 0
	for _, value := range current {
		if _, ok := old[value]; !ok {
			count++
		}
	}
	return count
}

func (p *Processor) markCurrent(task domain.Task) {
	if p.Cache == nil {
		return
	}
	p.Cache.Update(p.Upstream.ID, func(node *monitor.NodeSnapshot) {
		node.Runtime = monitor.RuntimeRunning
		node.UpdatedAt = time.Now().UTC()
		node.CurrentTask = &monitor.CurrentTaskSnapshot{ID: task.TaskID, Status: string(domain.V2Running), StartedAt: task.StartedAt}
	})
}

func (p *Processor) mergeObservation(observation gradio.Observation) {
	if p.Cache == nil {
		return
	}
	now := time.Now().UTC()
	p.Cache.Update(p.Upstream.ID, func(node *monitor.NodeSnapshot) {
		node.Health = monitor.HealthHealthy
		node.CheckedAt = now
		node.LastHealthyAt = now
		node.UpdatedAt = now
		node.PrivateQueue = observation.PrivateQueue
		node.CPUPercent = observation.CPUPercent
		node.MemoryPercent = observation.MemoryPercent
		node.GPUPercent = observation.GPUPercent
		node.VRAMPercent = observation.VRAMPercent
		node.LastError = nil
		if node.CurrentTask != nil {
			node.Runtime = monitor.RuntimeRunning
			return
		}
		switch observation.Status {
		case gradio.ObservationIdle:
			node.Runtime = monitor.RuntimeIdle
		case gradio.ObservationRunning:
			node.Runtime = monitor.RuntimeRunning
		default:
			node.Runtime = monitor.RuntimeUnknown
		}
	})
}

func (p *Processor) markPollFailure() {
	if p.Cache == nil {
		return
	}
	now := time.Now().UTC()
	p.Cache.Update(p.Upstream.ID, func(node *monitor.NodeSnapshot) {
		node.Health = monitor.HealthUnhealthy
		node.CheckedAt = now
		node.UpdatedAt = now
		node.LastError = &monitor.ErrorSnapshot{Code: "upstream_poll_error"}
	})
}

func (p *Processor) clearCurrent() {
	if p.Cache == nil {
		return
	}
	p.Cache.Update(p.Upstream.ID, func(node *monitor.NodeSnapshot) {
		node.CurrentTask = nil
		node.Runtime = monitor.RuntimeUnknown
		node.UpdatedAt = time.Now().UTC()
	})
}

func (p *Processor) markFinished(ctx context.Context) error {
	if p.Cache == nil {
		return nil
	}
	finished, err := p.Store.LatestFinishedForUpstream(ctx, p.Upstream.ID)
	if err != nil {
		return err
	}
	duration := int64(0)
	if !finished.StartedAt.IsZero() && !finished.FinishedAt.Before(finished.StartedAt) {
		duration = int64(finished.FinishedAt.Sub(finished.StartedAt) / time.Second)
	}
	p.Cache.Update(p.Upstream.ID, func(node *monitor.NodeSnapshot) {
		node.CurrentTask = nil
		node.Runtime = monitor.RuntimeIdle
		node.UpdatedAt = time.Now().UTC()
		node.LatestFinishedTask = &monitor.FinishedTaskSnapshot{
			ID: finished.TaskID, APIKeyID: finished.APIKeyID, Status: string(finished.Status),
			DurationSeconds: duration, FinishedAt: finished.FinishedAt,
		}
	})
	return nil
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
