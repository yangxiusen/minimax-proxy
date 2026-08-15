package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/google/uuid"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/logsafe"
	"minimax-h3-tc/internal/mediagate"
	"minimax-h3-tc/internal/store/sqlite"
	"minimax-h3-tc/internal/upstream/nodeapi"
)

type Store interface {
	GetNodeDispatchBarrier(context.Context, string) (sqlite.NodeDispatchBarrier, error)
	BindBarrierExecution(context.Context, string, int64, string) error
	DeferNodeDispatchBarrier(context.Context, string, int64, string, time.Time) error
	ResolveNodeDispatchBarrier(context.Context, string, int64) error
	HasNodeDispatchBarrier(context.Context, string) (bool, error)
	ClaimStage(context.Context, string, string, time.Duration) (sqlite.TaskStage, error)
	RenewStageLease(context.Context, string, string, time.Duration) error
	CreateStageAttempt(context.Context, sqlite.StageAttempt) error
	BindStageExecution(context.Context, string, string, string, string) error
	GetRunningStageAttempt(context.Context, string, string) (sqlite.StageAttempt, error)
	CompleteStage(context.Context, string, string, string, string) error
	CompleteStageWithOutput(context.Context, string, string, string, string, string, string, int64, string, string) (string, error)
	FailStage(context.Context, string, string, string, string, string, time.Time, bool) error
	GetActiveArtifactLocation(context.Context, string, string) (sqlite.ArtifactLocation, error)
	GetPrimaryArtifactLocation(context.Context, string) (sqlite.ArtifactLocation, error)
	GetTaskForExecution(context.Context, string) (domain.Task, error)
	GetInputSpoolFile(context.Context, string, string) (domain.InputSpoolFile, error)
	RegisterInputArtifact(context.Context, string, string, string, string, string, int64, string, string) error
}

type InputArtifactMigrator interface {
	Migrate(context.Context, MigrationCommand) (sqlite.ArtifactLocation, error)
}

type NodeClient interface {
	Health(context.Context, string) (nodeapi.Health, error)
	CreateExecution(context.Context, string, nodeapi.ExecutionRequest) (nodeapi.ExecutionReference, error)
	GetExecution(context.Context, string, string) (nodeapi.Execution, error)
	GetArtifact(context.Context, string, string) (nodeapi.Artifact, error)
	ImportArtifact(context.Context, string, nodeapi.ImportArtifactRequest) (nodeapi.Artifact, error)
	CancelExecution(context.Context, string, string, string) (nodeapi.ExecutionReference, error)
}

type Processor struct {
	Store         Store
	Client        NodeClient
	NodeID        string
	Migrator      InputArtifactMigrator
	Inputs        *InputMaterializer
	LeaseDuration time.Duration
	PollInterval  time.Duration
	Logger        *slog.Logger
	Now           func() time.Time
}

