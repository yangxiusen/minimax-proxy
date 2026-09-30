package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/google/uuid"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/upstream/tk2sd"
)

type Store interface {
	ClaimNextRemote(context.Context, string, int64, int) (domain.Task, error)
	ListActiveRemoteTasks(context.Context, string) ([]domain.Task, error)
	GetRemoteRun(context.Context, string) (domain.RemoteRun, error)
	AcquireRemoteLease(context.Context, string, string, string, time.Duration) (domain.RemoteRun, error)
	RenewRemoteLease(context.Context, domain.RemoteRun, time.Duration) error
	ReleaseRemoteLease(context.Context, domain.RemoteRun, int64, string, bool) error
	PrepareRemoteRequest(context.Context, domain.RemoteRun, string) error
	BeginRemoteSubmission(context.Context, domain.RemoteRun) error
	BindRemoteTask(context.Context, domain.RemoteRun, string) error
	ObserveRemoteTask(context.Context, domain.RemoteRun, string, string) error
	CompleteRemoteRun(context.Context, domain.RemoteRun, domain.RemoteCompletion) error
	RequestRemoteCancel(context.Context, string) error
}

type Client interface {
	AssetClient
	ResultClient
	Submit(context.Context, []byte, string) (string, error)
	Cancel(context.Context, string) error
}

type Processor struct {
	Store                       Store
	Client                      Client
	Inputs                      *InputMaterializer
	NodeID                      string
	NodeVersion                 int64
	NodeURL                     *url.URL
	Capacity                    int
	LeaseDuration, PollInterval time.Duration
	Now                         func() time.Time
	Logger                      *slog.Logger
}

func (p *Processor) ProcessOne(ctx context.Context) error {
	task, err := p.Store.ClaimNextRemote(ctx, p.NodeID, p.NodeVersion, p.Capacity)
	if err != nil {
		return err
	}
	p.log("领取远程任务", task.TaskID)
	return p.ProcessTask(ctx, task)
}

