package orchestrator

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/store/sqlite"
	"minimax-h3-tc/internal/upstream/nodeapi"
)

func TestProcessorCompletesFrozenStageAndRenewsLease(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	store := &stageStoreFake{stage: frozenStage(), onClaim: func(stage *sqlite.TaskStage, lease string) { stage.LeaseToken = lease }}
	client := &nodeClientFake{
		reference: nodeapi.ExecutionReference{ExecutionID: "exe-1", Status: "accepted"},
		executions: []nodeapi.Execution{
			{ExecutionID: "exe-1", Status: "running"},
			{ExecutionID: "exe-1", Status: "succeeded", ResultArtifactID: "node-artifact-1"},
		},
		onGet:    func() { now = now.Add(4 * time.Second) },
		artifact: nodeArtifact("node-artifact-1"),
	}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", LeaseDuration: 9 * time.Second, PollInterval: time.Nanosecond, Now: func() time.Time { return now }}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.completedArtifact != "logical-artifact-1" || store.renewed == 0 {
		t.Fatalf("completed=%q renewed=%d", store.completedArtifact, store.renewed)
	}
	if client.created.StageType != "generation" || client.created.ExternalTaskID != "task-1" || len(client.created.Parameters) == 0 {
		t.Fatalf("create request=%+v", client.created)
	}
}

func TestProcessorLogsStageSubmissionStatusAndCompletion(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	store := &stageStoreFake{stage: frozenStage(), onClaim: func(stage *sqlite.TaskStage, lease string) { stage.LeaseToken = lease }}
	client := &nodeClientFake{
		reference:  nodeapi.ExecutionReference{ExecutionID: "exe-log", Status: "accepted"},
		executions: []nodeapi.Execution{{ExecutionID: "exe-log", Status: "running"}, {ExecutionID: "exe-log", Status: "succeeded", ResultArtifactID: "artifact-log"}},
		artifact:   nodeArtifact("artifact-log"),
	}
	processor := Processor{Store: store, Client: client, NodeID: "node-log", PollInterval: time.Nanosecond, Logger: logger}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	output := logs.String()
	for _, expected := range []string{
		"任务阶段已认领", "节点阶段提交开始", "节点阶段已接受", "节点阶段状态变更", "任务阶段执行完成",
		`"task_id":"task-1"`, `"stage_id":"stage-1"`, `"node_id":"node-log"`, `"execution_id":"exe-log"`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("logs missing %q: %s", expected, output)
		}
	}
}

func TestProcessorLogsNodeSubmissionFailureReason(t *testing.T) {
	var logs bytes.Buffer
	store := &stageStoreFake{stage: frozenStage(), onClaim: func(stage *sqlite.TaskStage, lease string) { stage.LeaseToken = lease }}
	processor := Processor{
		Store: store, Client: &nodeClientFake{createErr: errors.New("connection reset by peer")}, NodeID: "node-log",
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(store.failedMessage, "connection reset by peer") {
		t.Fatalf("failure reason missing from task: %q", store.failedMessage)
	}
	output := logs.String()
	if !strings.Contains(output, "任务阶段执行失败") || !strings.Contains(output, `"error_reason":"节点阶段提交失败：connection reset by peer"`) {
		t.Fatalf("submission failure reason missing: %s", output)
	}
}

func TestProcessorResumesExpiredAttemptWithSameOperation(t *testing.T) {
	stage := frozenStage()
	stage.AttemptCount = 1
	store := &stageStoreFake{
		stage: stage,
		attempt: sqlite.StageAttempt{
			ID: "attempt-1", StageID: stage.ID, AttemptNo: 1, OperationID: "operation-1", Status: "dispatching",
		},
		onClaim: func(stage *sqlite.TaskStage, lease string) { stage.LeaseToken = lease },
	}
	client := &nodeClientFake{
		reference:  nodeapi.ExecutionReference{ExecutionID: "exe-resumed", Status: "accepted"},
		executions: []nodeapi.Execution{{ExecutionID: "exe-resumed", Status: "succeeded", ResultArtifactID: "artifact-resumed"}},
		artifact:   nodeArtifact("artifact-resumed"),
	}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", PollInterval: time.Nanosecond}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.createdAttempts != 0 || client.created.OperationID != "operation-1" || store.completedArtifact != "logical-artifact-1" {
		t.Fatalf("createdAttempts=%d request=%+v completed=%q", store.createdAttempts, client.created, store.completedArtifact)
	}
}

