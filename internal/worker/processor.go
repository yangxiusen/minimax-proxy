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
	ClaimNext(context.Context, string, ...int64) (domain.Task, error)
	SaveBaseline(context.Context, string, string, []string) error
	SaveSubmissionContext(context.Context, string, string, []string) error
	BindUpstreamJob(context.Context, string, string, string) error
	BeginRetry(context.Context, string, string, []string, []string) error
	FinishCancelled(context.Context, string, string) error
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

type ArgumentPreparer interface {
	PrepareArguments(context.Context, v2.ValidatedRequest, config.GenerationProfile) ([]any, error)
}

type JobsClient interface {
	ListJobs(context.Context) ([]gradio.Job, error)
	GetJob(context.Context, string) (gradio.Job, error)
	CancelJob(context.Context, string) (bool, error)
}

type Processor struct {
	Store            Store
	Client           GradioClient
	Upstream         config.UpstreamConfig
	Profiles         map[string]config.GenerationProfile
	Logger           *slog.Logger
	Cache            *monitor.Cache
	Gate             *sync.Mutex
	ExecutionTimeout time.Duration
	NodeVersion      int64
	Now              func() time.Time
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
	var task domain.Task
	if p.NodeVersion > 0 {
		task, err = p.Store.ClaimNext(ctx, p.Upstream.ID, p.NodeVersion)
	} else {
		task, err = p.Store.ClaimNext(ctx, p.Upstream.ID)
	}
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
	if jobs, ok := p.Client.(JobsClient); ok {
		before, listErr := jobs.ListJobs(ctx)
		if listErr != nil {
			p.markPollFailure()
			p.Logger.WarnContext(ctx, "读取私有任务基线失败，任务重新排队", "task_id", task.TaskID, "upstream_id", p.Upstream.ID, "stage", "jobs_baseline", "error_code", "upstream_unavailable")
			if err := p.Store.Requeue(ctx, task.TaskID, p.Upstream.ID); err != nil {
				return err
			}
			p.clearCurrent()
			return nil
		}
		if err := p.Store.SaveSubmissionContext(ctx, task.TaskID, p.Upstream.ID, jobIDs(before)); err != nil {
			return err
		}
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
	arguments, err := p.prepareArguments(ctx, validated)
	if err != nil {
		return p.fail(ctx, task, "upstream_media_upload_failed", "参考音频上传私有服务失败")
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
	} else if jobs, ok := p.Client.(JobsClient); ok {
		persisted, activeErr := p.Store.ActiveForUpstream(ctx, p.Upstream.ID)
		if activeErr != nil {
			return activeErr
		}
		before, parseErr := parseJobIDs(persisted.UpstreamJobsBeforeJSON)
		if parseErr != nil {
			return p.fail(ctx, task, "recovery_context_invalid", "私有任务基线无法解析")
		}
		after, listErr := jobs.ListJobs(ctx)
		if listErr != nil {
			if err := p.Store.MarkReconciling(ctx, task.TaskID, p.Upstream.ID); err != nil {
				return err
			}
		} else {
			created := newJobs(before, after)
			switch len(created) {
			case 0:
				if err := p.Store.MarkReconciling(ctx, task.TaskID, p.Upstream.ID); err != nil {
					return err
				}
			case 1:
				if err := p.Store.BindUpstreamJob(ctx, task.TaskID, p.Upstream.ID, created[0].ID); err != nil {
					return err
				}
			default:
				return p.fail(ctx, task, "upstream_job_ambiguous", "无法识别唯一私有任务")
			}
		}
	} else if err := p.Store.MarkRunning(ctx, task.TaskID, p.Upstream.ID); err != nil {
		return err
	}
	active, err = p.Store.ActiveForUpstream(ctx, p.Upstream.ID)
	if err != nil {
		return err
	}
	return p.poll(ctx, active, validated, baseline)
}

func (p *Processor) resume(ctx context.Context, task domain.Task) error {
	if task.Status == domain.StatusCancelling {
		return p.cancel(ctx, task)
	}
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
	completedWithoutResult := 0
	for {
		latest, latestErr := p.Store.ActiveForUpstream(ctx, p.Upstream.ID)
		if latestErr != nil {
			if errors.Is(latestErr, domain.ErrTaskNotFound) {
				p.clearCurrent()
				return nil
			}
			return latestErr
		}
		task = latest
		if task.Status == domain.StatusCancelling {
			return p.cancel(ctx, task)
		}
		result, err := p.Client.Call(ctx, p.Upstream.CheckAPIName, []any{})
		if err != nil {
			p.markPollFailure()
			if ctx.Err() != nil {
				_ = p.markReconciling(context.Background(), task)
				return ctx.Err()
			}
			if err := p.markReconciling(ctx, task); err != nil {
				return err
			}
			task.Status = domain.StatusReconciling
			p.Logger.WarnContext(ctx, "轮询私有服务失败", "task_id", task.TaskID, "upstream_id", p.Upstream.ID, "stage", "poll", "error_code", "upstream_poll_error")
			if p.timedOut(task) {
				return p.timeout(ctx, task, "upstream_unavailable_timeout", "私有服务持续不可用，任务已结束")
			}
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
		if jobs, ok := p.Client.(JobsClient); ok {
			if task.UpstreamJobID == "" {
				before, parseErr := parseJobIDs(task.UpstreamJobsBeforeJSON)
				if parseErr != nil {
					return p.fail(ctx, task, "recovery_context_invalid", "私有任务基线无法解析")
				}
				currentJobs, listErr := jobs.ListJobs(ctx)
				if listErr != nil {
					p.markPollFailure()
					if err := p.markReconciling(ctx, task); err != nil {
						return err
					}
					task.Status = domain.StatusReconciling
				} else {
					created := newJobs(before, currentJobs)
					if task.AttemptStartedAt.IsZero() {
						created = activeJobs(currentJobs)
					}
					switch len(created) {
					case 0:
						if observation.Status == gradio.ObservationIdle && queueEmpty(observation) {
							return p.retryOrFail(ctx, task, validated, current, "upstream_job_lost", "私有任务不存在且未发现生成结果")
						}
					case 1:
						if err := p.Store.BindUpstreamJob(ctx, task.TaskID, p.Upstream.ID, created[0].ID); err != nil {
							return err
						}
						task.UpstreamJobID = created[0].ID
					default:
						return p.fail(ctx, task, "upstream_job_ambiguous", "检测到多个新增私有任务")
					}
				}
			} else {
				job, jobErr := jobs.GetJob(ctx, task.UpstreamJobID)
				switch {
				case errors.Is(jobErr, gradio.ErrJobNotFound):
					return p.retryOrFail(ctx, task, validated, current, "upstream_job_lost", "私有任务不存在且未发现生成结果")
				case jobErr != nil:
					p.markPollFailure()
					if err := p.markReconciling(ctx, task); err != nil {
						return err
					}
					task.Status = domain.StatusReconciling
					if p.timedOut(task) {
						return p.timeout(ctx, task, "upstream_unavailable_timeout", "私有服务持续不可用，任务已结束")
					}
				case job.Status == gradio.JobFailed:
					return p.fail(ctx, task, "upstream_failed", "私有服务报告生成失败")
				case job.Status == gradio.JobCancelled:
					return p.fail(ctx, task, "upstream_cancelled", "私有任务已被取消")
				case job.Status == gradio.JobCompleted:
					completedWithoutResult++
					if completedWithoutResult >= 3 {
						return p.fail(ctx, task, "upstream_result_missing", "私有任务已完成但未发现生成结果")
					}
				default:
					completedWithoutResult = 0
				}
			}
		}
		if p.timedOut(task) {
			return p.timeout(ctx, task, "execution_timeout", "任务执行超过配置的最长时间")
		}
		if err := wait(ctx, pollInterval); err != nil {
			return err
		}
	}
}

func (p *Processor) timeout(ctx context.Context, task domain.Task, code, message string) error {
	if task.UpstreamJobID == "" {
		p.blockScheduling()
	} else if jobs, ok := p.Client.(JobsClient); ok {
		_, err := jobs.CancelJob(ctx, task.UpstreamJobID)
		if err != nil && !errors.Is(err, gradio.ErrJobNotFound) {
			p.blockScheduling()
			p.Logger.WarnContext(ctx, "任务超时后中止私有任务失败，继续关闭本地任务", "task_id", task.TaskID, "upstream_id", p.Upstream.ID, "stage", "timeout_cancel", "error_code", "upstream_cancel_error")
		}
	}
	return p.fail(ctx, task, code, message)
}

func (p *Processor) retryOrFail(ctx context.Context, task domain.Task, validated v2.ValidatedRequest, galleryBefore []string, code, message string) error {
	if task.RetryCount >= 1 {
		return p.fail(ctx, task, code, message)
	}
	jobs, ok := p.Client.(JobsClient)
	if !ok {
		return p.fail(ctx, task, code, message)
	}
	currentJobs, err := jobs.ListJobs(ctx)
	if err != nil {
		return err
	}
	if task.UpstreamJobID != "" {
		_, cancelErr := jobs.CancelJob(ctx, task.UpstreamJobID)
		if cancelErr != nil && !errors.Is(cancelErr, gradio.ErrJobNotFound) {
			return cancelErr
		}
	}
	if err := p.Store.BeginRetry(ctx, task.TaskID, p.Upstream.ID, jobIDs(currentJobs), galleryBefore); err != nil {
		return err
	}
	arguments, err := p.prepareArguments(ctx, validated)
	if err != nil {
		return p.fail(ctx, task, "upstream_media_upload_failed", "参考音频上传私有服务失败")
	}
	p.Logger.WarnContext(ctx, "私有任务丢失，自动重试一次", "task_id", task.TaskID, "api_key_id", task.APIKeyID, "upstream_id", p.Upstream.ID, "stage", "retry", "error_code", code, "retry_count", 1)
	if _, err := p.Client.Call(ctx, p.Upstream.SubmitAPIName, arguments); err != nil {
		if errors.Is(err, gradio.ErrRequestRejected) {
			return p.fail(ctx, task, "upstream_rejected", "私有服务拒绝重试任务参数")
		}
		if markErr := p.Store.MarkReconciling(ctx, task.TaskID, p.Upstream.ID); markErr != nil {
			return markErr
		}
	} else {
		after, listErr := jobs.ListJobs(ctx)
		if listErr != nil {
			if markErr := p.Store.MarkReconciling(ctx, task.TaskID, p.Upstream.ID); markErr != nil {
				return markErr
			}
		} else {
			created := newJobs(jobIDs(currentJobs), after)
			switch len(created) {
			case 0:
				if err := p.Store.MarkReconciling(ctx, task.TaskID, p.Upstream.ID); err != nil {
					return err
				}
			case 1:
				if err := p.Store.BindUpstreamJob(ctx, task.TaskID, p.Upstream.ID, created[0].ID); err != nil {
					return err
				}
			default:
				return p.fail(ctx, task, "upstream_job_ambiguous", "重试后检测到多个新增私有任务")
			}
		}
	}
	active, err := p.Store.ActiveForUpstream(ctx, p.Upstream.ID)
	if err != nil {
		return err
	}
	return p.poll(ctx, active, validated, galleryBefore)
}

func (p *Processor) cancel(ctx context.Context, task domain.Task) error {
	jobs, ok := p.Client.(JobsClient)
	jobID := task.UpstreamJobID
	cancelUnconfirmed := false
	if ok && jobID == "" {
		for {
			current, err := jobs.ListJobs(ctx)
			if err != nil {
				p.markPollFailure()
				if p.timedOut(task) {
					cancelUnconfirmed = true
					break
				}
				if err := wait(ctx, p.pollInterval()); err != nil {
					return err
				}
				continue
			}
			before, err := parseJobIDs(task.UpstreamJobsBeforeJSON)
			if err != nil {
				return err
			}
			created := newJobs(before, current)
			if task.AttemptStartedAt.IsZero() {
				created = activeJobs(current)
			}
			if len(created) == 1 {
				jobID = created[0].ID
			}
			if len(created) == 0 {
				cancelUnconfirmed = true
				break
			}
			if len(created) == 1 {
				break
			}
			if p.timedOut(task) {
				cancelUnconfirmed = true
				break
			}
			p.Logger.WarnContext(ctx, "中止任务时检测到多个候选私有任务", "task_id", task.TaskID, "upstream_id", p.Upstream.ID, "stage", "cancel", "error_code", "upstream_job_ambiguous")
			if err := wait(ctx, p.pollInterval()); err != nil {
				return err
			}
		}
	}
	if ok && jobID != "" {
		for {
			_, err := jobs.CancelJob(ctx, jobID)
			if err == nil || errors.Is(err, gradio.ErrJobNotFound) {
				break
			}
			p.markPollFailure()
			if p.timedOut(task) {
				cancelUnconfirmed = true
				break
			}
			if err := wait(ctx, p.pollInterval()); err != nil {
				return err
			}
		}
	}
	if cancelUnconfirmed {
		p.blockScheduling()
	}
	if err := p.Store.FinishCancelled(ctx, task.TaskID, p.Upstream.ID); err != nil {
		return err
	}
	p.Logger.InfoContext(ctx, "视频生成任务已中止", "task_id", task.TaskID, "api_key_id", task.APIKeyID, "upstream_id", p.Upstream.ID, "stage", "cancel")
	return p.markFinished(ctx)
}

func (p *Processor) timedOut(task domain.Task) bool {
	timeout := p.ExecutionTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	started := task.AttemptStartedAt
	if started.IsZero() {
		started = task.StartedAt
	}
	if started.IsZero() {
		return false
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	return !now.Before(started.Add(timeout))
}

func (p *Processor) pollInterval() time.Duration {
	if p.Upstream.PollInterval > 0 {
		return p.Upstream.PollInterval
	}
	return 3 * time.Second
}

func (p *Processor) prepareArguments(ctx context.Context, request v2.ValidatedRequest) ([]any, error) {
	profile := p.Profiles[request.Resolution]
	if preparer, ok := p.Client.(ArgumentPreparer); ok {
		return preparer.PrepareArguments(ctx, request, profile)
	}
	return gradio.BuildArguments(request, profile)
}

func jobIDs(jobs []gradio.Job) []string {
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.ID)
	}
	return ids
}

func parseJobIDs(data string) ([]string, error) {
	if data == "" {
		return []string{}, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(data), &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func newJobs(before []string, current []gradio.Job) []gradio.Job {
	known := make(map[string]struct{}, len(before))
	for _, id := range before {
		known[id] = struct{}{}
	}
	created := make([]gradio.Job, 0, 1)
	for _, job := range current {
		if _, ok := known[job.ID]; !ok {
			created = append(created, job)
		}
	}
	return created
}

func activeJobs(jobs []gradio.Job) []gradio.Job {
	active := make([]gradio.Job, 0, 1)
	for _, job := range jobs {
		if job.Status == gradio.JobPending || job.Status == gradio.JobInProgress {
			active = append(active, job)
		}
	}
	return active
}

func queueEmpty(observation gradio.Observation) bool {
	return observation.PrivateQueue != nil && *observation.PrivateQueue == 0
}

func (p *Processor) markReconciling(ctx context.Context, task domain.Task) error {
	if task.Status == domain.StatusReconciling {
		return nil
	}
	err := p.Store.MarkReconciling(ctx, task.TaskID, p.Upstream.ID)
	if errors.Is(err, domain.ErrStateConflict) {
		return nil
	}
	return err
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

func (p *Processor) blockScheduling() {
	if p.Cache == nil {
		return
	}
	p.Cache.Update(p.Upstream.ID, func(node *monitor.NodeSnapshot) {
		node.SchedulingBlocked = true
		node.Health = monitor.HealthUnhealthy
		node.Runtime = monitor.RuntimeUnknown
		node.UpdatedAt = time.Now().UTC()
		node.LastError = &monitor.ErrorSnapshot{Code: "upstream_cancel_unconfirmed"}
	})
}

func (p *Processor) markFinished(ctx context.Context) error {
	if p.Cache == nil {
		return nil
	}
	p.Cache.Update(p.Upstream.ID, func(node *monitor.NodeSnapshot) {
		node.CurrentTask = nil
		if node.SchedulingBlocked {
			node.Health = monitor.HealthUnhealthy
			node.Runtime = monitor.RuntimeUnknown
			node.LastError = &monitor.ErrorSnapshot{Code: "upstream_cancel_unconfirmed"}
		} else {
			node.Runtime = monitor.RuntimeIdle
		}
		node.UpdatedAt = time.Now().UTC()
	})
	finished, err := p.Store.LatestFinishedForUpstream(ctx, p.Upstream.ID)
	if err != nil {
		p.Logger.WarnContext(ctx, "读取最近完成任务失败，运行缓存已清理", "upstream_id", p.Upstream.ID, "stage", "cache_finish", "error_code", "latest_finished_unavailable")
		return nil
	}
	duration := int64(0)
	if !finished.StartedAt.IsZero() && !finished.FinishedAt.Before(finished.StartedAt) {
		duration = int64(finished.FinishedAt.Sub(finished.StartedAt) / time.Second)
	}
	p.Cache.Update(p.Upstream.ID, func(node *monitor.NodeSnapshot) {
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