// 每次扫描只推进一步，避免单个长任务阻塞同节点的其他恢复任务。
func (p *Processor) Resume(ctx context.Context) error {
	tasks, err := p.Store.ListActiveRemoteTasks(ctx, p.NodeID)
	if err != nil {
		return err
	}
	var result error
	for _, task := range tasks {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		err := p.ProcessTask(ctx, task)
		if err != nil && !errors.Is(err, domain.ErrRemotePending) && !errors.Is(err, domain.ErrStateConflict) {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (p *Processor) ProcessTask(ctx context.Context, task domain.Task) (err error) {
	if p.Store == nil || p.Client == nil || task.TaskID == "" || task.UpstreamID != p.NodeID || task.ProtocolVersion != domain.ProtocolTK2SD {
		return domain.ErrStateConflict
	}
	ttl := p.LeaseDuration
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	run, err := p.Store.AcquireRemoteLease(ctx, task.TaskID, p.NodeID, uuid.NewString(), ttl)
	if err != nil {
		return err
	}
	opCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(max(ttl/4, 100*time.Millisecond))
		defer ticker.Stop()
		for {
			select {
			case <-opCtx.Done():
				return
			case <-ticker.C:
				if p.Store.RenewRemoteLease(opCtx, run, ttl) != nil {
					cancel()
					return
				}
			}
		}
	}()
	code, reconcile := "", false
	defer func() {
		cancel()
		<-done
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		interval := p.PollInterval
		if interval < time.Second {
			interval = time.Second
		}
		releaseErr := p.Store.ReleaseRemoteLease(cleanup, run, p.now().Add(interval).Unix(), code, reconcile)
		if releaseErr != nil && !errors.Is(releaseErr, domain.ErrStateConflict) {
			err = errors.Join(err, releaseErr)
		}
	}()
	code, reconcile, err = p.advance(opCtx, task, run)
	return err
}

func (p *Processor) advance(ctx context.Context, task domain.Task, run domain.RemoteRun) (string, bool, error) {
	if run.ReconciliationRequired {
		return "reconciliation_required", true, domain.ErrRemoteReconciliation
	}
	if run.CancelState == "requested" && (run.Phase == domain.RemotePreparing || run.Phase == domain.RemotePrepared) {
		return "", false, p.complete(ctx, run, domain.RemoteCompletion{Status: "cancelled"})
	}
	if run.Phase == domain.RemotePreparing {
		body, err := p.Inputs.Prepare(ctx, task, run, p.Client)
		if err != nil {
			if errors.Is(err, ErrInvalidInput) || rejected(err) {
				return "input_invalid", false, p.complete(ctx, run, domain.RemoteCompletion{Status: "failed", ErrorCode: "tk2sd_input_invalid", ErrorMessage: "远程任务素材校验失败"})
			}
			return "input_unavailable", false, domain.ErrRemotePending
		}
		if err = p.Store.PrepareRemoteRequest(ctx, run, string(body)); err != nil {
			return "prepare_conflict", false, err
		}
		run, err = p.reloadRun(ctx, run)
		if err != nil {
			return "store_unavailable", false, err
		}
	}
	if run.Phase == domain.RemotePrepared || run.Phase == domain.RemoteSubmitIntent {
		hash := sha256.Sum256([]byte(run.RequestBodyJSON))
		var body struct {
			Model string `json:"model"`
		}
		if run.RequestBodyJSON == "" || hex.EncodeToString(hash[:]) != run.RequestBodyHash || json.Unmarshal([]byte(run.RequestBodyJSON), &body) != nil || body.Model != task.Model {
			return "submission_evidence_invalid", true, domain.ErrRemoteReconciliation
		}
		if err := p.Store.BeginRemoteSubmission(ctx, run); err != nil {
			return "submit_intent_conflict", false, err
		}
		p.log("提交远程任务", task.TaskID)
		id, err := p.Client.Submit(ctx, []byte(run.RequestBodyJSON), run.SubmissionKey)
		if err != nil {
			var upstream *tk2sd.HTTPError
			if errors.As(err, &upstream) && upstream.StatusCode == 409 {
				return "submit_conflict", true, domain.ErrRemoteReconciliation
			}
			if rejected(err) {
				if run.SubmitAttempts > 0 {
					return "replay_rejected", true, domain.ErrRemoteReconciliation
				}
				return "submit_rejected", false, p.complete(ctx, run, domain.RemoteCompletion{Status: "failed", ErrorCode: "tk2sd_submit_rejected", ErrorMessage: "远程服务拒绝生成请求", Feedback: feedback(err)})
			}
			return "submit_unknown", false, domain.ErrRemotePending
		}
		if id == "" {
			return "submit_invalid_response", false, domain.ErrRemotePending
		}
		if err = p.Store.BindRemoteTask(ctx, run, id); err != nil {
			return "submit_bind_failed", false, err
		}
		run, err = p.reloadRun(ctx, run)
		if err != nil {
			return "store_unavailable", false, err
		}
	}
	if run.Phase != domain.RemoteSubmitted || run.UpstreamTaskID == "" {
		return "submission_evidence_invalid", true, domain.ErrRemoteReconciliation
	}
	result, err := p.Client.Query(ctx, run.UpstreamTaskID)
	if err != nil {
		return "poll_unavailable", false, domain.ErrRemotePending
	}
	if result.ID != run.UpstreamTaskID || result.Model != task.Model {
		return "poll_identity_mismatch", false, domain.ErrRemotePending
	}
	// 重新读取取消意图，处理查询期间到达的取消请求。
	current, err := p.Store.GetRemoteRun(ctx, task.TaskID)
	if err != nil {
		return "store_unavailable", false, err
	}
	if current.LeaseToken != run.LeaseToken {
		return "lease_lost", false, domain.ErrStateConflict
	}
	run = current
	switch result.Status {
	case "queued", "running":
		cancelState := run.CancelState
		if cancelState == "requested" {
			if result.Status == "running" {
				cancelState = "rejected"
			} else {
				if err = p.Client.Cancel(ctx, run.UpstreamTaskID); err == nil {
					return "", false, p.complete(ctx, run, domain.RemoteCompletion{Status: "cancelled"})
				}
				var upstream *tk2sd.HTTPError
				if errors.As(err, &upstream) && upstream.StatusCode == 409 {
					confirmed, queryErr := p.Client.Query(ctx, run.UpstreamTaskID)
					if queryErr == nil && confirmed.ID == run.UpstreamTaskID && confirmed.Model == task.Model {
						if confirmed.Status == "cancelled" || confirmed.Status == "failed" || confirmed.Status == "succeeded" {
							return p.terminal(ctx, task, run, confirmed)
						}
						if confirmed.Status == "running" {
							cancelState = "rejected"
							result.Status = "running"
						}
					}
				}
			}
		}
		if err = p.Store.ObserveRemoteTask(ctx, run, result.Status, cancelState); err != nil {
			return "poll_store_failed", false, err
		}
		return "", false, domain.ErrRemotePending
	case "succeeded", "failed", "cancelled":
		return p.terminal(ctx, task, run, result)
	default:
		return "poll_status_invalid", false, domain.ErrRemotePending
	}
}

func (p *Processor) terminal(ctx context.Context, task domain.Task, run domain.RemoteRun, result tk2sd.Task) (string, bool, error) {
	completion := domain.RemoteCompletion{Status: result.Status}
	if result.Status == "succeeded" {
		base := p.NodeURL
		if base == nil {
			var snapshot struct {
				URL string `json:"service_url"`
			}
			if json.Unmarshal([]byte(task.DispatchSnapshotJSON), &snapshot) == nil {
				base, _ = url.Parse(snapshot.URL)
			}
		}
		expires, err := ValidateResultURL(ctx, base, run.UpstreamTaskID, result.VideoURL, p.now(), nil)
		if err != nil {
			return "result_invalid", false, domain.ErrRemotePending
		}
		completion.ResultURL, completion.ResultExpiresAt, completion.MetadataStatus = result.VideoURL, expires, "pending"
		if meta, err := p.Client.Metadata(ctx, run.UpstreamTaskID); err == nil {
			if data, ratio, err := ActualMetadata(meta); err == nil {
				completion.MetadataJSON, completion.Ratio, completion.MetadataStatus = data, ratio, "ready"
			}
		}
		if task.DeliveryRequired {
			completion.UploadJob = &domain.ResultUploadJob{ID: "result-upload-" + task.TaskID, TaskID: task.TaskID, ObjectKey: fmt.Sprintf("MiniMax-H3/%s/%s.mp4", p.now().Format("2006-01-02"), task.TaskID)}
		}
	} else if result.Status == "failed" {
		completion.ErrorCode, completion.ErrorMessage = "tk2sd_generation_failed", "远程视频生成失败"
		if result.Error != nil {
			completion.Feedback = &domain.UpstreamFeedback{Code: result.Error.Code, Message: result.Error.Message}
		}
	}
	return "", false, p.complete(ctx, run, completion)
}

func (p *Processor) complete(ctx context.Context, run domain.RemoteRun, result domain.RemoteCompletion) error {
	err := p.Store.CompleteRemoteRun(ctx, run, result)
	if err == nil {
		p.log("远程任务完成", run.TaskID)
	}
	return err
}

func (p *Processor) reloadRun(ctx context.Context, previous domain.RemoteRun) (domain.RemoteRun, error) {
	current, err := p.Store.GetRemoteRun(ctx, previous.TaskID)
	if err != nil {
		return previous, err
	}
	if current.LeaseToken != previous.LeaseToken || current.NodeID != previous.NodeID {
		return previous, domain.ErrStateConflict
	}
	return current, nil
}

func (p *Processor) RequestCancel(ctx context.Context, taskID string) error {
	return p.Store.RequestRemoteCancel(ctx, taskID)
}
func (p *Processor) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}
func (p *Processor) log(message, taskID string) {
	if p.Logger != nil {
		p.Logger.Info(message, "task_id", taskID, "node_id", p.NodeID, "protocol", domain.ProtocolTK2SD)
	}
}
func rejected(err error) bool {
	var e *tk2sd.HTTPError
	return errors.As(err, &e) && (e.StatusCode == 400 || e.StatusCode == 413 || e.StatusCode == 422)
}
func feedback(err error) *domain.UpstreamFeedback {
	var e *tk2sd.HTTPError
	if errors.As(err, &e) {
		return &domain.UpstreamFeedback{HTTPStatus: e.StatusCode, Code: e.Code, Message: e.Message}
	}
	return nil
}