func (processor *Processor) ProcessOne(ctx context.Context) error {
	if processor.Store == nil || processor.Client == nil || processor.NodeID == "" {
		return errors.New("阶段编排依赖未配置")
	}
	barrier, err := processor.Store.GetNodeDispatchBarrier(ctx, processor.NodeID)
	if err == nil {
		if barrier.NextRetryAt > processor.now().UnixMilli() {
			return domain.ErrQueueEmpty
		}
		return processor.reconcileCancellationBarrier(ctx, barrier)
	}
	if !errors.Is(err, sqlite.ErrNodeDispatchBarrierNotFound) {
		return err
	}
	leaseToken := uuid.NewString()
	stage, err := processor.Store.ClaimStage(ctx, processor.NodeID, leaseToken, processor.leaseDuration())
	if errors.Is(err, sqlite.ErrNoClaimableStage) {
		return domain.ErrQueueEmpty
	}
	if err != nil {
		return err
	}
	processor.logger().InfoContext(ctx, "任务阶段已认领", "task_id", stage.TaskID, "stage_id", stage.ID, "node_id", processor.NodeID, "stage_type", stage.StageType, "attempt_no", stage.AttemptCount+1, "stage", "stage_claim")
	if cancelled, err := processor.taskCancelled(ctx, stage.TaskID); err != nil || cancelled {
		return err
	}
	stageForNode, err := processor.localizeInput(ctx, stage)
	if err != nil {
		return processor.fail(ctx, stage, sqlite.StageAttempt{}, "artifact_migration_failed", operationErrorMessage("输入产物无法迁移到目标节点", err), false)
	}
	request, err := executionRequest(stageForNode)
	if err != nil {
		return processor.fail(ctx, stage, sqlite.StageAttempt{}, "invalid_stage_snapshot", err.Error(), true)
	}
	if stage.StageType == "generation" && generationUsesInputs(request.Parameters) {
		if processor.Inputs == nil {
			return processor.fail(ctx, stage, sqlite.StageAttempt{}, "input_materializer_unavailable", "输入素材服务未配置", true)
		}
		request.InputArtifacts, err = processor.Inputs.Materialize(ctx, stage.TaskID, processor.NodeID, "stage-inputs-"+stage.ID, processor.Client)
		if err != nil {
			if cancelled, taskErr := processor.taskCancelled(ctx, stage.TaskID); taskErr != nil || cancelled {
				return taskErr
			}
			return processor.fail(ctx, stage, sqlite.StageAttempt{}, "input_materialization_failed", materializationErrorMessage(err), false)
		}
		if cancelled, err := processor.taskCancelled(ctx, stage.TaskID); err != nil || cancelled {
			return err
		}
		processor.logger().InfoContext(ctx, "输入素材处理完成", "task_id", stage.TaskID, "stage_id", stage.ID, "node_id", processor.NodeID, "input_count", len(request.InputArtifacts), "stage", "input_materialization")
	}
	if stage.AttemptCount > 0 {
		attempt, attemptErr := processor.Store.GetRunningStageAttempt(ctx, stage.ID, stage.LeaseToken)
		if attemptErr == nil && (attempt.Status == "dispatching" || attempt.Status == "running" || attempt.Status == "validating" || attempt.Status == "unknown") {
			request.OperationID = attempt.OperationID
			request.ExternalTaskID = stage.TaskID
			request.StageID = stage.ID
			if attempt.ExecutionID == "" {
				processor.logger().InfoContext(ctx, "节点阶段恢复提交开始", "task_id", stage.TaskID, "stage_id", stage.ID, "attempt_id", attempt.ID, "node_id", processor.NodeID, "stage", "stage_submit")
				reference, submitErr := processor.Client.CreateExecution(ctx, "stage-resubmit-"+attempt.ID, request)
				if submitErr != nil {
					return processor.fail(ctx, stage, attempt, classifySubmitError(submitErr), operationErrorMessage("节点阶段恢复提交失败", submitErr), false)
				}
				if reference.ExecutionID == "" {
					return processor.fail(ctx, stage, attempt, "node_protocol_error", "节点未返回 execution_id", false)
				}
				attempt.ExecutionID = reference.ExecutionID
			}
			if bindErr := processor.Store.BindStageExecution(ctx, stage.ID, stage.LeaseToken, attempt.ID, attempt.ExecutionID); bindErr != nil {
				return bindErr
			}
			return processor.poll(ctx, stage, attempt)
		}
		if attemptErr != nil && !errors.Is(attemptErr, sqlite.ErrNoClaimableStage) {
			return attemptErr
		}
	}
	attempt := sqlite.StageAttempt{
		ID: uuid.NewString(), StageID: stage.ID, AttemptNo: stage.AttemptCount + 1,
		OperationID: uuid.NewString(), NodeID: processor.NodeID, LeaseToken: stage.LeaseToken, Status: "dispatching", InputArtifactID: stage.InputArtifactID,
	}
	request.OperationID = attempt.OperationID
	request.ExternalTaskID = stage.TaskID
	request.StageID = stage.ID
	requestSnapshot, err := json.Marshal(request)
	if err != nil {
		return err
	}
	attempt.RequestSnapshotJSON = string(requestSnapshot)
	if err := processor.Store.CreateStageAttempt(ctx, attempt); err != nil {
		return err
	}
	processor.logger().InfoContext(ctx, "节点阶段提交开始", "task_id", stage.TaskID, "stage_id", stage.ID, "attempt_id", attempt.ID, "node_id", processor.NodeID, "stage", "stage_submit")
	reference, err := processor.Client.CreateExecution(ctx, "stage-submit-"+attempt.ID, request)
	if err != nil {
		return processor.fail(ctx, stage, attempt, classifySubmitError(err), operationErrorMessage("节点阶段提交失败", err), false)
	}
	if reference.ExecutionID == "" {
		return processor.fail(ctx, stage, attempt, "node_protocol_error", "节点未返回 execution_id", false)
	}
	if err := processor.Store.BindStageExecution(ctx, stage.ID, leaseToken, attempt.ID, reference.ExecutionID); err != nil {
		return err
	}
	attempt.ExecutionID = reference.ExecutionID
	processor.logger().InfoContext(ctx, "节点阶段已接受", "task_id", stage.TaskID, "stage_id", stage.ID, "attempt_id", attempt.ID, "execution_id", attempt.ExecutionID, "node_id", processor.NodeID, "node_status", reference.Status, "stage", "stage_submit")
	return processor.poll(ctx, stage, attempt)
}

