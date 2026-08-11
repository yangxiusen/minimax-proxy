package registry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/monitor"
	"minimax-h3-tc/internal/scheduler"
	"minimax-h3-tc/internal/upstream/gradio"
	"minimax-h3-tc/internal/worker"
)

type RuntimeClient interface {
	monitor.ProbeClient
}

type NodeRuntimeFactory struct {
	Store            worker.Store
	Cache            *monitor.Cache
	Profiles         map[string]config.GenerationProfile
	ExecutionTimeout time.Duration
	MonitorInterval  time.Duration
	Logger           *slog.Logger
	Now              func() time.Time
	ClientFactory    func(config.UpstreamConfig) RuntimeClient
}

func (f NodeRuntimeFactory) Start(parent context.Context, node domain.ModelNode) (Runtime, error) {
	if f.Store == nil {
		return nil, errors.New("节点运行时 Store 未配置")
	}
	if f.Cache == nil {
		return nil, errors.New("节点运行时缓存未配置")
	}
	normalized, upstream, err := config.NormalizeModelNode(node.ModelNodeInput)
	if err != nil {
		return nil, err
	}
	logger := f.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clientFactory := f.ClientFactory
	if clientFactory == nil {
		clientFactory = func(upstream config.UpstreamConfig) RuntimeClient {
			httpClient := &http.Client{Timeout: upstream.RequestTimeout}
			return gradio.NewClientWithJobs(upstream.BaseURL, upstream.JobsBaseURL, httpClient, 8<<20)
		}
	}
	client := clientFactory(upstream)
	if client == nil {
		return nil, errors.New("节点运行时客户端未创建")
	}
	gate := &sync.Mutex{}
	if err := f.initializeSnapshot(parent, node, normalized, upstream); err != nil {
		return nil, err
	}
	maxHealthAge := upstream.RequestTimeout + monitorInterval(f.MonitorInterval)
	processor := &worker.Processor{
		Store: f.Store, Client: client, Upstream: upstream, Profiles: f.Profiles, Logger: logger, Cache: f.Cache, Gate: gate,
		ExecutionTimeout: f.ExecutionTimeout, NodeVersion: node.Version, Now: f.Now,
	}
	dispatcher := scheduler.New([]scheduler.Slot{{
		ID: node.ID, Processor: processor,
		Health: func(context.Context) error {
			if !normalized.Enabled {
				return domain.ErrNodeDisabled
			}
			return cachedSchedulable(f.Cache, node.ID, runtimeNow(f.Now), maxHealthAge)
		},
		Active: func(ctx context.Context) (bool, error) {
			_, err := f.Store.ActiveForUpstream(ctx, node.ID)
			if errors.Is(err, domain.ErrTaskNotFound) {
				return false, nil
			}
			return err == nil, err
		},
	}}, time.Second, logger)
	collector := &monitor.Collector{
		Cache: f.Cache, Nodes: []monitor.CollectorNode{{Upstream: upstream, Client: client, Gate: gate}},
		Interval: monitorInterval(f.MonitorInterval), Now: f.Now, Logger: logger,
	}
	nodeCtx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		dispatcher.Run(nodeCtx)
	}()
	go func() {
		defer group.Done()
		collector.Run(nodeCtx)
	}()
	go func() {
		group.Wait()
		close(done)
	}()
	return &nodeRuntime{cancel: cancel, done: done, wake: dispatcher.Wake}, nil
}

func (f NodeRuntimeFactory) initializeSnapshot(ctx context.Context, node domain.ModelNode, input domain.ModelNodeInput, upstream config.UpstreamConfig) error {
	previous, _ := f.Cache.Get(node.ID)
	snapshot := monitor.NodeSnapshot{
		ID: node.ID, Address: upstream.BaseURL.Host, Disabled: !input.Enabled, Applying: true,
		SchedulingBlocked: previous.SchedulingBlocked,
	}
	active, err := f.Store.ActiveForUpstream(ctx, node.ID)
	if err != nil && !errors.Is(err, domain.ErrTaskNotFound) {
		return err
	}
	if err == nil {
		snapshot.Runtime = monitor.RuntimeRunning
		snapshot.CurrentTask = &monitor.CurrentTaskSnapshot{ID: active.TaskID, Status: string(active.Status.V2()), StartedAt: active.StartedAt}
	}
	latest, latestErr := f.Store.LatestFinishedForUpstream(ctx, node.ID)
	if latestErr != nil && !errors.Is(latestErr, domain.ErrTaskNotFound) {
		return latestErr
	}
	if latestErr == nil {
		duration := int64(0)
		if !latest.StartedAt.IsZero() && !latest.FinishedAt.Before(latest.StartedAt) {
			duration = int64(latest.FinishedAt.Sub(latest.StartedAt) / time.Second)
		}
		snapshot.LatestFinishedTask = &monitor.FinishedTaskSnapshot{
			ID: latest.TaskID, APIKeyID: latest.APIKeyID, Status: string(latest.Status), DurationSeconds: duration, FinishedAt: latest.FinishedAt,
		}
	}
	f.Cache.Set(snapshot)
	return nil
}

type nodeRuntime struct {
	cancel context.CancelFunc
	done   <-chan struct{}
	wake   func()
	once   sync.Once
}

func (r *nodeRuntime) Wake() {
	if r.wake != nil {
		r.wake()
	}
}

func (r *nodeRuntime) Stop() {
	r.once.Do(func() {
		r.cancel()
		<-r.done
	})
}

func cachedSchedulable(cache *monitor.Cache, id string, now time.Time, maxAge time.Duration) error {
	node, ok := cache.Get(id)
	if !ok || node.Disabled || node.Applying || node.Health != monitor.HealthHealthy || node.CheckedAt.IsZero() || node.CheckedAt.Before(now.Add(-maxAge)) {
		return errors.New("节点健康状态不可用或已过期")
	}
	if node.SchedulingBlocked || node.Runtime == monitor.RuntimeRunning || node.PrivateQueue != nil && *node.PrivateQueue > 0 {
		return errors.New("私有实例仍有任务运行")
	}
	return nil
}

func monitorInterval(value time.Duration) time.Duration {
	if value <= 0 {
		return 5 * time.Second
	}
	return value
}

func runtimeNow(now func() time.Time) time.Time {
	if now != nil {
		return now().UTC()
	}
	return time.Now().UTC()
}