func TestProcessorRetriesTransientExecutionReadsWithoutCreatingAnotherAttempt(t *testing.T) {
	store := &stageStoreFake{stage: frozenStage(), onClaim: func(stage *sqlite.TaskStage, lease string) { stage.LeaseToken = lease }}
	client := &nodeClientFake{
		reference:  nodeapi.ExecutionReference{ExecutionID: "exe-1", Status: "accepted"},
		getErrors:  []error{errors.New("temporary connection reset")},
		executions: []nodeapi.Execution{{ExecutionID: "exe-1", Status: "succeeded", ResultArtifactID: "artifact-1"}},
		artifact:   nodeArtifact("artifact-1"),
	}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", LeaseDuration: time.Minute, PollInterval: time.Nanosecond}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.createdAttempts != 1 || store.failedCode != "" || store.completedArtifact != "logical-artifact-1" {
		t.Fatalf("createdAttempts=%d failed=%q completed=%q", store.createdAttempts, store.failedCode, store.completedArtifact)
	}
}

func TestProcessorLogsTransientExecutionReadReason(t *testing.T) {
	var logs bytes.Buffer
	store := &stageStoreFake{stage: frozenStage(), onClaim: func(stage *sqlite.TaskStage, lease string) { stage.LeaseToken = lease }}
	client := &nodeClientFake{
		reference:  nodeapi.ExecutionReference{ExecutionID: "exe-retry", Status: "accepted"},
		getErrors:  []error{errors.New("temporary connection reset")},
		executions: []nodeapi.Execution{{ExecutionID: "exe-retry", Status: "succeeded", ResultArtifactID: "artifact-retry"}},
		artifact:   nodeArtifact("artifact-retry"),
	}
	processor := Processor{Store: store, Client: client, NodeID: "node-log", PollInterval: time.Nanosecond, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	output := logs.String()
	if !strings.Contains(output, "节点阶段状态读取失败，将继续重试") || !strings.Contains(output, `"error_reason":"temporary connection reset"`) {
		t.Fatalf("transient read reason missing: %s", output)
	}
}

func TestProcessorPreservesArtifactReadFailureReason(t *testing.T) {
	var logs bytes.Buffer
	store := &stageStoreFake{stage: frozenStage(), onClaim: func(stage *sqlite.TaskStage, lease string) { stage.LeaseToken = lease }}
	client := &nodeClientFake{
		reference:   nodeapi.ExecutionReference{ExecutionID: "exe-artifact", Status: "accepted"},
		executions:  []nodeapi.Execution{{ExecutionID: "exe-artifact", Status: "succeeded", ResultArtifactID: "artifact-broken"}},
		artifactErr: errors.New("artifact registry locked"),
	}
	processor := Processor{Store: store, Client: client, NodeID: "node-log", PollInterval: time.Nanosecond, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(store.failedMessage, "artifact registry locked") {
		t.Fatalf("artifact failure reason missing from task: %q", store.failedMessage)
	}
	if !strings.Contains(logs.String(), `"error_reason":"节点阶段产物元数据不可用：artifact registry locked"`) {
		t.Fatalf("artifact failure reason missing from logs: %s", logs.String())
	}
}

func TestProcessorNotifiesNodeAfterTaskIsCancelledLocally(t *testing.T) {
	stage := frozenStage()
	stage.AttemptCount = 1
	store := &stageStoreFake{
		stage: stage,
		task:  domain.Task{TaskID: stage.TaskID, Status: domain.StatusCancelled},
		attempt: sqlite.StageAttempt{
			ID: "attempt-cancel", StageID: stage.ID, AttemptNo: 1, OperationID: "operation-cancel", ExecutionID: "execution-cancel", Status: "running",
		},
		onClaim: func(stage *sqlite.TaskStage, lease string) { stage.LeaseToken = lease },
	}
	client := &nodeClientFake{}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", PollInterval: time.Nanosecond}

	stage.LeaseToken = "lease-cancel"
	if err := processor.poll(context.Background(), stage, store.attempt); err != nil {
		t.Fatal(err)
	}
	if client.cancelledExecution != "execution-cancel" {
		t.Fatalf("cancelled execution=%q", client.cancelledExecution)
	}
	if client.cancelOperation != "stage-cancel-attempt-cancel" {
		t.Fatalf("cancel operation=%q", client.cancelOperation)
	}
}

func TestProcessorReconcilesBarrierBeforeClaimingStage(t *testing.T) {
	store := &stageStoreFake{barrier: &sqlite.NodeDispatchBarrier{
		NodeID: "node-1", TaskID: "cancelled-task", StageID: "cancelled-stage", AttemptID: "cancelled-attempt",
		OperationID: "submit-operation", ExecutionID: "execution-1", CancelOperationID: "stage-cancel-cancelled-attempt", RowVersion: 1,
	}}
	client := &nodeClientFake{executions: []nodeapi.Execution{{ExecutionID: "execution-1", Status: "cancelled"}}}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", Now: func() time.Time { return time.Unix(2_000_000_000, 0) }}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.claimed != 0 || !store.barrierResolved {
		t.Fatalf("claimed=%d barrierResolved=%v", store.claimed, store.barrierResolved)
	}
	if client.cancelledExecution != "execution-1" || client.cancelOperation != "stage-cancel-cancelled-attempt" {
		t.Fatalf("cancelled=%q operation=%q", client.cancelledExecution, client.cancelOperation)
	}
}

func TestProcessorRecoversUnboundBarrierWithOriginalRequest(t *testing.T) {
	request := nodeapi.ExecutionRequest{OperationID: "submit-operation", ExternalTaskID: "cancelled-task", StageID: "cancelled-stage", StageType: "generation", Parameters: json.RawMessage(`{"prompt":"frozen"}`)}
	snapshot, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	store := &stageStoreFake{barrier: &sqlite.NodeDispatchBarrier{
		NodeID: "node-1", TaskID: request.ExternalTaskID, StageID: request.StageID, AttemptID: "cancelled-attempt",
		OperationID: request.OperationID, CancelOperationID: "stage-cancel-cancelled-attempt", RequestSnapshotJSON: string(snapshot), RowVersion: 1,
	}}
	client := &nodeClientFake{
		reference:  nodeapi.ExecutionReference{ExecutionID: "recovered-execution", Status: "running"},
		executions: []nodeapi.Execution{{ExecutionID: "recovered-execution", Status: "cancelled"}},
	}
	processor := Processor{Store: store, Client: client, NodeID: "node-1"}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.created.OperationID != request.OperationID || client.created.ExternalTaskID != request.ExternalTaskID || store.createdAttempts != 0 || store.claimed != 0 {
		t.Fatalf("created=%+v createdAttempts=%d claimed=%d", client.created, store.createdAttempts, store.claimed)
	}
	if !store.barrierResolved {
		t.Fatal("barrier was not resolved")
	}
}

func TestProcessorKeepsBarrierWhenCancelFailsAndExecutionIsRunning(t *testing.T) {
	store := &stageStoreFake{barrier: &sqlite.NodeDispatchBarrier{
		NodeID: "node-1", AttemptID: "attempt-1", ExecutionID: "execution-1", CancelOperationID: "cancel-1", RowVersion: 1,
	}}
	client := &nodeClientFake{
		cancelErr:  &nodeapi.HTTPError{StatusCode: 503, Code: "unavailable"},
		executions: []nodeapi.Execution{{ExecutionID: "execution-1", Status: "running"}},
	}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", Now: func() time.Time { return time.Unix(2_000_000_000, 0) }}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.barrierResolved || store.barrierDeferred != "node_cancel_unavailable" || store.claimed != 0 {
		t.Fatalf("resolved=%v deferred=%q claimed=%d", store.barrierResolved, store.barrierDeferred, store.claimed)
	}
}

