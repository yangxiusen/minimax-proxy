package monitor

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestNewCacheInitializesUnknownNodes(t *testing.T) {
	cache := NewCache([]NodeSnapshot{{ID: "gpu-1", Address: "127.0.0.1:7860"}})

	got, ok := cache.Get("gpu-1")
	if !ok {
		t.Fatal("Get() did not return configured node")
	}
	if got.Health != HealthUnknown || got.Runtime != RuntimeUnknown {
		t.Fatalf("initial status = health %q, runtime %q", got.Health, got.Runtime)
	}
	if cache.HealthyCount() != 0 || cache.Available() {
		t.Fatalf("unknown node must not be available: count=%d available=%v", cache.HealthyCount(), cache.Available())
	}
}

func TestCacheSetUpdatesSnapshotAndAvailability(t *testing.T) {
	cache := NewCache(nil)
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	queue := 2
	cache.Set(NodeSnapshot{
		ID: "gpu-1", Health: HealthHealthy, Runtime: RuntimeRunning,
		PrivateQueue: &queue, UpdatedAt: now,
		CurrentTask: &CurrentTaskSnapshot{ID: "task-1", Status: "running", StartedAt: now.Add(-time.Minute)},
	})

	got, ok := cache.Get("gpu-1")
	if !ok || got.Health != HealthHealthy || got.Runtime != RuntimeRunning {
		t.Fatalf("Get() = %+v, %v", got, ok)
	}
	if cache.HealthyCount() != 1 || !cache.Available() {
		t.Fatalf("healthy node must be available: count=%d available=%v", cache.HealthyCount(), cache.Available())
	}
}

func TestCacheSetDefaultsMissingStatusesToUnknown(t *testing.T) {
	cache := NewCache(nil)
	cache.Set(NodeSnapshot{ID: "gpu-1"})

	got, _ := cache.Get("gpu-1")
	if got.Health != HealthUnknown || got.Runtime != RuntimeUnknown {
		t.Fatalf("status = health %q, runtime %q", got.Health, got.Runtime)
	}
}

func TestCacheReadsAndWritesUseIndependentCopies(t *testing.T) {
	queue := 3
	cpu := 42.5
	cache := NewCache([]NodeSnapshot{{
		ID: "gpu-1", Health: HealthHealthy, PrivateQueue: &queue, CPUPercent: &cpu,
		CurrentTask:        &CurrentTaskSnapshot{ID: "task-1"},
		LatestFinishedTask: &FinishedTaskSnapshot{ID: "task-0"},
		LastError:          &ErrorSnapshot{Code: "upstream_unhealthy", Summary: "不可信摘要"},
	}})

	queue = 99
	first, _ := cache.Get("gpu-1")
	*first.PrivateQueue = 88
	*first.CPUPercent = 100
	first.CurrentTask.ID = "changed"
	first.LatestFinishedTask.ID = "changed"
	first.LastError.Code = "changed"
	listed := cache.List()
	listed[0].ID = "changed"

	got, _ := cache.Get("gpu-1")
	if *got.PrivateQueue != 3 || *got.CPUPercent != 42.5 || got.CurrentTask.ID != "task-1" || got.LatestFinishedTask.ID != "task-0" || got.LastError.Code != "upstream_unhealthy" || got.ID != "gpu-1" {
		t.Fatalf("cached snapshot was mutated through an external copy: %+v", got)
	}
}

func TestNewCacheSanitizesLastError(t *testing.T) {
	tests := []struct {
		code    string
		summary string
	}{
		{code: "upstream_unhealthy", summary: "私有服务连接失败"},
		{code: "upstream_poll_error", summary: "私有服务状态查询失败"},
		{code: "upstream_protocol_error", summary: "私有服务状态响应异常"},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			cache := NewCache([]NodeSnapshot{{
				ID: "gpu-1",
				LastError: &ErrorSnapshot{
					Code:    test.code,
					Summary: "token=secret\nhttp://10.0.0.1/private",
				},
			}})

			got, _ := cache.Get("gpu-1")
			if got.LastError == nil || got.LastError.Code != test.code || got.LastError.Summary != test.summary {
				t.Fatalf("LastError = %+v", got.LastError)
			}
		})
	}
}

func TestCacheSetSanitizesUnknownLastError(t *testing.T) {
	cache := NewCache(nil)
	cache.Set(NodeSnapshot{
		ID: "gpu-1",
		LastError: &ErrorSnapshot{
			Code:    "raw_backend_error",
			Summary: "Bearer secret-token\nhttp://10.0.0.1/private",
		},
	})

	got, _ := cache.Get("gpu-1")
	if got.LastError == nil || got.LastError.Code != "upstream_error" || got.LastError.Summary != "私有服务异常" {
		t.Fatalf("LastError = %+v", got.LastError)
	}
}