func (processor *Processor) reconcileCancellationBarrier(ctx context.Context, barrier sqlite.NodeDispatchBarrier) error {
	if barrier.ExecutionID == "" {
		if barrier.RequestSnapshotJSON == "" {
			return processor.deferCancellationBarrier(ctx, barrier, "execution_request_snapshot_missing")
		}
		var request nodeapi.ExecutionRequest
		if err := json.Unmarshal([]byte(barrier.RequestSnapshotJSON), &request); err != nil || request.OperationID != barrier.OperationID {
			return processor.deferCancellationBarrier(ctx, barrier, "execution_request_snapshot_invalid")
		}
		reference, err := processor.Client.CreateExecution(ctx, "cancel-recover-"+barrier.AttemptID, request)
		if err != nil {
			return processor.deferCancellationBarrier(ctx, barrier, cancellationErrorCode("node_execution_recovery_failed", err))
		}
		if reference.ExecutionID == "" {
			return processor.deferCancellationBarrier(ctx, barrier, "node_protocol_error")
		}
		if err := processor.Store.BindBarrierExecution(ctx, barrier.NodeID, barrier.RowVersion, reference.ExecutionID); err != nil {
			return err
		}
		barrier.ExecutionID = reference.ExecutionID
		barrier.RowVersion++
	}

	_, cancelErr := processor.Client.CancelExecution(ctx, "cancel-reconcile-"+barrier.AttemptID, barrier.ExecutionID, barrier.CancelOperationID)
	execution, getErr := processor.Client.GetExecution(ctx, "cancel-check-"+barrier.AttemptID, barrier.ExecutionID)
	if getErr == nil {
		if terminalNodeExecution(execution.Status) {
			return processor.Store.ResolveNodeDispatchBarrier(ctx, barrier.NodeID, barrier.RowVersion)
		}
		code := "node_cancel_reconciling"
		if cancelErr != nil {
			code = cancellationErrorCode("node_cancel_unavailable", cancelErr)
		}
		return processor.deferCancellationBarrier(ctx, barrier, code)
	}
	if nodeExecutionNotFound(getErr) {
		health, healthErr := processor.Client.Health(ctx, "cancel-health-"+barrier.AttemptID)
		if healthErr == nil && nodeQueueExplicitlyIdle(health) {
			return processor.Store.ResolveNodeDispatchBarrier(ctx, barrier.NodeID, barrier.RowVersion)
		}
		if healthErr != nil {
			return processor.deferCancellationBarrier(ctx, barrier, cancellationErrorCode("node_health_unavailable", healthErr))
		}
		return processor.deferCancellationBarrier(ctx, barrier, "node_queue_not_idle")
	}
	return processor.deferCancellationBarrier(ctx, barrier, cancellationErrorCode("node_execution_unavailable", getErr))
}

func (processor *Processor) deferCancellationBarrier(ctx context.Context, barrier sqlite.NodeDispatchBarrier, code string) error {
	retryAt := processor.now().Add(processor.pollInterval())
	if err := processor.Store.DeferNodeDispatchBarrier(ctx, barrier.NodeID, barrier.RowVersion, code, retryAt); err != nil {
		return err
	}
	return nil
}

func terminalNodeExecution(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
}

func nodeExecutionNotFound(err error) bool {
	var responseError *nodeapi.HTTPError
	return errors.As(err, &responseError) && responseError.StatusCode == 404
}

func nodeQueueExplicitlyIdle(health nodeapi.Health) bool {
	return health.Status == "healthy" && health.Runtime != nil && health.Runtime.QueueRunning != nil && health.Runtime.QueuePending != nil && *health.Runtime.QueueRunning == 0 && *health.Runtime.QueuePending == 0
}

