package registry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/monitor"
	"minimax-h3-tc/internal/orchestrator"
	"minimax-h3-tc/internal/scheduler"
	"minimax-h3-tc/internal/upstream/gradio"
	"minimax-h3-tc/internal/upstream/nodeapi"
	"minimax-h3-tc/internal/worker"
)

type RuntimeClient interface {
	monitor.ProbeClient
}

type NodeSecretOpener interface {
	Open(nonce, ciphertext []byte) (string, error)
}

type NodeAPIClient interface {
	Health(context.Context, string) (nodeapi.Health, error)
	Capabilities(context.Context, string) (nodeapi.Capabilities, error)
	CreateExecution(context.Context, string, nodeapi.ExecutionRequest) (nodeapi.ExecutionReference, error)
	GetExecution(context.Context, string, string) (nodeapi.Execution, error)
	GetArtifact(context.Context, string, string) (nodeapi.Artifact, error)
	ImportArtifact(context.Context, string, nodeapi.ImportArtifactRequest) (nodeapi.Artifact, error)
	CancelExecution(context.Context, string, string, string) (nodeapi.ExecutionReference, error)
}

type RuntimeStore interface {
	worker.Store
	orchestrator.Store
}

type NodeRuntimeFactory struct {
	Store                RuntimeStore
	Cache                *monitor.Cache
	Profiles             map[string]config.GenerationProfile
	ExecutionTimeout     time.Duration
	MonitorInterval      time.Duration
	Logger               *slog.Logger
	Now                  func() time.Time
	ClientFactory        func(config.UpstreamConfig) RuntimeClient
	NodeSecrets          NodeSecretOpener
	ArtifactMigrator     orchestrator.InputArtifactMigrator
	NodeAPIClientFactory func(*url.URL, string, *http.Client, int64) NodeAPIClient
	InputSpoolRoot       string
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
	if normalized.UsesNodeAPI() {
		return f.startNodeAPI(parent, node, normalized, upstream)
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
	address := ""
	if upstream.ServiceURL != nil {
		address = upstream.ServiceURL.Host
	} else if upstream.BaseURL != nil {
		address = upstream.BaseURL.Host
	}
	snapshot := monitor.NodeSnapshot{
		ID: node.ID, Address: address, Disabled: !input.Enabled, Applying: true,
		SchedulingBlocked: previous.SchedulingBlocked,
	}
	blocked, err := f.Store.HasNodeDispatchBarrier(ctx, node.ID)
	if err != nil {
		return err
	}
	if blocked {
		snapshot.SchedulingBlocked = true
		snapshot.Runtime = monitor.RuntimeRunning
		snapshot.LastError = &monitor.ErrorSnapshot{Code: "node_cancel_reconciling"}
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

func (f NodeRuntimeFactory) startNodeAPI(parent context.Context, node domain.ModelNode, input domain.ModelNodeInput, upstream config.UpstreamConfig) (Runtime, error) {
	if f.Store == nil {
		return nil, errors.New("节点运行时 Store 未配置")
	}
	if f.Cache == nil {
		return nil, errors.New("节点运行时缓存未配置")
	}
	if f.NodeSecrets == nil {
		return nil, errors.New("节点 API Key 解密器未配置")
	}
	apiKey, err := f.NodeSecrets.Open(input.APIKeyNonce, input.APIKeyCiphertext)
	if err != nil {
		return nil, errors.New("节点 API Key 解密失败")
	}
	if err := f.initializeSnapshot(parent, node, input, upstream); err != nil {
		return nil, err
	}
	factory := f.NodeAPIClientFactory
	if factory == nil {
		factory = func(serviceURL *url.URL, apiKey string, client *http.Client, maxBody int64) NodeAPIClient {
			return nodeapi.NewClient(serviceURL, apiKey, client, maxBody)
		}
	}
	client := factory(upstream.ServiceURL, apiKey, &http.Client{Timeout: upstream.RequestTimeout}, 1<<20)
	if client == nil {
		return nil, errors.New("节点 API 客户端未创建")
	}
	nodeCtx, cancel := context.WithCancel(parent)
	probeWake := make(chan struct{}, 1)
	maxHealthAge := upstream.RequestTimeout + monitorInterval(f.MonitorInterval)
	processor := &orchestrator.Processor{
		Store: f.Store, Client: client, NodeID: node.ID, Migrator: f.ArtifactMigrator,
		Inputs:        &orchestrator.InputMaterializer{Store: f.Store, Logger: f.Logger, InputSpoolRoot: f.InputSpoolRoot},
		LeaseDuration: max(f.ExecutionTimeout, 10*time.Minute), PollInterval: input.PollInterval, Logger: f.Logger, Now: f.Now,
	}
	dispatcher := scheduler.New([]scheduler.Slot{{
		ID: node.ID, Processor: processor,
		Health: func(context.Context) error {
			if !input.Enabled {
				return domain.ErrNodeDisabled
			}
			return cachedSchedulable(f.Cache, node.ID, runtimeNow(f.Now), maxHealthAge)
		},
		Active: func(ctx context.Context) (bool, error) {
			blocked, err := f.Store.HasNodeDispatchBarrier(ctx, node.ID)
			if err != nil || blocked {
				return blocked, err
			}
			_, err = f.Store.ActiveForUpstream(ctx, node.ID)
			if errors.Is(err, domain.ErrTaskNotFound) {
				return false, nil
			}
			return err == nil, err
		},
	}}, time.Second, f.Logger)
	done := make(chan struct{})
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		f.runNodeAPIProbe(nodeCtx, node.ID, input.Enabled, client, probeWake)
	}()
	go func() {
		defer group.Done()
		dispatcher.Run(nodeCtx)
	}()
	go func() {
		group.Wait()
		close(done)
	}()
	return &nodeRuntime{
		cancel: cancel,
		done:   done,
		wake: func() {
			select {
			case probeWake <- struct{}{}:
			default:
			}
			dispatcher.Wake()
		},
	}, nil
}

func (f NodeRuntimeFactory) runNodeAPIProbe(ctx context.Context, nodeID string, enabled bool, client NodeAPIClient, wake <-chan struct{}) {
	interval := monitorInterval(f.MonitorInterval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		f.probeNodeAPI(ctx, nodeID, enabled, client)
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-ticker.C:
		}
	}
}

func (f NodeRuntimeFactory) probeNodeAPI(parent context.Context, nodeID string, enabled bool, client NodeAPIClient) {
	if parent.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, monitorInterval(f.MonitorInterval))
	defer cancel()
	now := runtimeNow(f.Now)
	blocked, barrierErr := f.Store.HasNodeDispatchBarrier(ctx, nodeID)
	health, err := client.Health(ctx, "monitor-health-"+nodeID)
	if barrierErr != nil {
		err = barrierErr
	}
	if err == nil && health.Status != "healthy" {
		err = errors.New("节点健康状态异常")
	}
	var capabilities nodeapi.Capabilities
	if err == nil {
		capabilities, err = client.Capabilities(ctx, "monitor-capabilities-"+nodeID)
	}
	var active domain.Task
	hasActive := false
	if err == nil {
		active, err = f.Store.ActiveForUpstream(ctx, nodeID)
		if errors.Is(err, domain.ErrTaskNotFound) {
			err = nil
		} else if err == nil {
			hasActive = true
		}
	}
	var latest domain.AdminTaskSummary
	hasLatest := false
	if err == nil {
		latest, err = f.Store.LatestFinishedForUpstream(ctx, nodeID)
		if errors.Is(err, domain.ErrTaskNotFound) {
			err = nil
		} else if err == nil {
			hasLatest = true
		}
	}
	if err != nil {
		f.Cache.Update(nodeID, func(snapshot *monitor.NodeSnapshot) {
			snapshot.Applying = false
			snapshot.Disabled = !enabled
			snapshot.Health = monitor.HealthUnhealthy
			snapshot.Runtime = monitor.RuntimeUnknown
			snapshot.SchedulingBlocked = blocked
			snapshot.CheckedAt = now
			snapshot.UpdatedAt = now
			code := "node_api_unhealthy"
			if blocked {
				code = "node_cancel_reconciling"
				snapshot.Runtime = monitor.RuntimeRunning
			}
			snapshot.LastError = &monitor.ErrorSnapshot{Code: code}
		})
		return
	}
	queue, memory, vram, cpu, gpu := healthRuntimeSnapshot(health.Runtime)
	runtimeStatus := monitor.RuntimeIdle
	if hasActive || blocked || queue != nil && *queue > 0 {
		runtimeStatus = monitor.RuntimeRunning
	}
	f.Cache.Update(nodeID, func(snapshot *monitor.NodeSnapshot) {
		snapshot.Applying = false
		snapshot.Disabled = !enabled
		snapshot.Health = monitor.HealthHealthy
		snapshot.SchedulingBlocked = blocked
		if blocked {
			snapshot.Health = monitor.HealthUnhealthy
		}
		snapshot.Runtime = runtimeStatus
		snapshot.PrivateQueue = queue
		snapshot.MemoryPercent = memory
		snapshot.VRAMPercent = vram
		snapshot.CPUPercent = cpu
		snapshot.GPUPercent = gpu
		snapshot.CurrentTask = nil
		if hasActive {
			snapshot.CurrentTask = &monitor.CurrentTaskSnapshot{
				ID: active.TaskID, Status: string(active.Status.V2()), StartedAt: active.StartedAt,
			}
		}
		snapshot.LatestFinishedTask = nil
		if hasLatest {
			duration := int64(0)
			if !latest.StartedAt.IsZero() && !latest.FinishedAt.Before(latest.StartedAt) {
				duration = int64(latest.FinishedAt.Sub(latest.StartedAt) / time.Second)
			}
			snapshot.LatestFinishedTask = &monitor.FinishedTaskSnapshot{
				ID: latest.TaskID, APIKeyID: latest.APIKeyID, Status: string(latest.Status),
				DurationSeconds: duration, FinishedAt: latest.FinishedAt,
			}
		}
		snapshot.CheckedAt = now
		snapshot.LastHealthyAt = now
		snapshot.UpdatedAt = now
		snapshot.LastError = nil
		if blocked {
			snapshot.LastError = &monitor.ErrorSnapshot{Code: "node_cancel_reconciling"}
		}
		snapshot.Capabilities = capabilities.Raw
	})
}