func TestCacheSupportsConcurrentReadersAndWriters(t *testing.T) {
	cache := NewCache(nil)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := 0; iteration < 500; iteration++ {
				id := fmt.Sprintf("gpu-%d", worker%4)
				value := float64(iteration)
				cache.Set(NodeSnapshot{ID: id, Health: HealthHealthy, CPUPercent: &value})
				_, _ = cache.Get(id)
				_ = cache.List()
				_ = cache.HealthyCount()
				_ = cache.Available()
			}
		}()
	}
	wg.Wait()
	if cache.HealthyCount() != 4 {
		t.Fatalf("HealthyCount() = %d, want 4", cache.HealthyCount())
	}
}

func TestCacheUpdateMergesConcurrentFields(t *testing.T) {
	cache := NewCache([]NodeSnapshot{{ID: "gpu-1"}})
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		cache.Update("gpu-1", func(node *NodeSnapshot) {
			queue := 3
			node.PrivateQueue = &queue
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		cache.Update("gpu-1", func(node *NodeSnapshot) {
			cpu := 42.5
			node.CPUPercent = &cpu
		})
	}()
	close(start)
	wg.Wait()

	got, _ := cache.Get("gpu-1")
	if got.PrivateQueue == nil || *got.PrivateQueue != 3 || got.CPUPercent == nil || *got.CPUPercent != 42.5 {
		t.Fatalf("Update() lost a merged field: %+v", got)
	}
}

func TestCacheUpdateNormalizesAndClonesSnapshot(t *testing.T) {
	cache := NewCache(nil)
	queue := 2
	cache.Update("gpu-1", func(node *NodeSnapshot) {
		node.PrivateQueue = &queue
		node.LastError = &ErrorSnapshot{Code: "raw", Summary: "secret"}
	})
	queue = 99

	got, ok := cache.Get("gpu-1")
	if !ok || got.ID != "gpu-1" || got.Health != HealthUnknown || got.Runtime != RuntimeUnknown {
		t.Fatalf("Update() snapshot = %+v, ok = %v", got, ok)
	}
	if *got.PrivateQueue != 2 || got.LastError == nil || got.LastError.Code != "upstream_error" {
		t.Fatalf("Update() did not clone and normalize: %+v", got)
	}
}

func TestCacheAvailableFreshRequiresRecentHealthyCheck(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		node      NodeSnapshot
		maxAge    time.Duration
		available bool
	}{
		{name: "fresh healthy", node: NodeSnapshot{ID: "fresh", Health: HealthHealthy, CheckedAt: now.Add(-4 * time.Second)}, maxAge: 5 * time.Second, available: true},
		{name: "boundary is fresh", node: NodeSnapshot{ID: "boundary", Health: HealthHealthy, CheckedAt: now.Add(-5 * time.Second)}, maxAge: 5 * time.Second, available: true},
		{name: "stale", node: NodeSnapshot{ID: "stale", Health: HealthHealthy, CheckedAt: now.Add(-6 * time.Second)}, maxAge: 5 * time.Second},
		{name: "unchecked", node: NodeSnapshot{ID: "unchecked", Health: HealthHealthy}, maxAge: 5 * time.Second},
		{name: "unhealthy", node: NodeSnapshot{ID: "unhealthy", Health: HealthUnhealthy, CheckedAt: now}, maxAge: 5 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cache := NewCache([]NodeSnapshot{test.node})
			if got := cache.AvailableFresh(now, test.maxAge); got != test.available {
				t.Fatalf("AvailableFresh() = %v, want %v", got, test.available)
			}
		})
	}
}

func TestCacheAvailableFreshAcceptsQueueWhileNodeCancellationIsReconciling(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	cache := NewCache([]NodeSnapshot{{
		ID: "gpu-cancelling", Health: HealthUnhealthy, Runtime: RuntimeRunning,
		SchedulingBlocked: true, CheckedAt: now, LastError: &ErrorSnapshot{Code: "node_cancel_reconciling"},
	}})

	if !cache.AvailableFresh(now, time.Second) {
		t.Fatal("cancellation barrier must preserve queue admission capacity")
	}
}

func TestCacheAvailabilityExcludesDisabledAndApplyingNodesAndSupportsDelete(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	cache := NewCache([]NodeSnapshot{
		{ID: "disabled", Health: HealthHealthy, CheckedAt: now, Disabled: true},
		{ID: "applying", Health: HealthHealthy, CheckedAt: now, Applying: true},
		{ID: "ready", Health: HealthHealthy, CheckedAt: now},
	})
	if !cache.AvailableFresh(now, time.Second) {
		t.Fatal("ready node was not available")
	}
	cache.Delete("ready")
	if cache.AvailableFresh(now, time.Second) {
		t.Fatal("disabled or applying node was considered available")
	}
	if _, ok := cache.Get("ready"); ok {
		t.Fatal("deleted node remained in cache")
	}
}
