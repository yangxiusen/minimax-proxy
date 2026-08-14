package monitor

import (
	"sort"
	"sync"
	"time"
)

type HealthStatus string

const (
	HealthUnknown   HealthStatus = "unknown"
	HealthHealthy   HealthStatus = "healthy"
	HealthUnhealthy HealthStatus = "unhealthy"
)

type RuntimeStatus string

const (
	RuntimeUnknown RuntimeStatus = "unknown"
	RuntimeIdle    RuntimeStatus = "idle"
	RuntimeRunning RuntimeStatus = "running"
)

type CurrentTaskSnapshot struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
}

type FinishedTaskSnapshot struct {
	ID              string    `json:"id"`
	APIKeyID        string    `json:"api_key_id"`
	Status          string    `json:"status"`
	DurationSeconds int64     `json:"duration_seconds"`
	FinishedAt      time.Time `json:"finished_at"`
}

type ErrorSnapshot struct {
	Code    string `json:"code"`
	Summary string `json:"summary"`
}

type NodeSnapshot struct {
	ID                 string                `json:"id"`
	Address            string                `json:"address"`
	Health             HealthStatus          `json:"health"`
	Runtime            RuntimeStatus         `json:"runtime"`
	PrivateQueue       *int                  `json:"private_queue"`
	CPUPercent         *float64              `json:"cpu_percent"`
	MemoryPercent      *float64              `json:"memory_percent"`
	GPUPercent         *float64              `json:"gpu_percent"`
	VRAMPercent        *float64              `json:"vram_percent"`
	CheckedAt          time.Time             `json:"checked_at"`
	LastHealthyAt      time.Time             `json:"last_healthy_at"`
	UpdatedAt          time.Time             `json:"updated_at"`
	CurrentTask        *CurrentTaskSnapshot  `json:"current_task"`
	LatestFinishedTask *FinishedTaskSnapshot `json:"latest_finished_task"`
	LastError          *ErrorSnapshot        `json:"last_error"`
	Disabled           bool                  `json:"-"`
	Applying           bool                  `json:"-"`
	SchedulingBlocked  bool                  `json:"-"`
	Capabilities       map[string]any        `json:"-"`
}

type Cache struct {
	mu    sync.RWMutex
	nodes map[string]NodeSnapshot
}

func NewCache(nodes []NodeSnapshot) *Cache {
	cache := &Cache{nodes: make(map[string]NodeSnapshot, len(nodes))}
	for _, node := range nodes {
		node = normalizeNode(node)
		cache.nodes[node.ID] = cloneNode(node)
	}
	return cache
}

func (c *Cache) Set(node NodeSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	node = normalizeNode(node)
	c.nodes[node.ID] = cloneNode(node)
}

func (c *Cache) Update(id string, update func(*NodeSnapshot)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	node := cloneNode(c.nodes[id])
	node.ID = id
	if update != nil {
		update(&node)
	}
	node = normalizeNode(node)
	c.nodes[id] = cloneNode(node)
}

func (c *Cache) Delete(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.nodes, id)
}

func (c *Cache) Get(id string) (NodeSnapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	node, ok := c.nodes[id]
	return cloneNode(node), ok
}

func (c *Cache) List() []NodeSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]NodeSnapshot, 0, len(c.nodes))
	for _, node := range c.nodes {
		result = append(result, cloneNode(node))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (c *Cache) HealthyCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	count := 0
	for _, node := range c.nodes {
		if !node.Disabled && !node.Applying && node.Health == HealthHealthy {
			count++
		}
	}
	return count
}

func (c *Cache) Available() bool {
	return c.HealthyCount() > 0
}

func (c *Cache) AvailableFresh(now time.Time, maxAge time.Duration) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, node := range c.nodes {
		if !node.Disabled && !node.Applying && node.Health == HealthHealthy && !node.CheckedAt.IsZero() && !node.CheckedAt.Before(now.Add(-maxAge)) {
			return true
		}
	}
	return false
}

func cloneNode(node NodeSnapshot) NodeSnapshot {
	if node.Capabilities != nil {
		node.Capabilities = cloneMap(node.Capabilities)
	}
	if node.PrivateQueue != nil {
		value := *node.PrivateQueue
		node.PrivateQueue = &value
	}
	if node.CPUPercent != nil {
		value := *node.CPUPercent
		node.CPUPercent = &value
	}
	if node.MemoryPercent != nil {
		value := *node.MemoryPercent
		node.MemoryPercent = &value
	}
	if node.GPUPercent != nil {
		value := *node.GPUPercent
		node.GPUPercent = &value
	}
	if node.VRAMPercent != nil {
		value := *node.VRAMPercent
		node.VRAMPercent = &value
	}
	if node.CurrentTask != nil {
		value := *node.CurrentTask
		node.CurrentTask = &value
	}
	if node.LatestFinishedTask != nil {
		value := *node.LatestFinishedTask
		node.LatestFinishedTask = &value
	}
	if node.LastError != nil {
		value := *node.LastError
		node.LastError = &value
	}
	return node
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		switch typed := value.(type) {
		case map[string]any:
			result[key] = cloneMap(typed)
		case []any:
			result[key] = append([]any(nil), typed...)
		case []string:
			result[key] = append([]string(nil), typed...)
		case []int:
			result[key] = append([]int(nil), typed...)
		default:
			result[key] = value
		}
	}
	return result
}

func normalizeNode(node NodeSnapshot) NodeSnapshot {
	if node.Health == "" {
		node.Health = HealthUnknown
	}
	if node.Runtime == "" {
		node.Runtime = RuntimeUnknown
	}
	if node.LastError != nil {
		node.LastError = sanitizeError(node.LastError.Code)
	}
	return node
}

func sanitizeError(code string) *ErrorSnapshot {
	switch code {
	case "upstream_unhealthy":
		return &ErrorSnapshot{Code: code, Summary: "私有服务连接失败"}
	case "upstream_poll_error":
		return &ErrorSnapshot{Code: code, Summary: "私有服务状态查询失败"}
	case "upstream_protocol_error":
		return &ErrorSnapshot{Code: code, Summary: "私有服务状态响应异常"}
	case "upstream_jobs_unhealthy":
		return &ErrorSnapshot{Code: code, Summary: "私有任务服务连接失败"}
	case "upstream_cancel_unconfirmed":
		return &ErrorSnapshot{Code: code, Summary: "私有任务中止状态待确认"}
	case "node_api_unhealthy":
		return &ErrorSnapshot{Code: code, Summary: "模型节点 API 健康检查失败"}
	default:
		return &ErrorSnapshot{Code: "upstream_error", Summary: "私有服务异常"}
	}
}
