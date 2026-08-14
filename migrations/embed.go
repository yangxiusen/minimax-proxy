package migrations

import _ "embed"

// Initial 保存首版完整数据库结构，由服务启动时执行。
//
//go:embed 001_init.sql
var Initial string

// ResolutionTiers 将历史 768P 档修正为 480P，并扩展允许的分辨率。
//
//go:embed 002_resolution_tiers.sql
var ResolutionTiers string

// TaskLifecycleClosure 增加模型任务关联、一次重试和中止中状态。
//
//go:embed 003_task_lifecycle_closure.sql
var TaskLifecycleClosure string

// ModelServiceNodes 增加模型节点配置和旧 YAML 一次性导入标记。
//
//go:embed 004_model_service_nodes.sql
var ModelServiceNodes string

// ProfilesAndStages 增加请求配置版本、测试运行、任务阶段和阶段尝试。
//
//go:embed 005_profiles_and_stages.sql
var ProfilesAndStages string

// ArtifactLifecycle 增加逻辑产物、物理位置、可靠删除与回调投递。
//
//go:embed 006_artifact_lifecycle.sql
var ArtifactLifecycle string

// SingleEndpointNodes 将节点迁移到单服务入口并保留旧协议兼容字段。
//
//go:embed 007_single_endpoint_nodes.sql
var SingleEndpointNodes string

// CallbackDeliveryPayload 保存可重复签名的稳定 Body 和发送租约。
//
//go:embed 008_callback_delivery_payload.sql
var CallbackDeliveryPayload string

// ExternalAPIKeys 增加数据库管理的外部 API Key 和旧配置导入标记。
//
//go:embed 010_external_api_keys.sql
var ExternalAPIKeys string

// RequestProfileSimplification 将请求配置收敛为按分辨率唯一并清理旧测试数据。
//
//go:embed 011_request_profile_simplification.sql
var RequestProfileSimplification string

// ExternalAPIKeyPlaintext 按管理需求保存可复制的外部 API Key 明文。
//
//go:embed 012_external_api_key_plaintext.sql
var ExternalAPIKeyPlaintext string

// DynamicRequestResolutions 移除固定分辨率约束并增加规范化名称唯一键。
//
//go:embed 013_dynamic_request_resolutions.sql
var DynamicRequestResolutions string

// NodeDispatchBarriers 增加节点取消对账屏障和阶段请求快照。
//
//go:embed 014_node_dispatch_barriers.sql
var NodeDispatchBarriers string