func TestProcessorOnlyResolvesMissingExecutionWhenNodeQueueIsExplicitlyIdle(t *testing.T) {
	for _, test := range []struct {
		name         string
		healthStatus string
		running      *int
		pending      *int
		resolved     bool
	}{
		{name: "busy", healthStatus: "healthy", running: intPointer(1), pending: intPointer(0)},
		{name: "idle", healthStatus: "healthy", running: intPointer(0), pending: intPointer(0), resolved: true},
		{name: "missing_count", healthStatus: "healthy", running: nil, pending: intPointer(0)},
		{name: "unhealthy", healthStatus: "unhealthy", running: intPointer(0), pending: intPointer(0)},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &stageStoreFake{barrier: &sqlite.NodeDispatchBarrier{NodeID: "node-1", AttemptID: "attempt-1", ExecutionID: "missing", CancelOperationID: "cancel-1", RowVersion: 1}}
			client := &nodeClientFake{
				getErrors: []error{&nodeapi.HTTPError{StatusCode: 404, Code: "execution_not_found"}},
				health:    nodeapi.Health{Status: test.healthStatus, Runtime: &nodeapi.HealthRuntime{QueueRunning: test.running, QueuePending: test.pending}},
			}
			processor := Processor{Store: store, Client: client, NodeID: "node-1"}
			if err := processor.ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			if store.barrierResolved != test.resolved {
				t.Fatalf("resolved=%v, want %v; deferred=%q", store.barrierResolved, test.resolved, store.barrierDeferred)
			}
		})
	}
}

