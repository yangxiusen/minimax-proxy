package registry

import (
	"context"
	"sync"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/monitor"
)

func TestRegistryReconcilesCreateReplaceDeleteAndMissedWake(t *testing.T) {
	store := &registryStore{nodes: []domain.ModelNode{{ModelNodeInput: domain.ModelNodeInput{ID: "gpu-1", Enabled: true}, Version: 1}}}
	factory := &recordingFactory{}
	cache := monitor.NewCache(nil)
	registry := New(store, factory.Start, cache, 10*time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { registry.Run(ctx); close(done) }()

	waitFor(t, func() bool { return factory.started("gpu-1", 1) == 1 })
	cache.Set(monitor.NodeSnapshot{ID: "gpu-1"})
	store.set([]domain.ModelNode{{ModelNodeInput: domain.ModelNodeInput{ID: "gpu-1", Enabled: true}, Version: 2}})
	waitFor(t, func() bool { return factory.started("gpu-1", 2) == 1 && factory.stopped("gpu-1", 1) == 1 })
	if factory.started("gpu-1", 1) != 1 {
		t.Fatal("same node version was started more than once")
	}

	store.set(nil)
	waitFor(t, func() bool { return factory.stopped("gpu-1", 2) == 1 })
	if _, ok := cache.Get("gpu-1"); ok {
		t.Fatal("deleted node remained in cache")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("registry did not stop")
	}
}

func TestRegistryStartsDisabledNodesAndWakeReachesWorkers(t *testing.T) {
	store := &registryStore{nodes: []domain.ModelNode{{ModelNodeInput: domain.ModelNodeInput{ID: "gpu-disabled", Enabled: false}, Version: 3}}}
	factory := &recordingFactory{}
	registry := New(store, factory.Start, monitor.NewCache(nil), time.Hour, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { registry.Run(ctx); close(done) }()
	waitFor(t, func() bool { return factory.started("gpu-disabled", 3) == 1 })

	registry.Wake()
	waitFor(t, func() bool { return factory.woken("gpu-disabled", 3) > 0 })
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("registry did not stop")
	}
	if factory.stopped("gpu-disabled", 3) != 1 {
		t.Fatal("disabled runtime was not stopped")
	}
}

type registryStore struct {
	mu    sync.Mutex
	nodes []domain.ModelNode
}

func (s *registryStore) ListModelNodes(context.Context) ([]domain.ModelNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.ModelNode(nil), s.nodes...), nil
}

func (s *registryStore) set(nodes []domain.ModelNode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes = append([]domain.ModelNode(nil), nodes...)
}

type runtimeKey struct {
	id      string
	version int64
}

type recordingFactory struct {
	mu     sync.Mutex
	starts map[runtimeKey]int
	stops  map[runtimeKey]int
	wakes  map[runtimeKey]int
}

func (f *recordingFactory) Start(_ context.Context, node domain.ModelNode) (Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.starts == nil {
		f.starts = map[runtimeKey]int{}
	}
	key := runtimeKey{id: node.ID, version: node.Version}
	f.starts[key]++
	return runtimeFuncs{
		wake: func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.wakes == nil {
				f.wakes = map[runtimeKey]int{}
			}
			f.wakes[key]++
		},
		stop: func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.stops == nil {
				f.stops = map[runtimeKey]int{}
			}
			f.stops[key]++
		},
	}, nil
}

func (f *recordingFactory) started(id string, version int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts[runtimeKey{id: id, version: version}]
}
func (f *recordingFactory) stopped(id string, version int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stops[runtimeKey{id: id, version: version}]
}
func (f *recordingFactory) woken(id string, version int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wakes[runtimeKey{id: id, version: version}]
}

type runtimeFuncs struct {
	wake func()
	stop func()
}

func (r runtimeFuncs) Wake() { r.wake() }
func (r runtimeFuncs) Stop() { r.stop() }

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met")
}
