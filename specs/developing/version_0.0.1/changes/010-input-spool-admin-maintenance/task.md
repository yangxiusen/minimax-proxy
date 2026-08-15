# 开发任务列表

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 版本/变更 | `010-input-spool-admin-maintenance` |
| 任务范围 | 输入临时文件、Manager 任务详情、物理删除、数据库自动迁移 |
| 输入文档 | `CHANGE_SPEC.md`、`TECH_SOLUTION.md`、`DATABASE_DELTA.md`、`API_DELTA.md`、`PROTOTYPE_DELTA.md`、`TEST_ACCEPTANCE.md` |

## 2. 任务总览

| 任务ID | 任务名称 | 优先级 | 状态 | 依赖任务 | AI可完成 | 说明 |
| --- | --- | --- | --- | --- | --- | --- |
| DSG-001 | 设计确认 | High | Completed | - | 是 | 已按 010 设计执行 |
| DB-001 | 新增 v15 迁移和 Store 元数据方法 | High | Completed | DSG-001 | 是 | 已创建 `task_input_spool_files` 并接入自动迁移 |
| SPL-001 | 实现输入托管模块 | High | Completed | DB-001 | 是 | 已实现 Data URI 解码、格式识别、原子写入、清理 |
| V2-001 | 创建任务接入输入托管 | High | Completed | SPL-001 | 是 | 已改写为 `proxy-input://`，并增加幂等预查 |
| ORC-001 | 执行阶段支持 `proxy-input://` | High | Completed | V2-001 | 是 | 已从托管文件读取并继续导入 Node |
| MGR-001 | 新增 Manager 任务详情接口 | High | Completed | DB-001,V2-001 | 是 | 已返回脱敏请求、媒体元数据和配置摘要 |
| MGR-002 | Manager 前端新增查看弹窗 | Medium | Completed | MGR-001 | 是 | 已增加任务行查看按钮、弹窗状态和错误提示 |
| MGR-003 | Manager 媒体输入文件查看与下载 | Medium | Completed | MGR-001,SPL-001 | 是 | 已新增受保护文件内容接口，并在媒体输入行提供“查看/下载”操作 |
| DEL-001 | 实现终态任务物理删除 | High | Completed | DB-001,SPL-001 | 是 | 已实现无取消屏障且远端产物删除成功后物理删除 DB 和本地目录；真实 Node 行为待人工联调确认 |
| CLN-001 | 启动孤儿临时目录清理 | Medium | Completed | SPL-001,DB-001 | 是 | 已清理 `.part`、候选目录和无 DB 关联目录，不自动删除合法任务 |
| TEST-001 | 本地自动化测试 | High | Completed | ORC-001,MGR-002,DEL-001,CLN-001 | 是 | 已覆盖创建、执行、查看、删除、迁移、孤儿清理 |
| CR-001 | 代码审查并修复问题 | High | Completed | TEST-001 | 是 | 已完成本地自审并修复发现问题 |
| DOC-001 | 文档同步 | Medium | Completed | CR-001 | 是 | 已更新任务状态和验证记录 |

## 3. 开发要点

- `internal/inputspool` 建议作为新包，避免把文件系统细节塞进 V2 handler 或 Orchestrator；根目录默认从 `database.path` 的目录推导为 `temp-inputs`。
- `TaskStore.Create` 可扩展输入参数，保证任务、stage 和 spool 元数据同事务写入。
- 幂等重复命中已有任务时优先在写临时文件前返回；并发竞争导致候选目录已创建时，失败方必须删除候选目录。
- `InputMaterializer` 必须同时支持 `proxy-input://`、HTTP/HTTPS 和 legacy Data URI。
- Manager 详情接口不得返回 Base64 正文和本地绝对路径。
- 物理删除不能先删 DB 再尝试远端删除，否则会丢失 Node artifact 线索。
- Manager 物理删除在远端 Node 删除成功后，最终 DB 事务必须复核当前 artifact location 与已删除快照一致；若发生并发变化，返回冲突并保留 DB/本地文件。
- `cancelled` 任务存在 `node_dispatch_barriers` 时必须禁止删除，不能删除屏障上下文。
- 物理删除要先清空 `video_tasks.active_stage_id/result_artifact_id` 和 `task_stages` artifact 引用，再按外键顺序删除子表，最后删除 `video_tasks`。
- 启动孤儿清理只能删除无 `video_tasks`/`task_input_spool_files` 关联且超过安全窗口的临时文件，不能自动删除终态任务。
- v15 迁移加入 `migrations/embed.go` 和 `store.migrate`，启动自动执行。

## 4. 完成标准

- [x] 新任务的 `video_tasks.request_json` 不再保存 Base64 正文。
- [x] Data URI 文件按真实格式保存，字节与原始解码结果一致。
- [x] Proxy 重启后排队任务可继续执行。
- [x] Manager 可以查看任务请求详情且不泄露 Base64。
- [x] Manager 任务详情中的本地托管媒体输入可以点击查看和下载，且不泄露本地路径。
- [x] Manager 删除终态任务后，SQLite 关联行和本地输入目录被物理删除。
- [x] cancelled 任务仍有 Node 调度屏障时，Manager 删除返回 409 且不删除任何上下文。
- [x] v14 数据库启动新版本后自动迁移到 v15。
- [x] `go test ./...` 通过。
- [x] `go vet ./...` 无阻塞问题。
- [x] 代码审查完成并修复阻塞问题。

## 5. 本地验证记录

| 命令 | 结果 |
| --- | --- |
| `go test ./...` | 通过 |
| `go vet ./...` | 通过 |
| `go build ./cmd/server ./cmd/healthcheck` | 通过 |
| `go test ./internal/httpapi/manager -run "Test(TaskInputContentRequiresAuthenticationAndSupportsInlineAndDownload\|ManagerPageIncludesTaskDetailDialog\|TaskDetailRequiresAuthenticationAndReturnsSanitizedRequest)"` | 通过 |

## 6. 人工后续确认

- 真实 Node `DeleteArtifacts` 行为需在正式节点或联调节点确认，当前本地测试覆盖了调用顺序、错误保留和 DB/本地文件一致性。
- 正式环境升级前需备份 SQLite 和数据库同级 `temp-inputs` 目录。