func intPointer(value int) *int { return &value }

func TestProcessorTurnsMissingQueueIntoSchedulerEmpty(t *testing.T) {
	processor := Processor{Store: &stageStoreFake{claimErr: sqlite.ErrNoClaimableStage}, Client: &nodeClientFake{}, NodeID: "node-1"}
	if err := processor.ProcessOne(context.Background()); err == nil || err.Error() != "队列为空" {
		t.Fatalf("error=%v", err)
	}
}

func TestProcessorRejectsGenerationArtifactThatViolatesFrozenMedia(t *testing.T) {
	store := &stageStoreFake{stage: frozenStage(), onClaim: func(stage *sqlite.TaskStage, lease string) { stage.LeaseToken = lease }}
	client := &nodeClientFake{
		reference:  nodeapi.ExecutionReference{ExecutionID: "exe-invalid", Status: "accepted"},
		executions: []nodeapi.Execution{{ExecutionID: "exe-invalid", Status: "succeeded", ResultArtifactID: "artifact-invalid"}},
		artifact:   nodeArtifact("artifact-invalid"),
	}
	client.artifact.MediaManifest = json.RawMessage(`{"video":{"width":640,"height":480,"start_seconds":0,"duration_seconds":5,"avg_frame_rate":"24/1","frame_count":120,"pts_monotonic":true},"audio":{"present":false}}`)
	processor := Processor{Store: store, Client: client, NodeID: "node-1", PollInterval: time.Nanosecond}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.completedArtifact != "" || store.failedCode != "node_artifact_invalid" {
		t.Fatalf("completed=%q failedCode=%q", store.completedArtifact, store.failedCode)
	}
}

func TestProcessorPreservesInputMaterializationFailure(t *testing.T) {
	stage := frozenStage()
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(stage.ConfigSnapshotJSON), &snapshot); err != nil {
		t.Fatal(err)
	}
	parameters := snapshot["parameters"].(map[string]any)
	parameters["scenario"] = "r2va"
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	stage.ConfigSnapshotJSON = string(encoded)
	payload := base64.StdEncoding.EncodeToString([]byte("image"))
	store := &stageStoreFake{
		stage:   stage,
		task:    domain.Task{TaskID: stage.TaskID, RequestJSON: `{"content":[{"type":"image_url","role":"reference_image","image_url":{"url":"data:image/jpeg;base64,` + payload + `"}}]}`},
		onClaim: func(stage *sqlite.TaskStage, lease string) { stage.LeaseToken = lease },
	}
	client := &nodeClientFake{importErr: errors.New("节点磁盘空间不足")}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", Inputs: &InputMaterializer{Store: store}}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.failedCode != "input_materialization_failed" || store.failedMessage != "输入素材导入失败：节点磁盘空间不足" {
		t.Fatalf("failed code=%q message=%q", store.failedCode, store.failedMessage)
	}
}