func cancellationErrorCode(fallback string, err error) string {
	var responseError *nodeapi.HTTPError
	if !errors.As(err, &responseError) {
		return fallback
	}
	if responseError.StatusCode == 401 || responseError.StatusCode == 403 {
		return "node_authentication_failed"
	}
	if responseError.StatusCode >= 500 {
		return fallback
	}
	if responseError.Code != "" {
		return responseError.Code
	}
	return fallback
}

func generationUsesInputs(parameters json.RawMessage) bool {
	var value struct {
		Scenario string `json:"scenario"`
	}
	return json.Unmarshal(parameters, &value) == nil && value.Scenario != "" && value.Scenario != "t2va"
}

func (processor *Processor) localizeInput(ctx context.Context, stage sqlite.TaskStage) (sqlite.TaskStage, error) {
	if stage.InputArtifactID == "" {
		return stage, nil
	}
	logicalID := stage.InputArtifactID
	local, err := processor.Store.GetActiveArtifactLocation(ctx, logicalID, processor.NodeID)
	if err == nil {
		stage.InputArtifactID = local.NodeArtifactID
		return stage, nil
	}
	if !errors.Is(err, sqlite.ErrArtifactNotFound) {
		return stage, err
	}
	if processor.Migrator == nil {
		return stage, errors.New("跨节点产物迁移器未配置")
	}
	primary, err := processor.Store.GetPrimaryArtifactLocation(ctx, logicalID)
	if err != nil {
		return stage, err
	}
	if primary.NodeID == processor.NodeID {
		return stage, errors.New("目标节点主产物位置异常")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(logicalID+"\x00"+processor.NodeID)))
	local, err = processor.Migrator.Migrate(ctx, MigrationCommand{
		RequestID: "stage-input-" + stage.ID, OperationID: "migrate-" + digest,
		ArtifactID: logicalID, SourceNodeID: primary.NodeID, TargetNodeID: processor.NodeID,
		TargetLocationID: "loc-" + digest, Filename: logicalID + ".mp4",
	})
	if err != nil {
		return stage, err
	}
	stage.InputArtifactID = local.NodeArtifactID
	return stage, nil
}

