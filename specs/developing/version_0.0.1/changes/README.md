# `version_0.0.1` 变更索引

## 目录说明

本目录记录 `v0.0.1` 需求确认后的迭代、缺陷和兼容修复。目录名必须使用 `{编号}-{功能名称}`，新增时同时检查开发中与归档目录的最大编号。

## 状态枚举

| 状态 | 说明 |
| --- | --- |
| Pending | 已创建，未开始 |
| In Progress | 开发中 |
| Completed | 开发闭环完成 |
| Blocked | 被阻塞 |
| Cancelled | 已取消 |

## 变更清单

| 编号 | 变更标题 | 类型 | 状态 | 负责人 | 关联文档 | 路径 | 备注 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 001-monitor-console | 只读监控控制台与节点状态缓存 | 迭代 | In Progress | 待人工填写 | CHANGE_SPEC.md、PRD_DELTA.md、PROTOTYPE_DELTA.md、TECH_SOLUTION.md、API_DELTA.md、task.md、TEST_ACCEPTANCE.md | `specs/developing/version_0.0.1/changes/001-monitor-console/` | 实现与本地验收完成，待 Docker 和真实环境人工联调 |
| 002-task-lifecycle-closure | 任务恢复、中止、删除与视频访问闭环 | 缺陷修复/迭代 | In Progress | 待人工填写 | CHANGE_SPEC.md、PRD_DELTA.md、TECH_SOLUTION.md、DATABASE_DELTA.md、API_DELTA.md、PROTOTYPE_DELTA.md、task.md、TEST_ACCEPTANCE.md | `specs/developing/version_0.0.1/changes/002-task-lifecycle-closure/` | 设计完成，待开发与真实模型故障演练 |
| 003-audio-base64-input | 音频 Data URI 输入与私有上传适配 | 兼容修复 | In Progress | 待人工填写 | CHANGE_SPEC.md、API_DELTA.md、TECH_SOLUTION.md、task.md、TEST_ACCEPTANCE.md | `specs/developing/version_0.0.1/changes/003-audio-base64-input/` | 实现与本地验证完成，真实私有服务联调待人工确认 |
| 004-manager-node-configuration | 管理后台、节点配置与 H3 Node API 契约修复 | 缺陷修复/迭代 | In Progress | 待人工填写 | CHANGE_SPEC.md、PRD_DELTA.md、TECH_SOLUTION.md、DATABASE_DELTA.md、API_DELTA.md、NODE_API_CONTRACT_AUDIT.md、api-modules/manager-nodes.md、api-modules/h3-node-integration.md、api-modules/task-delivery.md、PROTOTYPE_DELTA.md、task.md、TEST_ACCEPTANCE.md | `specs/developing/version_0.0.1/changes/004-manager-node-configuration/` | 节点真实闭环已完成；新增整任务 FIFO、Manager 视频播放与绝对 Proxy 结果 URL 设计，MN-026 至 MN-030 待 `/s-develop` |
| 005-api-key-management | 对外 API Key 后台管理与数据库迁移 | 迭代/安全优化 | In Progress | 待人工填写 | CHANGE_SPEC.md、PRD_DELTA.md、PROTOTYPE_DELTA.md、TECH_SOLUTION.md、API_DELTA.md、DATABASE_DELTA.md、task.md、TEST_ACCEPTANCE.md | `specs/developing/version_0.0.1/changes/005-api-key-management/` | 开发与本地自动验证完成；真实升级、回滚和发布仍需人工验收 |
| 006-node-single-key-conf | Node 单 Key 与 conf.yml 凭据配置 | 安全优化/配置与接口调整 | In Progress | 待人工填写 | CHANGE_SPEC.md、PRD_DELTA.md、PROTOTYPE_DELTA.md、TECH_SOLUTION.md、API_DELTA.md、DATABASE_DELTA.md、task.md、TEST_ACCEPTANCE.md | `specs/developing/version_0.0.1/changes/006-node-single-key-conf/` | `/s-develop` 实现与独立审查完成；取代 004 中 Node Key ID、scope 和复合 Token 规则，真实联调、ACL、GPU 与发布验收待人工确认 |
| 007-request-profile-simplification | 模型请求配置简化与参数收敛 | 缺陷修复/配置模型与页面优化 | Pending | 待人工填写 | CHANGE_SPEC.md、PRD_DELTA.md、PARAMETER_AUDIT.md、DATABASE_DELTA.md、PROTOTYPE_DELTA.md、TECH_SOLUTION.md、API_DELTA.md、task.md、TEST_ACCEPTANCE.md | `specs/developing/version_0.0.1/changes/007-request-profile-simplification/` | `/s-design` 完成；单 resolution 单配置、保存即生效、支持删除，并修复水印与无效参数问题，待 `/s-develop` 实施 |
| 008-direct-node-artifact-delivery | 节点直出视频签名链接 | 架构优化/接口与配置调整 | Pending | 待人工填写 | CHANGE_SPEC.md、PRD_DELTA.md、TECH_SOLUTION.md、API_DELTA.md、task.md、TEST_ACCEPTANCE.md | `specs/developing/version_0.0.1/changes/008-direct-node-artifact-delivery/` | 48 小时可重复访问的 Node 签名 URL，复用外部可达 `service_url`，待 `/s-develop` 实施 |
| 009-running-task-cancellation-consistency | 运行中任务中止一致性与 Node 调度隔离 | 缺陷修复/可靠性 | In Progress | 待人工填写 | CHANGE_SPEC.md、PRD_DELTA.md、TECH_SOLUTION.md、DATABASE_DELTA.md、API_DELTA.md、task.md、TEST_ACCEPTANCE.md | `specs/developing/version_0.0.1/changes/009-running-task-cancellation-consistency/` | 本地实现和自动化验证完成；真实 Node 中断、双 Node 故障演练和重启组合待人工确认 |

## 维护规则

- 新建 change 时立即追加索引，状态变化时同步更新。
- 不删除历史记录；取消或废弃使用状态标记。
- 负责人未知时写 `待人工填写`。
- 每个 change 至少维护 `CHANGE_SPEC.md`、`task.md` 和 `TEST_ACCEPTANCE.md`。