func TestProcessorLogsInputMaterializationFailureWithQueueMilestones(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)

	stage := frozenStage()
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(stage.ConfigSnapshotJSON), &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot["parameters"].(map[string]any)["scenario"] = "r2va"
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	stage.ConfigSnapshotJSON = string(encoded)
	payload := base64.StdEncoding.EncodeToString([]byte("private-image-content"))
	store := &stageStoreFake{
		stage:   stage,
		task:    domain.Task{TaskID: stage.TaskID, RequestJSON: `{"content":[{"type":"image_url","role":"reference_image","image_url":{"url":"data:image/jpeg;base64,` + payload + `"}}]}`},
		onClaim: func(stage *sqlite.TaskStage, lease string) { stage.LeaseToken = lease },
	}
	processor := Processor{
		Store: store, Client: &nodeClientFake{importErr: errors.New("节点磁盘空间不足")},
		NodeID: "node-1", Inputs: &InputMaterializer{Store: store},
	}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	output := logs.String()
	for _, expected := range []string{
		"任务阶段已认领", "输入素材处理开始", "输入素材 Base64 解码完成", "输入素材节点导入失败",
		`"task_id":"task-1"`, `"stage_id":"stage-1"`, `"node_id":"node-1"`,
		`"error_code":"input_materialization_failed"`, `"error_reason":"输入素材导入失败：节点磁盘空间不足"`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("logs missing %q: %s", expected, output)
		}
	}
	if strings.Contains(output, payload) || strings.Contains(output, "private-image-content") {
		t.Fatalf("logs leaked input media: %s", output)
	}
}

func TestProcessorDoesNotOverwriteCancellationWithMaterializationFailure(t *testing.T) {
	stage := frozenStage()
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(stage.ConfigSnapshotJSON), &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot["parameters"].(map[string]any)["scenario"] = "r2va"
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	stage.ConfigSnapshotJSON = string(encoded)
	payload := base64.StdEncoding.EncodeToString([]byte("image"))
	store := &stageStoreFake{
		stage:   stage,
		task:    domain.Task{TaskID: stage.TaskID, Status: domain.StatusRunning, RequestJSON: `{"content":[{"type":"image_url","role":"reference_image","image_url":{"url":"data:image/jpeg;base64,` + payload + `"}}]}`},
		onClaim: func(stage *sqlite.TaskStage, lease string) { stage.LeaseToken = lease },
	}
	client := &nodeClientFake{importErr: errors.New("upload interrupted")}
	client.onImport = func() { store.task.Status = domain.StatusCancelled }
	processor := Processor{Store: store, Client: client, NodeID: "node-1", Inputs: &InputMaterializer{Store: store}}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.failedCode != "" {
		t.Fatalf("cancellation overwritten by failure %q", store.failedCode)
	}
}

func TestMaterializationErrorMessageRedactsSourceURL(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://media.example/input.png?token=secret", Err: errors.New("i/o timeout")}
	message := materializationErrorMessage(err)
	if strings.Contains(message, "secret") || message != "输入素材下载失败：i/o timeout" {
		t.Fatalf("message = %q", message)
	}
}

func TestMaterializationErrorMessageDistinguishesNodeImportFromDownload(t *testing.T) {
	err := stagedInputError{phase: "import", err: &url.Error{Op: "Post", URL: "http://private-node:8201/internal/v1/artifacts/import", Err: errors.New("i/o timeout")}}
	message := materializationErrorMessage(err)
	if strings.Contains(message, "private-node") || message != "输入素材导入失败：i/o timeout" {
		t.Fatalf("message = %q", message)
	}
}

type stagedInputError struct {
	phase string
	err   error
}

func (e stagedInputError) Error() string                     { return e.err.Error() }
func (e stagedInputError) Unwrap() error                     { return e.err }
func (e stagedInputError) InputMaterializationPhase() string { return e.phase }

func frozenStage() sqlite.TaskStage {
	parameters, _ := json.Marshal(map[string]any{
		"scenario": "t2va", "prompt": "test", "width": 832, "height": 480, "duration": 5,
		"steps": 8, "seed": 42, "model_mode": "high_quality", "sage_attention": "auto",
		"easycache_enabled": true, "te_speed_enabled": false, "loras": []any{}, "ref_image_size": "match",
		"fl2va_model": "__follow_model_mode__", "ref2va_model": "__follow_model_mode__",
	})
	snapshot, _ := json.Marshal(map[string]any{
		"stage_type": "generation", "parameters": json.RawMessage(parameters),
		"expected_media": map[string]any{"preserve_timeline": true, "preserve_audio": true},
	})
	return sqlite.TaskStage{ID: "stage-1", TaskID: "task-1", StageType: "generation", MaxAttempts: 3, ConfigSnapshotJSON: string(snapshot)}
}