func (processor *Processor) poll(ctx context.Context, stage sqlite.TaskStage, attempt sqlite.StageAttempt) error {
	interval := processor.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	renewEvery := processor.leaseDuration() / 3
	if renewEvery < time.Second {
		renewEvery = time.Second
	}
	lastRenewed := processor.now()
	lastStatus := ""
	for {
		task, taskErr := processor.Store.GetTaskForExecution(ctx, stage.TaskID)
		if taskErr != nil {
			return taskErr
		}
		if task.Status == domain.StatusCancelling {
			_, _ = processor.Client.CancelExecution(ctx, "stage-cancel-"+attempt.ID, attempt.ExecutionID, "stage-cancel-"+attempt.ID)
			return nil
		}
		if task.Status == domain.StatusCancelled {
			_, _ = processor.Client.CancelExecution(ctx, "stage-cancel-"+attempt.ID, attempt.ExecutionID, "stage-cancel-"+attempt.ID)
			return nil
		}
		if processor.now().Sub(lastRenewed) >= renewEvery {
			if err := processor.Store.RenewStageLease(ctx, stage.ID, stage.LeaseToken, processor.leaseDuration()); err != nil {
				return err
			}
			lastRenewed = processor.now()
		}
		execution, err := processor.Client.GetExecution(ctx, "stage-poll-"+attempt.ID, attempt.ExecutionID)
		if err != nil {
			if code, terminal := terminalExecutionReadError(err); terminal {
				return processor.fail(ctx, stage, attempt, code, operationErrorMessage("节点执行状态不可恢复", err), true)
			}
			processor.logger().WarnContext(ctx, "节点阶段状态读取失败，将继续重试", "task_id", stage.TaskID, "stage_id", stage.ID, "attempt_id", attempt.ID, "execution_id", attempt.ExecutionID, "node_id", processor.NodeID, "stage", "stage_poll", "error_code", "node_execution_unavailable", "error_reason", logsafe.Error(err))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			}
			continue
		}
		if execution.Status != lastStatus {
			processor.logger().InfoContext(ctx, "节点阶段状态变更", "task_id", stage.TaskID, "stage_id", stage.ID, "attempt_id", attempt.ID, "execution_id", attempt.ExecutionID, "node_id", processor.NodeID, "node_status", execution.Status, "stage", "stage_poll")
			lastStatus = execution.Status
		}
		switch execution.Status {
		case "accepted", "queued", "running", "validating", "cancelling", "unknown":
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			}
		case "succeeded":
			if execution.ResultArtifactID == "" {
				return processor.fail(ctx, stage, attempt, "node_protocol_error", "节点成功响应缺少产物", false)
			}
			artifact, metadataErr := processor.Client.GetArtifact(ctx, "stage-artifact-"+attempt.ID, execution.ResultArtifactID)
			if metadataErr != nil {
				return processor.fail(ctx, stage, attempt, "node_artifact_unavailable", operationErrorMessage("节点阶段产物元数据不可用", metadataErr), false)
			}
			if artifact.ArtifactID != execution.ResultArtifactID || artifact.Kind != "video" || artifact.State != "active" || artifact.SizeBytes <= 0 || len(artifact.SHA256) != 64 || len(artifact.MediaManifest) == 0 || string(artifact.MediaManifest) == "null" {
				return processor.fail(ctx, stage, attempt, "node_artifact_invalid", "节点阶段产物元数据无效", true)
			}
			request, requestErr := executionRequest(stage)
			if requestErr != nil {
				return processor.fail(ctx, stage, attempt, "node_artifact_invalid", "冻结阶段配置不可解析", true)
			}
			if validationErr := mediagate.Validate(stage.StageType, request.Parameters, artifact.MediaManifest); validationErr != nil {
				return processor.fail(ctx, stage, attempt, "node_artifact_invalid", validationErr.Error(), true)
			}
			logicalArtifactID, completeErr := processor.Store.CompleteStageWithOutput(
				ctx, stage.TaskID, stage.ID, stage.LeaseToken, attempt.ID, processor.NodeID, artifact.ArtifactID,
				artifact.SizeBytes, artifact.SHA256, string(artifact.MediaManifest),
			)
			if completeErr != nil {
				return completeErr
			}
			processor.logger().InfoContext(ctx, "任务阶段执行完成", "task_id", stage.TaskID, "stage_id", stage.ID, "attempt_id", attempt.ID, "execution_id", attempt.ExecutionID, "node_id", processor.NodeID, "artifact_id", logicalArtifactID, "stage", "stage_complete")
			return nil
		case "failed", "cancelled":
			code, retryable := "node_execution_failed", false
			message := "节点阶段执行失败"
			if execution.Error != nil {
				code, retryable = execution.Error.Code, execution.Error.Retryable
				if execution.Error.Message != "" {
					message = execution.Error.Message
				}
			}
			return processor.fail(ctx, stage, attempt, code, message, !retryable)
		default:
			return processor.fail(ctx, stage, attempt, "node_protocol_error", "节点返回未知状态", false)
		}
	}
}

func (processor *Processor) taskCancelled(ctx context.Context, taskID string) (bool, error) {
	task, err := processor.Store.GetTaskForExecution(ctx, taskID)
	if err != nil {
		return false, err
	}
	return task.Status == domain.StatusCancelled, nil
}

func executionRequest(stage sqlite.TaskStage) (nodeapi.ExecutionRequest, error) {
	var snapshot struct {
		StageType     string                `json:"stage_type"`
		Parameters    json.RawMessage       `json:"parameters"`
		Profile       map[string]any        `json:"profile"`
		ExpectedMedia nodeapi.ExpectedMedia `json:"expected_media"`
	}
	if err := json.Unmarshal([]byte(stage.ConfigSnapshotJSON), &snapshot); err != nil {
		return nodeapi.ExecutionRequest{}, err
	}
	parameters := snapshot.Parameters
	if len(parameters) == 0 {
		derived, err := deriveParameters(stage.StageType, snapshot.Profile)
		if err != nil {
			return nodeapi.ExecutionRequest{}, err
		}
		parameters = derived
	}
	inputArtifacts := []nodeapi.InputArtifact{}
	if stage.InputArtifactID != "" {
		inputArtifacts = append(inputArtifacts, nodeapi.InputArtifact{ArtifactID: stage.InputArtifactID})
	}
	return nodeapi.ExecutionRequest{
		StageType: stage.StageType, InputArtifacts: inputArtifacts, Parameters: parameters,
		ExpectedMedia: snapshot.ExpectedMedia,
	}, nil
}

