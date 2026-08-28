package official

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/upstream/minimaxv2"
)

type Store interface {
	ClaimNextOfficial(context.Context, string, int64, int) (domain.Task, error)
	BindOfficialTask(context.Context, string, string, string) error
	MarkOfficialGenerated(context.Context, string, string, string, string, *domain.ResultUploadJob) error
	MarkOfficialFailed(context.Context, string, string, string, string, *domain.UpstreamFeedback) error
	SaveOfficialSubmissionBaseline(context.Context, string, string, []string) error
}

type Client interface {
	Submit(context.Context, []byte) (string, error)
	Query(context.Context, string) (minimaxv2.Task, error)
	List(context.Context) ([]minimaxv2.Task, error)
}

type RequestRestorer interface {
	Restore(context.Context, string, []byte) ([]byte, error)
}

type Processor struct {
	Store        Store
	Client       Client
	Inputs       RequestRestorer
	NodeID       string
	NodeVersion  int64
	Capacity     int
	PollInterval time.Duration
	Now          func() time.Time
}

func (p *Processor) ProcessOne(ctx context.Context) error {
	task, err := p.Store.ClaimNextOfficial(ctx, p.NodeID, p.NodeVersion, p.Capacity)
	if err != nil {
		return err
	}
	return p.ProcessTask(ctx, task)
}

func (p *Processor) ProcessTask(ctx context.Context, task domain.Task) error {
	upstreamTaskID := task.UpstreamJobID
	if upstreamTaskID == "" {
		baseline := make(map[string]struct{})
		if task.OfficialSubmissionBaselineSaved {
			var ids []string
			if err := json.Unmarshal([]byte(task.UpstreamJobsBeforeJSON), &ids); err != nil {
				return p.Store.MarkOfficialFailed(ctx, task.TaskID, p.NodeID, "official_reconcile_invalid", "官方任务对账快照无效", nil)
			}
			for _, id := range ids {
				baseline[id] = struct{}{}
			}
			if reconciled, ok, err := p.reconcileSubmissionWithRetry(ctx, task, baseline); err != nil {
				return p.failUnlessStopping(ctx, task, "official_reconcile_failed", err)
			} else if ok {
				upstreamTaskID = reconciled
				if err := p.Store.BindOfficialTask(ctx, task.TaskID, p.NodeID, reconciled); err != nil {
					return err
				}
			} else {
				return p.Store.MarkOfficialFailed(ctx, task.TaskID, p.NodeID, "official_submit_uncertain", "官方任务提交结果无法确认，已停止自动重提", nil)
			}
		}
		if upstreamTaskID == "" {
			requestBody := []byte(task.RequestJSON)
			if p.Inputs != nil {
				restored, restoreErr := p.Inputs.Restore(ctx, task.TaskID, requestBody)
				if restoreErr != nil {
					if ctx.Err() != nil {
						return restoreErr
					}
					return p.Store.MarkOfficialFailed(ctx, task.TaskID, p.NodeID, "official_input_materialization_failed", "官方任务输入素材还原失败", nil)
				}
				requestBody = restored
			}
			before, err := p.Client.List(ctx)
			if err != nil {
				return p.failUnlessStopping(ctx, task, "official_baseline_failed", err)
			}
			ids := make([]string, 0, len(before))
			for _, item := range before {
				ids = append(ids, item.ID)
				baseline[item.ID] = struct{}{}
			}
			if err := p.Store.SaveOfficialSubmissionBaseline(ctx, task.TaskID, p.NodeID, ids); err != nil {
				return err
			}
			created, err := p.Client.Submit(minimaxv2.WithProxyTaskID(ctx, task.TaskID), requestBody)
			if err != nil {
				if reconciled, ok, reconcileErr := p.reconcileSubmissionWithRetry(ctx, task, baseline); reconcileErr == nil && ok {
					created = reconciled
				} else if reconcileErr != nil {
					return p.failUnlessStoppingWithFeedback(ctx, task, "official_reconcile_failed", reconcileErr, upstreamFeedback(err))
				} else {
					return p.failUnlessStopping(ctx, task, "official_submit_failed", err)
				}
			}
			if err := p.Store.BindOfficialTask(ctx, task.TaskID, p.NodeID, created); err != nil {
				return err
			}
			upstreamTaskID = created
		}
	}

	interval := p.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	for {
		result, err := p.Client.Query(ctx, upstreamTaskID)
		if err != nil {
			if retryableQueryError(err) {
				if waitErr := wait(ctx, interval); waitErr != nil {
					return waitErr
				}
				continue
			}
			return p.failUnlessStopping(ctx, task, "official_query_failed", err)
		}
		switch result.Status {
		case minimaxv2.StatusQueued, minimaxv2.StatusRunning:
			if err := wait(ctx, interval); err != nil {
				return err
			}
		case minimaxv2.StatusSucceeded:
			var job *domain.ResultUploadJob
			if task.DeliveryRequired {
				now := p.now()
				job = &domain.ResultUploadJob{
					ID: "result-upload-" + task.TaskID, TaskID: task.TaskID,
					ObjectKey: fmt.Sprintf("MiniMax-H3/%s/%s.mp4", now.Format("2006-01-02"), task.TaskID),
				}
			}
			return p.Store.MarkOfficialGenerated(ctx, task.TaskID, p.NodeID, result.Content.URL, result.Ratio, job)
		case minimaxv2.StatusFailed, minimaxv2.StatusCancelled:
			code, message := "official_generation_failed", "官方视频生成任务失败"
			var feedback *domain.UpstreamFeedback
			if result.Status == minimaxv2.StatusCancelled {
				code, message = "official_task_cancelled", "官方视频生成任务已取消"
			}
			if result.Error != nil {
				feedback = &domain.UpstreamFeedback{Code: result.Error.Code, Message: result.Error.Message}
			}
			code, message = domain.LocalizeOfficialError(code, message, feedback)
			return p.Store.MarkOfficialFailed(ctx, task.TaskID, p.NodeID, code, message, feedback)
		default:
			return p.Store.MarkOfficialFailed(ctx, task.TaskID, p.NodeID, "official_status_invalid", "官方任务返回未知状态", nil)
		}
	}
}

