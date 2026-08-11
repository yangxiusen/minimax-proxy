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