func deriveParameters(stageType string, profile map[string]any) (json.RawMessage, error) {
	if profile == nil {
		return nil, errors.New("阶段配置缺少 parameters")
	}
	var value any
	switch stageType {
	case "interpolation":
		value = profile["interpolation"]
	case "restoration":
		value = profile["restoration"]
	case "watermark":
		watermark, _ := profile["watermark"].(map[string]any)
		value = map[string]any{"enabled": true, "aigc_watermark": true, "text": watermark["text"]}
	default:
		return nil, fmt.Errorf("阶段 %s 必须冻结显式 parameters", stageType)
	}
	return json.Marshal(value)
}

func (processor *Processor) fail(ctx context.Context, stage sqlite.TaskStage, attempt sqlite.StageAttempt, code, message string, terminal bool) error {
	if attempt.ID == "" {
		attempt = sqlite.StageAttempt{ID: uuid.NewString(), StageID: stage.ID, AttemptNo: stage.AttemptCount + 1, OperationID: uuid.NewString(), NodeID: processor.NodeID, LeaseToken: stage.LeaseToken, Status: "dispatching"}
		if err := processor.Store.CreateStageAttempt(ctx, attempt); err != nil {
			return err
		}
	}
	terminal = terminal || stage.AttemptCount+1 >= stage.MaxAttempts || !retryableCode(code)
	next := processor.now().Add(backoff(stage.AttemptCount + 1))
	processor.logger().ErrorContext(ctx, "任务阶段执行失败", "task_id", stage.TaskID, "stage_id", stage.ID, "attempt_id", attempt.ID, "node_id", processor.NodeID, "stage", "stage_failure", "error_code", code, "error_reason", sanitize(message), "terminal", terminal, "retry_at", next)
	return processor.Store.FailStage(ctx, stage.ID, stage.LeaseToken, attempt.ID, code, sanitize(message), next, terminal)
}

func classifySubmitError(err error) string {
	var responseError *nodeapi.HTTPError
	if errors.As(err, &responseError) && responseError.Code != "" {
		return responseError.Code
	}
	return "node_submit_unavailable"
}

func materializationErrorMessage(err error) string {
	var phased interface{ InputMaterializationPhase() string }
	if errors.As(err, &phased) {
		if phased.InputMaterializationPhase() == "read" {
			return "输入素材下载失败：" + logsafe.Error(err)
		}
		return "输入素材导入失败：" + logsafe.Error(err)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return "输入素材下载失败：" + logsafe.Error(urlErr)
	}
	return "输入素材导入失败：" + logsafe.Error(err)
}

func operationErrorMessage(prefix string, err error) string {
	return prefix + "：" + logsafe.Error(err)
}

func terminalExecutionReadError(err error) (string, bool) {
	var responseError *nodeapi.HTTPError
	if !errors.As(err, &responseError) || responseError.StatusCode == 408 || responseError.StatusCode == 429 || responseError.StatusCode >= 500 {
		return "", false
	}
	if responseError.StatusCode >= 400 && responseError.StatusCode < 500 {
		if responseError.Code != "" {
			return responseError.Code, true
		}
		return "node_execution_rejected", true
	}
	return "", false
}

func retryableCode(code string) bool {
	switch code {
	case "node_submit_unavailable", "node_execution_unavailable", "node_artifact_unavailable", "comfyui_unavailable", "stage_runtime_unavailable", "artifact_locked":
		return true
	default:
		return false
	}
}

func backoff(attempt int) time.Duration {
	if attempt > 8 {
		attempt = 8
	}
	return time.Duration(1<<max(attempt-1, 0)) * time.Second
}

func sanitize(message string) string {
	if len(message) > 512 {
		return message[:512]
	}
	return message
}

func (processor *Processor) leaseDuration() time.Duration {
	if processor.LeaseDuration > 0 {
		return processor.LeaseDuration
	}
	return 10 * time.Minute
}

func (processor *Processor) pollInterval() time.Duration {
	if processor.PollInterval > 0 {
		return processor.PollInterval
	}
	return time.Second
}

func (processor *Processor) now() time.Time {
	if processor.Now != nil {
		return processor.Now().UTC()
	}
	return time.Now().UTC()
}

func (processor *Processor) logger() *slog.Logger {
	if processor.Logger != nil {
		return processor.Logger
	}
	return slog.Default()
}
