package registry

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/monitor"
)

type NodeStore interface {
	ListModelNodes(context.Context) ([]domain.ModelNode, error)
}

type Runtime interface {
	Wake()
	Stop()
}

type RuntimeFactory func(context.Context, domain.ModelNode) (Runtime, error)

type entry struct {
	version int64
	runtime Runtime
}

type Registry struct {
	store    NodeStore
	factory  RuntimeFactory
	cache    *monitor.Cache
	interval time.Duration
	logger   *slog.Logger
	wake     chan struct{}
	mu       sync.Mutex
	entries  map[string]entry
}

func New(store NodeStore, factory RuntimeFactory, cache *monitor.Cache, interval time.Duration, logger *slog.Logger) *Registry {
	if interval <= 0 {
		interval = time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	if cache == nil {
		cache = monitor.NewCache(nil)
	}
	return &Registry{
		store: store, factory: factory, cache: cache, interval: interval, logger: logger,
		wake: make(chan struct{}, 1), entries: make(map[string]entry),
	}
}

func (r *Registry) Run(ctx context.Context) {
	if r.store == nil || r.factory == nil {
		return
	}
	r.reconcile(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	defer r.stopAll()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
			r.reconcile(ctx)
		case <-ticker.C:
			r.reconcile(ctx)
		}
	}
}

func (r *Registry) Wake() {
	r.mu.Lock()
	runtimes := make([]Runtime, 0, len(r.entries))
	for _, current := range r.entries {
		runtimes = append(runtimes, current.runtime)
	}
	r.mu.Unlock()
	for _, runtime := range runtimes {
		runtime.Wake()
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *Registry) reconcile(ctx context.Context) {
	nodes, err := r.store.ListModelNodes(ctx)
	if err != nil {
		r.logger.ErrorContext(ctx, "读取模型节点配置失败", "stage", "node_reconcile", "error_code", "node_config_read_failed")
		return
	}
	desired := make(map[string]domain.ModelNode, len(nodes))
	for _, node := range nodes {
		desired[node.ID] = node
	}

	type stoppedEntry struct {
		id      string
		deleted bool
		runtime Runtime
	}
	var stopped []stoppedEntry
	var starts []domain.ModelNode
	r.mu.Lock()
	for id, current := range r.entries {
		node, exists := desired[id]
		if exists && node.Version == current.version {
			delete(desired, id)
			continue
		}
		delete(r.entries, id)
		stopped = append(stopped, stoppedEntry{id: id, deleted: !exists, runtime: current.runtime})
	}
	for _, node := range desired {
		starts = append(starts, node)
	}
	r.mu.Unlock()

	for _, current := range stopped {
		current.runtime.Stop()
		if current.deleted {
			r.cache.Delete(current.id)
		}
	}
	for _, node := range starts {
		if ctx.Err() != nil {
			return
		}
		runtime, err := r.factory(ctx, node)
		if err != nil {
			r.logger.ErrorContext(ctx, "应用模型节点配置失败", "node_id", node.ID, "config_version", node.Version, "stage", "node_reconcile", "error_code", "node_runtime_start_failed")
			continue
		}
		r.mu.Lock()
		r.entries[node.ID] = entry{version: node.Version, runtime: runtime}
		r.mu.Unlock()
	}
}

func (r *Registry) stopAll() {
	r.mu.Lock()
	runtimes := make([]Runtime, 0, len(r.entries))
	for id, current := range r.entries {
		runtimes = append(runtimes, current.runtime)
		delete(r.entries, id)
	}
	r.mu.Unlock()
	for _, runtime := range runtimes {
		runtime.Stop()
	}
}
