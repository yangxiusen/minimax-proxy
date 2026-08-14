package registry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/monitor"
	storepkg "minimax-h3-tc/internal/store/sqlite"
	"minimax-h3-tc/internal/upstream/gradio"
	"minimax-h3-tc/internal/upstream/nodeapi"
)

func TestNodeRuntimeFactoryMonitorsDisabledNodeWithoutClaiming(t *testing.T) {
	ctx := context.Background()
	store, err := storepkg.Open(ctx, filepath.Join(t.TempDir(), "runtime.db"), storepkg.Options{
		ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10, Retention: time.Hour, IdempotencyTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Create(ctx, domain.NewTask{
		TaskID: "queued", APIKeyID: "owner", Model: "MiniMax-H3", Scenario: "t2va",
		RequestJSON: `{"model":"MiniMax-H3"}`, RequestHash: "hash", Resolution: "480P", Duration: 5, Ratio: "16:9",
	}, "", nil); err != nil {
		t.Fatal(err)
	}
	client := &runtimeClientFake{called: make(chan struct{}, 1)}
	cache := monitor.NewCache(nil)
	factory := NodeRuntimeFactory{
		Store: store, Cache: cache, MonitorInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ClientFactory: func(config.UpstreamConfig) RuntimeClient { return client },
	}
	node := domain.ModelNode{ModelNodeInput: domain.ModelNodeInput{
		ID: "gpu-disabled", BaseURL: "http://127.0.0.1:7860", JobsBaseURL: "http://127.0.0.1:8188",
		PublicBaseURL: "https://video.example.com", HealthPath: "/", SubmitAPIName: "submit", CheckAPIName: "check",
		PollInterval: time.Second, RequestTimeout: time.Second, Enabled: false,
	}, Version: 1}
	runtime, err := factory.Start(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()
	select {
	case <-client.called:
	case <-time.After(time.Second):
		t.Fatal("disabled node was not monitored")
	}
	waitFor(t, func() bool {
		snapshot, ok := cache.Get(node.ID)
		return ok && snapshot.Disabled && !snapshot.Applying && snapshot.Health == monitor.HealthHealthy
	})
	queued, err := store.Get(ctx, "owner", "queued")
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != domain.StatusQueuedOpen {
		t.Fatalf("disabled node claimed task: status=%s", queued.Status)
	}
}

func TestDisabledNodeDrainsCancellingTaskWithoutClaimingNextTask(t *testing.T) {
	ctx := context.Background()
	store, err := storepkg.Open(ctx, filepath.Join(t.TempDir(), "drain.db"), storepkg.Options{
		ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10, Retention: time.Hour, IdempotencyTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	input := domain.ModelNodeInput{
		ID: "gpu-drain", BaseURL: "http://127.0.0.1:7860", JobsBaseURL: "http://127.0.0.1:8188",
		PublicBaseURL: "https://video.example.com", HealthPath: "/", SubmitAPIName: "submit", CheckAPIName: "check",
		PollInterval: time.Second, RequestTimeout: time.Second, Enabled: true,
	}
	node, err := store.CreateModelNode(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, taskID := range []string{"active", "next"} {
		if _, err := store.Create(ctx, domain.NewTask{
			TaskID: taskID, APIKeyID: "owner", Model: "MiniMax-H3", Scenario: "t2va",
			RequestJSON: `{"model":"MiniMax-H3"}`, RequestHash: taskID, Resolution: "480P", Duration: 5, Ratio: "16:9",
		}, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ClaimNext(ctx, node.ID, node.Version); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestAdminCancel(ctx, "active"); err != nil {
		t.Fatal(err)
	}
	input.Enabled = false
	disabled, err := store.UpdateModelNode(ctx, node.ID, node.Version, input)
	if err != nil {
		t.Fatal(err)
	}
	client := &runtimeClientFake{called: make(chan struct{}, 1)}
	cache := monitor.NewCache(nil)
	factory := NodeRuntimeFactory{
		Store: store, Cache: cache, MonitorInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ClientFactory: func(config.UpstreamConfig) RuntimeClient { return client },
	}
	runtime, err := factory.Start(ctx, disabled)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()
	waitFor(t, func() bool {
		task, err := store.Get(ctx, "owner", "active")
		return err == nil && task.Status == domain.StatusCancelled
	})
	next, err := store.Get(ctx, "owner", "next")
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != domain.StatusQueuedOpen {
		t.Fatalf("disabled node claimed next task: status=%s", next.Status)
	}
}

func TestCachedSchedulableRejectsPrivateWorkAndSchedulingBlock(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	queue := 1
	cache := monitor.NewCache([]monitor.NodeSnapshot{{
		ID: "gpu-1", Health: monitor.HealthHealthy, Runtime: monitor.RuntimeRunning, PrivateQueue: &queue, CheckedAt: now,
	}})
	if err := cachedSchedulable(cache, "gpu-1", now, 10*time.Second); err == nil {
		t.Fatal("running private instance was considered schedulable")
	}
	cache.Update("gpu-1", func(node *monitor.NodeSnapshot) {
		node.Runtime = monitor.RuntimeIdle
		zero := 0
		node.PrivateQueue = &zero
	})
	if err := cachedSchedulable(cache, "gpu-1", now, 10*time.Second); err != nil {
		t.Fatalf("idle private instance was not schedulable: %v", err)
	}
	cache.Update("gpu-1", func(node *monitor.NodeSnapshot) { node.SchedulingBlocked = true })
	if err := cachedSchedulable(cache, "gpu-1", now, 10*time.Second); err == nil {
		t.Fatal("explicitly blocked node was considered schedulable")
	}
}

func TestNodeAPISnapshotRestoresCancellationBarrierAsBlockedWork(t *testing.T) {
	ctx := context.Background()
	store, err := storepkg.Open(ctx, filepath.Join(t.TempDir(), "barrier-runtime.db"), storepkg.Options{
		ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10, Retention: time.Hour, IdempotencyTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	nodeInput := domain.ModelNodeInput{
		ID: "gpu-barrier", ServiceURL: "http://127.0.0.1:7860", ProtocolVersion: nodeapi.ProtocolVersion,
		APIKeyNonce: []byte("nonce"), APIKeyCiphertext: []byte("ciphertext"), APIKeyFingerprint: "fingerprint",
		PollInterval: time.Second, RequestTimeout: time.Second, Enabled: true,
	}
	node, err := store.CreateModelNode(ctx, nodeInput)
	if err != nil {
		t.Fatal(err)
	}
	input := domain.NewTask{
		TaskID: "barrier-task", APIKeyID: "owner", Model: "MiniMax-H3", Scenario: "t2va",
		RequestJSON: `{"model":"MiniMax-H3"}`, RequestHash: "barrier-task", Resolution: "480P", Duration: 5, Ratio: "16:9",
		Stages: []domain.NewTaskStage{{ID: "barrier-stage", StageType: "generation", StageOrder: 10, MaxAttempts: 1, ConfigSnapshotJSON: `{}`}},
	}
	if _, err := store.Create(ctx, input, "", nil); err != nil {
		t.Fatal(err)
	}
	stage, err := store.ClaimStage(ctx, node.ID, "barrier-lease", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	attempt := storepkg.StageAttempt{
		ID: "barrier-attempt", StageID: stage.ID, AttemptNo: 1, OperationID: "barrier-operation", NodeID: node.ID,
		LeaseToken: stage.LeaseToken, Status: "dispatching", RequestSnapshotJSON: `{"operation_id":"barrier-operation"}`,
	}
	if err := store.CreateStageAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := store.BindStageExecution(ctx, stage.ID, stage.LeaseToken, attempt.ID, "barrier-execution"); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestAdminCancel(ctx, input.TaskID); err != nil {
		t.Fatal(err)
	}

	cache := monitor.NewCache(nil)
	factory := NodeRuntimeFactory{Store: store, Cache: cache}
	_, upstream, err := config.NormalizeModelNode(nodeInput)
	if err != nil {
		t.Fatal(err)
	}
	if err := factory.initializeSnapshot(ctx, node, nodeInput, upstream); err != nil {
		t.Fatal(err)
	}
	snapshot, ok := cache.Get(node.ID)
	if !ok || !snapshot.SchedulingBlocked || snapshot.Runtime != monitor.RuntimeRunning || snapshot.LastError == nil || snapshot.LastError.Code != "node_cancel_reconciling" {
		t.Fatalf("snapshot = %+v, lastError=%+v, ok=%v", snapshot, snapshot.LastError, ok)
	}
}

func TestNodeAPIRuntimeUsesSingleServiceEndpointAndBearerSecret(t *testing.T) {
	ctx := context.Background()
	store, err := storepkg.Open(ctx, filepath.Join(t.TempDir(), "node-api.db"), storepkg.Options{
		ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10, Retention: time.Hour, IdempotencyTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	nodeInput := domain.ModelNodeInput{
		ID: "gpu-api", ServiceURL: "http://127.0.0.1:7860", ProtocolVersion: nodeapi.ProtocolVersion,
		APIKeyNonce: []byte("nonce"), APIKeyCiphertext: []byte("ciphertext"), APIKeyFingerprint: "fingerprint",
		PollInterval: time.Second, RequestTimeout: time.Second, Enabled: true,
	}
	node, err := store.CreateModelNode(ctx, nodeInput)
	if err != nil {
		t.Fatal(err)
	}
	input := domain.NewTask{
		TaskID: "active-h3", APIKeyID: "owner", Model: "MiniMax-H3", Scenario: "t2va",
		RequestJSON: `{"model":"MiniMax-H3"}`, RequestHash: "active-h3", Resolution: "480P", Duration: 5, Ratio: "16:9",
		Stages: []domain.NewTaskStage{{ID: "active-stage", StageType: "generation", StageOrder: 10, MaxAttempts: 2, ConfigSnapshotJSON: `{}`}},
	}
	if _, err := store.Create(ctx, input, "", nil); err != nil {
		t.Fatal(err)
	}
	stage, err := store.ClaimStage(ctx, node.ID, "lease", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	attempt := storepkg.StageAttempt{ID: "active-attempt", StageID: stage.ID, AttemptNo: 1, OperationID: "active-operation", NodeID: node.ID, LeaseToken: stage.LeaseToken, Status: "dispatching"}
	if err := store.CreateStageAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := store.BindStageExecution(ctx, stage.ID, stage.LeaseToken, attempt.ID, "active-execution"); err != nil {
		t.Fatal(err)
	}
	queueRunning, queuePending := 0, 2
	memoryTotal, memoryFree := int64(1000), int64(250)
	vramTotal, vramFree := int64(800), int64(200)
	cpu, gpu := 12.5, 42.0
	client := &nodeAPIClientFake{
		called: make(chan string, 2),
		health: nodeapi.Health{Status: "healthy", ProtocolVersion: nodeapi.ProtocolVersion, Runtime: &nodeapi.HealthRuntime{
			QueueRunning: &queueRunning, QueuePending: &queuePending,
			MemoryTotalBytes: &memoryTotal, MemoryFreeBytes: &memoryFree,
			VRAMTotalBytes: &vramTotal, VRAMFreeBytes: &vramFree,
			CPUPercent: &cpu, GPUPercent: &gpu,
		}},
	}
	cache := monitor.NewCache(nil)
	factory := NodeRuntimeFactory{
		Store:           store,
		Cache:           cache,
		MonitorInterval: time.Hour,
		NodeSecrets:     nodeSecretFake{},
		NodeAPIClientFactory: func(serviceURL *url.URL, apiKey string, _ *http.Client, _ int64) NodeAPIClient {
			if serviceURL.String() != "http://127.0.0.1:7860" || apiKey != "Abcdefghijklmnopqrstuvwx12345678" {
				t.Fatalf("node API connection=%s key=%q", serviceURL, apiKey)
			}
			return client
		},
	}
	runtime, err := factory.Start(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()
	for range 2 {
		select {
		case <-client.called:
		case <-time.After(time.Second):
			t.Fatal("node API was not probed")
		}
	}
	waitFor(t, func() bool {
		snapshot, ok := cache.Get(node.ID)
		return ok && snapshot.Health == monitor.HealthHealthy && snapshot.Runtime == monitor.RuntimeRunning && !snapshot.Applying &&
			snapshot.PrivateQueue != nil && *snapshot.PrivateQueue == 2 &&
			snapshot.MemoryPercent != nil && *snapshot.MemoryPercent == 75 &&
			snapshot.VRAMPercent != nil && *snapshot.VRAMPercent == 75 &&
			snapshot.CPUPercent != nil && *snapshot.CPUPercent == cpu &&
			snapshot.GPUPercent != nil && *snapshot.GPUPercent == gpu &&
			snapshot.CurrentTask != nil && snapshot.CurrentTask.ID == input.TaskID && snapshot.CurrentTask.Status == "running"
	})
	if err := store.CompleteStage(ctx, stage.ID, stage.LeaseToken, attempt.ID, ""); err != nil {
		t.Fatal(err)
	}
	runtime.Wake()
	for range 2 {
		select {
		case <-client.called:
		case <-time.After(time.Second):
			t.Fatal("node API was not reprobed")
		}
	}
	waitFor(t, func() bool {
		snapshot, ok := cache.Get(node.ID)
		return ok && snapshot.CurrentTask == nil && snapshot.LatestFinishedTask != nil &&
			snapshot.LatestFinishedTask.ID == input.TaskID && snapshot.LatestFinishedTask.Status == "succeeded"
	})
}

type runtimeClientFake struct {
	called chan struct{}
}

type nodeSecretFake struct{}

func (nodeSecretFake) Open(_ []byte, _ []byte) (string, error) {
	return "Abcdefghijklmnopqrstuvwx12345678", nil
}

type nodeAPIClientFake struct {
	called chan string
	health nodeapi.Health
}

func (f *nodeAPIClientFake) Health(_ context.Context, requestID string) (nodeapi.Health, error) {
	f.called <- requestID
	if f.health.Status != "" {
		return f.health, nil
	}
	return nodeapi.Health{Status: "healthy", ProtocolVersion: nodeapi.ProtocolVersion}, nil
}

func (f *nodeAPIClientFake) Capabilities(_ context.Context, requestID string) (nodeapi.Capabilities, error) {
	f.called <- requestID
	return nodeapi.Capabilities{ProtocolVersion: nodeapi.ProtocolVersion}, nil
}

func (*nodeAPIClientFake) CreateExecution(context.Context, string, nodeapi.ExecutionRequest) (nodeapi.ExecutionReference, error) {
	return nodeapi.ExecutionReference{}, errors.New("unexpected execution create")
}

func (*nodeAPIClientFake) GetExecution(context.Context, string, string) (nodeapi.Execution, error) {
	return nodeapi.Execution{}, errors.New("unexpected execution read")
}

func (*nodeAPIClientFake) GetArtifact(context.Context, string, string) (nodeapi.Artifact, error) {
	return nodeapi.Artifact{}, errors.New("unexpected artifact read")
}
func (*nodeAPIClientFake) ImportArtifact(context.Context, string, nodeapi.ImportArtifactRequest) (nodeapi.Artifact, error) {
	return nodeapi.Artifact{}, errors.New("unexpected import")
}

func (*nodeAPIClientFake) CancelExecution(context.Context, string, string, string) (nodeapi.ExecutionReference, error) {
	return nodeapi.ExecutionReference{}, nil
}

func (*runtimeClientFake) Healthy(context.Context, string) error { return nil }
func (*runtimeClientFake) ListJobs(context.Context) ([]gradio.Job, error) {
	return nil, nil
}
func (c *runtimeClientFake) Call(context.Context, string, []any) ([]any, error) {
	select {
	case c.called <- struct{}{}:
	default:
	}
	zero := 0
	return []any{nil, "idle", zero, ""}, nil
}
