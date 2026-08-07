package monitor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
)

func TestCollectorProbesImmediatelyAndMergesObservation(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	cache := NewCache([]NodeSnapshot{{ID: "gpu-1"}})
	client := &collectorClient{result: []any{nil, "idle", "queue: 2", "CPU: 10% 内存: 20% GPU: 30% 显存: 40%"}, called: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	collector := Collector{
		Cache:    cache,
		Nodes:    []CollectorNode{{Upstream: config.UpstreamConfig{ID: "gpu-1", HealthPath: "/health", CheckAPIName: "check"}, Client: client, Gate: &sync.Mutex{}}},
		Interval: time.Hour,
		Now:      func() time.Time { return now },
	}
	done := make(chan struct{})
	go func() { collector.Run(ctx); close(done) }()
	select {
	case <-client.called:
	case <-time.After(time.Second):
		t.Fatal("collector did not probe immediately")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("collector did not stop after cancellation")
	}

	if client.order != "health,call" {
		t.Fatalf("probe order = %q", client.order)
	}
	got, _ := cache.Get("gpu-1")
	if got.Health != HealthHealthy || got.Runtime != RuntimeIdle || !got.CheckedAt.Equal(now) || !got.LastHealthyAt.Equal(now) || got.LastError != nil {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.PrivateQueue == nil || *got.PrivateQueue != 2 || got.VRAMPercent == nil || *got.VRAMPercent != 40 {
		t.Fatalf("observation was not merged: %+v", got)
	}
}

func TestCollectorKeepsCurrentTaskRunningAndSkipsBusyGate(t *testing.T) {
	started := time.Now().UTC()
	cache := NewCache([]NodeSnapshot{{ID: "gpu-1", CurrentTask: &CurrentTaskSnapshot{ID: "task-1", StartedAt: started}}})
	gate := &sync.Mutex{}
	gate.Lock()
	client := &collectorClient{result: []any{nil, "idle"}, called: make(chan struct{}, 1)}
	collector := Collector{Cache: cache, Nodes: []CollectorNode{{Upstream: config.UpstreamConfig{ID: "gpu-1"}, Client: client, Gate: gate}}}
	collector.probe(context.Background(), collector.Nodes[0])
	if client.calls != 0 {
		t.Fatalf("busy gate calls = %d", client.calls)
	}
	gate.Unlock()
	collector.probe(context.Background(), collector.Nodes[0])

	got, _ := cache.Get("gpu-1")
	if got.Runtime != RuntimeRunning || got.CurrentTask == nil || got.CurrentTask.ID != "task-1" {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestCollectorFailureMarksUnhealthyAndPreservesResources(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	cpu := 75.0
	cache := NewCache([]NodeSnapshot{{ID: "gpu-1", Health: HealthHealthy, CPUPercent: &cpu, LastHealthyAt: now.Add(-time.Minute)}})
	client := &collectorClient{healthErr: errors.New("secret upstream address"), called: make(chan struct{}, 1)}
	collector := Collector{Cache: cache, Nodes: []CollectorNode{{Upstream: config.UpstreamConfig{ID: "gpu-1"}, Client: client, Gate: &sync.Mutex{}}}, Now: func() time.Time { return now }}
	collector.probe(context.Background(), collector.Nodes[0])

	got, _ := cache.Get("gpu-1")
	if got.Health != HealthUnhealthy || !got.CheckedAt.Equal(now) || got.CPUPercent == nil || *got.CPUPercent != 75 || got.LastError == nil || got.LastError.Code != "upstream_unhealthy" {
		t.Fatalf("snapshot = %+v", got)
	}
	if client.calls != 0 {
		t.Fatalf("Call() ran after failed Healthy(): %d", client.calls)
	}
}

type collectorClient struct {
	result    []any
	healthErr error
	callErr   error
	called    chan struct{}
	calls     int
	order     string
}

func (c *collectorClient) Healthy(context.Context, string) error {
	if c.order == "" {
		c.order = "health"
	} else {
		c.order += ",health"
	}
	return c.healthErr
}

func (c *collectorClient) Call(context.Context, string, []any) ([]any, error) {
	c.calls++
	c.order += ",call"
	if c.called != nil {
		select {
		case c.called <- struct{}{}:
		default:
		}
	}
	return c.result, c.callErr
}
