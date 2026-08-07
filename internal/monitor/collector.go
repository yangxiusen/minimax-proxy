package monitor

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/upstream/gradio"
)

type ProbeClient interface {
	Healthy(context.Context, string) error
	Call(context.Context, string, []any) ([]any, error)
}

type CollectorNode struct {
	Upstream config.UpstreamConfig
	Client   ProbeClient
	Gate     *sync.Mutex
}

type Collector struct {
	Cache    *Cache
	Nodes    []CollectorNode
	Interval time.Duration
	Now      func() time.Time
	Logger   *slog.Logger
}

func (c *Collector) Run(ctx context.Context) {
	if c.Cache == nil {
		return
	}
	var group sync.WaitGroup
	for _, configured := range c.Nodes {
		node := configured
		if node.Client == nil {
			continue
		}
		group.Add(1)
		go func() {
			defer group.Done()
			c.runNode(ctx, node)
		}()
	}
	group.Wait()
}

func (c *Collector) runNode(ctx context.Context, node CollectorNode) {
	c.probe(ctx, node)
	interval := c.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.probe(ctx, node)
		}
	}
}

func (c *Collector) probe(ctx context.Context, node CollectorNode) {
	if c.Cache == nil || node.Client == nil || ctx.Err() != nil {
		return
	}
	if node.Gate != nil {
		if !node.Gate.TryLock() {
			return
		}
		defer node.Gate.Unlock()
	}
	timeout := node.Upstream.RequestTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := node.Client.Healthy(probeCtx, node.Upstream.HealthPath); err != nil {
		c.markFailure(node.Upstream.ID, "upstream_unhealthy")
		c.logger().WarnContext(ctx, "私有服务监控健康检查失败", "upstream_id", node.Upstream.ID, "stage", "monitor_health", "error_code", "upstream_unhealthy")
		return
	}
	result, err := node.Client.Call(probeCtx, node.Upstream.CheckAPIName, []any{})
	if err != nil {
		c.markFailure(node.Upstream.ID, "upstream_poll_error")
		c.logger().WarnContext(ctx, "私有服务监控状态查询失败", "upstream_id", node.Upstream.ID, "stage", "monitor_poll", "error_code", "upstream_poll_error")
		return
	}
	c.mergeObservation(node.Upstream.ID, gradio.ParseObservation(result))
}

func (c *Collector) mergeObservation(id string, observation gradio.Observation) {
	now := c.now()
	c.Cache.Update(id, func(node *NodeSnapshot) {
		node.Health = HealthHealthy
		node.CheckedAt = now
		node.LastHealthyAt = now
		node.UpdatedAt = now
		node.PrivateQueue = observation.PrivateQueue
		node.CPUPercent = observation.CPUPercent
		node.MemoryPercent = observation.MemoryPercent
		node.GPUPercent = observation.GPUPercent
		node.VRAMPercent = observation.VRAMPercent
		node.LastError = nil
		if node.CurrentTask != nil {
			node.Runtime = RuntimeRunning
			return
		}
		switch observation.Status {
		case gradio.ObservationIdle:
			node.Runtime = RuntimeIdle
		case gradio.ObservationRunning:
			node.Runtime = RuntimeRunning
		default:
			node.Runtime = RuntimeUnknown
		}
	})
}

func (c *Collector) markFailure(id, code string) {
	now := c.now()
	c.Cache.Update(id, func(node *NodeSnapshot) {
		node.Health = HealthUnhealthy
		node.CheckedAt = now
		node.UpdatedAt = now
		node.LastError = &ErrorSnapshot{Code: code}
	})
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Collector) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}