func healthRuntimeSnapshot(runtime *nodeapi.HealthRuntime) (queue *int, memory, vram, cpu, gpu *float64) {
	if runtime == nil {
		return nil, nil, nil, nil, nil
	}
	if runtime.QueueRunning != nil || runtime.QueuePending != nil {
		value := 0
		valid := true
		for _, count := range []*int{runtime.QueueRunning, runtime.QueuePending} {
			if count == nil {
				continue
			}
			if *count < 0 {
				valid = false
				break
			}
			value += *count
		}
		if valid {
			queue = &value
		}
	}
	memory = usedPercent(runtime.MemoryTotalBytes, runtime.MemoryFreeBytes)
	vram = usedPercent(runtime.VRAMTotalBytes, runtime.VRAMFreeBytes)
	cpu = validPercent(runtime.CPUPercent)
	gpu = validPercent(runtime.GPUPercent)
	return queue, memory, vram, cpu, gpu
}

func usedPercent(total, free *int64) *float64 {
	if total == nil || free == nil || *total <= 0 || *free < 0 || *free > *total {
		return nil
	}
	value := float64(*total-*free) / float64(*total) * 100
	return &value
}

func validPercent(value *float64) *float64 {
	if value == nil || *value < 0 || *value > 100 {
		return nil
	}
	copyValue := *value
	return &copyValue
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
