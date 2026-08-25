package domain

import (
	"errors"
	"time"
)

var (
	ErrNodeNotFound        = errors.New("模型服务节点不存在")
	ErrNodeIDConflict      = errors.New("模型服务节点 ID 已存在")
	ErrNodeVersionConflict = errors.New("模型服务节点配置版本冲突")
	ErrNodeHasActiveTask   = errors.New("模型服务节点存在活动任务")
	ErrNodeMustBeDisabled  = errors.New("模型服务节点必须先停用")
	ErrNodeConfigStale     = errors.New("模型服务节点配置已过期")
	ErrNodeDisabled        = errors.New("模型服务节点已停用")
)

type ModelNodeInput struct {
	ServiceURL        string
	ProtocolVersion   string
	APIKeyCiphertext  []byte
	APIKeyNonce       []byte
	APIKeyFingerprint string
	UpstreamModel     string
	MaxConcurrency    int
	ReplaceResultURL  bool

	// Legacy 字段只用于读取和运行升级前的 gradio-v1 节点。
	ID             string
	BaseURL        string
	JobsBaseURL    string
	PublicBaseURL  string
	HealthPath     string
	SubmitAPIName  string
	CheckAPIName   string
	PollInterval   time.Duration
	RequestTimeout time.Duration
	Enabled        bool
}

func (input ModelNodeInput) UsesNodeAPI() bool {
	return input.ProtocolVersion == "h3-node-v1"
}

func (input ModelNodeInput) UsesOfficialV2() bool {
	return input.ProtocolVersion == "minimax-v2"
}

type ModelNode struct {
	ModelNodeInput
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}
