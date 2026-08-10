# 任务生命周期闭环开发任务

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 版本/变更 | `002-task-lifecycle-closure` |
| 输入文档 | `CHANGE_SPEC.md`、`TECH_SOLUTION.md`、`DATABASE_DELTA.md`、`API_DELTA.md`、`PROTOTYPE_DELTA.md` |
| 执行方式 | 测试驱动，按依赖顺序在当前会话内实施 |

## 2. 任务总览

| 任务ID | 任务名称 | 优先级 | 状态 | 依赖 |
| --- | --- | --- | --- | --- |
| LC-001 | 设计产物复核与接口/状态命名确认 | High | Completed | - |
| LC-002 | 增加 10 分钟执行超时配置和 SQLite 迁移 | High | Pending | LC-001 |
| LC-003 | 实现私有 Jobs API列举、查询和取消客户端 | High | Pending | LC-002 |
| LC-004 | 实现 Store 的 job 关联、唯一重试、中止和终态删除事务 | High | Pending | LC-002 |
| LC-005 | 重构 Worker 为按 job ID对账、恢复、超时、重试和中止闭环 | High | Pending | LC-003、LC-004 |
| LC-006 | 扩展管理任务列表并实现中止、删除接口 | High | Pending | LC-004、LC-005 |
| LC-007 | 实现监控页面操作列、确认、播放和状态反馈 | High | Pending | LC-006 |
| LC-008 | 补齐故障恢复、并发竞态、迁移和页面资源测试 | High | Pending | LC-005、LC-007 |
| LC-009 | 运行格式化、测试、竞态、静态检查和构建 | High | Pending | LC-008 |
| LC-010 | 执行代码审查并修复阻塞问题 | High | Pending | LC-009 |
| LC-011 | 同步配置示例、运维说明和任务状态 | Medium | Pending | LC-010 |

## 3. 文件范围

- 数据与配置：`migrations/003_task_lifecycle_closure.sql`、`internal/config/`、`config.example.yaml`。
- 上游适配：`internal/upstream/gradio/`。
- 状态与执行：`internal/domain/task.go`、`internal/store/sqlite/`、`internal/worker/`。
- 管理接口与页面：`internal/httpapi/monitor/`。
- 装配：`cmd/server/main.go` 及相关测试。

## 4. 完成标准

- [ ] 任务在成功、明确失败、丢失、超时、中止任一路径进入持久化终态并释放实例。
- [ ] 自动重试跨重启最多一次。
- [ ] 管理中止、删除和播放符合状态权限及二次确认要求。
- [ ] `go test ./...`、`go vet ./...`、两入口构建通过。
- [ ] 代码审查无未处理的阻塞问题，文档同步完成。

真实模型联调、故障演练、性能验收和发布确认只记录在 `TEST_ACCEPTANCE.md`，不标记为 AI 已完成。