func nodeArtifact(id string) nodeapi.Artifact {
	return nodeapi.Artifact{
		ArtifactID: id, Kind: "video", SizeBytes: 123,
		SHA256:        "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		MediaManifest: json.RawMessage(`{"video":{"width":832,"height":480,"start_seconds":0,"duration_seconds":5,"avg_frame_rate":"24/1","frame_count":120,"pts_monotonic":true},"audio":{"present":false}}`),
		State:         "active",
	}
}

type stageStoreFake struct {
	stage             sqlite.TaskStage
	task              domain.Task
	attempt           sqlite.StageAttempt
	claimErr          error
	onClaim           func(*sqlite.TaskStage, string)
	createdAttempts   int
	renewed           int
	completedArtifact string
	failedCode        string
	failedMessage     string
	barrier           *sqlite.NodeDispatchBarrier
	barrierResolved   bool
	barrierDeferred   string
	claimed           int
}

func (s *stageStoreFake) GetNodeDispatchBarrier(context.Context, string) (sqlite.NodeDispatchBarrier, error) {
	if s.barrier == nil {
		return sqlite.NodeDispatchBarrier{}, sqlite.ErrNodeDispatchBarrierNotFound
	}
	return *s.barrier, nil
}
func (s *stageStoreFake) BindBarrierExecution(_ context.Context, _ string, rowVersion int64, executionID string) error {
	if s.barrier == nil || s.barrier.RowVersion != rowVersion {
		return sqlite.ErrNodeDispatchBarrierConflict
	}
	s.barrier.ExecutionID = executionID
	s.barrier.RowVersion++
	return nil
}
func (s *stageStoreFake) DeferNodeDispatchBarrier(_ context.Context, _ string, rowVersion int64, code string, _ time.Time) error {
	if s.barrier == nil || s.barrier.RowVersion != rowVersion {
		return sqlite.ErrNodeDispatchBarrierConflict
	}
	s.barrierDeferred = code
	s.barrier.RowVersion++
	return nil
}
func (s *stageStoreFake) ResolveNodeDispatchBarrier(_ context.Context, _ string, rowVersion int64) error {
	if s.barrier == nil || s.barrier.RowVersion != rowVersion {
		return sqlite.ErrNodeDispatchBarrierConflict
	}
	s.barrierResolved = true
	s.barrier = nil
	return nil
}
func (s *stageStoreFake) HasNodeDispatchBarrier(context.Context, string) (bool, error) {
	return s.barrier != nil, nil
}

func (s *stageStoreFake) GetActiveArtifactLocation(context.Context, string, string) (sqlite.ArtifactLocation, error) {
	return sqlite.ArtifactLocation{}, sqlite.ErrArtifactNotFound
}
func (s *stageStoreFake) GetPrimaryArtifactLocation(context.Context, string) (sqlite.ArtifactLocation, error) {
	return sqlite.ArtifactLocation{}, sqlite.ErrArtifactNotFound
}
func (s *stageStoreFake) GetTaskForExecution(context.Context, string) (domain.Task, error) {
	if s.task.TaskID != "" {
		return s.task, nil
	}
	return domain.Task{TaskID: s.stage.TaskID, Status: domain.StatusRunning}, nil
}
func (s *stageStoreFake) GetInputSpoolFile(context.Context, string, string) (domain.InputSpoolFile, error) {
	return domain.InputSpoolFile{}, domain.ErrTaskNotFound
}
func (s *stageStoreFake) RegisterInputArtifact(context.Context, string, string, string, string, string, int64, string, string) error {
	return nil
}