func (p *Processor) reconcileSubmission(ctx context.Context, task domain.Task, baseline map[string]struct{}) (string, bool, error) {
	items, err := p.Client.List(ctx)
	if err != nil {
		return "", false, err
	}
	candidates := make([]string, 0, 1)
	for _, item := range items {
		if _, existed := baseline[item.ID]; existed {
			continue
		}
		if item.Resolution != "" && item.Resolution != task.Resolution {
			continue
		}
		if item.Duration != 0 && item.Duration != task.Duration {
			continue
		}
		if item.Ratio != "" && task.RatioRequested != "" && item.Ratio != task.RatioRequested {
			continue
		}
		if item.CreatedAt > 0 && !task.AttemptStartedAt.IsZero() && item.CreatedAt < task.AttemptStartedAt.Add(-5*time.Second).Unix() {
			continue
		}
		candidates = append(candidates, item.ID)
	}
	return firstCandidate(candidates)
}

func (p *Processor) reconcileSubmissionWithRetry(ctx context.Context, task domain.Task, baseline map[string]struct{}) (string, bool, error) {
	for attempt := 0; attempt < 3; attempt++ {
		id, ok, err := p.reconcileSubmission(ctx, task, baseline)
		if err != nil || ok {
			return id, ok, err
		}
		if attempt < 2 {
			interval := p.PollInterval
			if interval <= 0 {
				interval = time.Second
			}
			if err := wait(ctx, interval); err != nil {
				return "", false, err
			}
		}
	}
	return "", false, nil
}

func firstCandidate(candidates []string) (string, bool, error) {
	if len(candidates) == 0 {
		return "", false, nil
	}
	if len(candidates) > 1 {
		return "", false, errors.New("官方任务对账存在多个候选")
	}
	return candidates[0], true, nil
}

func retryableQueryError(err error) bool {
	var httpError *minimaxv2.HTTPError
	if errors.As(err, &httpError) {
		return httpError.StatusCode == 429 || httpError.StatusCode >= 500
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func (p *Processor) failUnlessStopping(ctx context.Context, task domain.Task, code string, cause error) error {
	return p.failUnlessStoppingWithFeedback(ctx, task, code, cause, upstreamFeedback(cause))
}

func (p *Processor) failUnlessStoppingWithFeedback(ctx context.Context, task domain.Task, code string, cause error, feedback *domain.UpstreamFeedback) error {
	if ctx.Err() != nil {
		return cause
	}
	code, message := domain.LocalizeOfficialError(code, officialFailureMessage(code), feedback)
	if err := p.Store.MarkOfficialFailed(ctx, task.TaskID, p.NodeID, code, message, feedback); err != nil {
		return errors.Join(cause, err)
	}
	return nil
}

func upstreamFeedback(cause error) *domain.UpstreamFeedback {
	var httpError *minimaxv2.HTTPError
	if !errors.As(cause, &httpError) {
		return nil
	}
	return &domain.UpstreamFeedback{
		HTTPStatus: httpError.StatusCode, Code: httpError.Code, Type: httpError.Type,
		Message: httpError.Message, ResourceType: httpError.ResourceType, RequestID: httpError.RequestID,
	}
}

func officialFailureMessage(code string) string {
	switch code {
	case "official_baseline_failed":
		return "官方任务列表查询失败"
	case "official_reconcile_failed":
		return "官方任务提交结果对账失败"
	case "official_submit_failed":
		return "官方任务提交失败"
	case "official_query_failed":
		return "官方任务状态查询失败"
	default:
		return "官方任务处理失败"
	}
}

func (p *Processor) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
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
