package registry

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/monitor"
	storepkg "minimax-h3-tc/internal/store/sqlite"
	"minimax-h3-tc/internal/upstream/gradio"
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

type runtimeClientFake struct {
	called chan struct{}
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