func (s *stageStoreFake) ClaimStage(_ context.Context, _ string, lease string, _ time.Duration) (sqlite.TaskStage, error) {
	s.claimed++
	if s.claimErr != nil {
		return sqlite.TaskStage{}, s.claimErr
	}
	stage := s.stage
	if s.onClaim != nil {
		s.onClaim(&stage, lease)
	}
	return stage, nil
}
func (s *stageStoreFake) RenewStageLease(context.Context, string, string, time.Duration) error {
	s.renewed++
	return nil
}
func (s *stageStoreFake) CreateStageAttempt(_ context.Context, attempt sqlite.StageAttempt) error {
	s.createdAttempts++
	s.attempt = attempt
	return nil
}
func (s *stageStoreFake) BindStageExecution(_ context.Context, _, _, attemptID, executionID string) error {
	if s.attempt.ID != attemptID {
		return errors.New("unexpected attempt")
	}
	s.attempt.ExecutionID, s.attempt.Status = executionID, "running"
	return nil
}
func (s *stageStoreFake) GetRunningStageAttempt(context.Context, string, string) (sqlite.StageAttempt, error) {
	if s.attempt.ID == "" {
		return sqlite.StageAttempt{}, sqlite.ErrNoClaimableStage
	}
	return s.attempt, nil
}
func (s *stageStoreFake) CompleteStage(_ context.Context, _, _, _ string, artifactID string) error {
	s.completedArtifact = artifactID
	return nil
}

func (s *stageStoreFake) CompleteStageWithOutput(_ context.Context, _, _, _, _ string, _ string, _ string, _ int64, _, _ string) (string, error) {
	s.completedArtifact = "logical-artifact-1"
	return s.completedArtifact, nil
}

func (s *stageStoreFake) FailStage(_ context.Context, _ string, _ string, _ string, code string, message string, _ time.Time, _ bool) error {
	s.failedCode = code
	s.failedMessage = message
	return nil
}

type nodeClientFake struct {
	reference          nodeapi.ExecutionReference
	executions         []nodeapi.Execution
	getErrors          []error
	created            nodeapi.ExecutionRequest
	onGet              func()
	artifact           nodeapi.Artifact
	artifactErr        error
	importErr          error
	onImport           func()
	cancelledExecution string
	cancelOperation    string
	cancelErr          error
	createErr          error
	health             nodeapi.Health
	healthErr          error
}

func (c *nodeClientFake) CreateExecution(_ context.Context, _ string, request nodeapi.ExecutionRequest) (nodeapi.ExecutionReference, error) {
	c.created = request
	if c.createErr != nil {
		return nodeapi.ExecutionReference{}, c.createErr
	}
	return c.reference, nil
}
func (c *nodeClientFake) GetExecution(context.Context, string, string) (nodeapi.Execution, error) {
	if len(c.getErrors) > 0 {
		err := c.getErrors[0]
		c.getErrors = c.getErrors[1:]
		return nodeapi.Execution{}, err
	}
	if len(c.executions) == 0 {
		return nodeapi.Execution{}, errors.New("no execution")
	}
	result := c.executions[0]
	c.executions = c.executions[1:]
	if c.onGet != nil {
		c.onGet()
	}
	return result, nil
}
func (c *nodeClientFake) GetArtifact(context.Context, string, string) (nodeapi.Artifact, error) {
	if c.artifactErr != nil {
		return nodeapi.Artifact{}, c.artifactErr
	}
	return c.artifact, nil
}
func (c *nodeClientFake) ImportArtifact(context.Context, string, nodeapi.ImportArtifactRequest) (nodeapi.Artifact, error) {
	if c.onImport != nil {
		c.onImport()
	}
	if c.importErr != nil {
		return nodeapi.Artifact{}, c.importErr
	}
	return nodeapi.Artifact{}, errors.New("unexpected import")
}
func (c *nodeClientFake) CancelExecution(_ context.Context, _, executionID, operationID string) (nodeapi.ExecutionReference, error) {
	c.cancelledExecution = executionID
	c.cancelOperation = operationID
	if c.cancelErr != nil {
		return nodeapi.ExecutionReference{}, c.cancelErr
	}
	return nodeapi.ExecutionReference{ExecutionID: executionID, Status: "cancelling"}, nil
}
func (c *nodeClientFake) Health(context.Context, string) (nodeapi.Health, error) {
	return c.health, c.healthErr
}
