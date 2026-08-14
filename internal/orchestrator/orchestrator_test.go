package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
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
	attempt           sqlite.StageAttempt
	claimErr          error
	onClaim           func(*sqlite.TaskStage, string)
	createdAttempts   int
	renewed           int
	completedArtifact string
	failedCode        string
}

func (s *stageStoreFake) GetActiveArtifactLocation(context.Context, string, string) (sqlite.ArtifactLocation, error) {
	return sqlite.ArtifactLocation{}, sqlite.ErrArtifactNotFound
}
func (s *stageStoreFake) GetPrimaryArtifactLocation(context.Context, string) (sqlite.ArtifactLocation, error) {
	return sqlite.ArtifactLocation{}, sqlite.ErrArtifactNotFound
}
func (s *stageStoreFake) GetTaskForExecution(context.Context, string) (domain.Task, error) {
	return domain.Task{}, domain.ErrTaskNotFound
}
func (s *stageStoreFake) RegisterInputArtifact(context.Context, string, string, string, string, string, int64, string, string) error {
	return nil
}

func (s *stageStoreFake) ClaimStage(_ context.Context, _ string, lease string, _ time.Duration) (sqlite.TaskStage, error) {
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

func (s *stageStoreFake) FailStage(_ context.Context, _ string, _ string, _ string, code string, _ string, _ time.Time, _ bool) error {
	s.failedCode = code
	return nil
}
func (s *stageStoreFake) RegisterStageOutput(context.Context, string, string, string, string, int64, string, string) (string, error) {
	return "logical-artifact-1", nil
}

type nodeClientFake struct {
	reference  nodeapi.ExecutionReference
	executions []nodeapi.Execution
	getErrors  []error
	created    nodeapi.ExecutionRequest
	onGet      func()
	artifact   nodeapi.Artifact
}

func (c *nodeClientFake) CreateExecution(_ context.Context, _ string, request nodeapi.ExecutionRequest) (nodeapi.ExecutionReference, error) {
	c.created = request
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
	return c.artifact, nil
}
func (c *nodeClientFake) ImportArtifact(context.Context, string, nodeapi.ImportArtifactRequest) (nodeapi.Artifact, error) {
	return nodeapi.Artifact{}, errors.New("unexpected import")
}
