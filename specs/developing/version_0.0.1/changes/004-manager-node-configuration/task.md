# 管理后台与动态节点开发任务

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 版本/变更 | `004-manager-node-configuration` |
| 任务范围 | 配置迁移、SQLite、节点运行时、调度/监控、Manager API 和 Web |
| 输入文档 | `CHANGE_SPEC.md`、`PRD_DELTA.md`、`TECH_SOLUTION.md`、`DATABASE_DELTA.md`、`API_DELTA.md`、`PROTOTYPE_DELTA.md` |
| 执行方式 | 使用 `s-develop`，按测试驱动和依赖顺序实施 |

## 2. 任务总览

| 任务ID | 任务名称 | 优先级 | 状态 | 依赖 | AI可完成 |
| --- | --- | --- | --- | --- | --- |
| MN-001 | 复核设计和建立失败测试基线 | High | Completed | - | 是 |
| MN-002 | 增加 v4 节点配置数据库迁移 | High | Completed | MN-001 | 是 |
| MN-003 | 实现节点 Store 与一次性 YAML 导入 | High | Completed | MN-002 | 是 |
| MN-004 | 重构配置加载为旧节点延迟解析 | High | Completed | MN-003 | 是 |
| MN-005 | 实现动态 Scheduler 与 Monitor 运行单元 | High | Completed | MN-003 | 是 |
| MN-006 | 实现节点 Registry 周期协调 | High | Completed | MN-004、MN-005 | 是 |
| MN-007 | 将管理接口与会话迁移到 `/manager` | High | Completed | MN-001 | 是 |
| MN-008 | 实现节点配置和连接测试 API | High | Completed | MN-003、MN-006、MN-007 | 是 |
| MN-009 | 实现管理后台配置弹窗与节点状态 | High | Completed | MN-008 | 是 |
| MN-010 | 重构服务装配和启动导入顺序 | High | Completed | MN-004、MN-006、MN-008 | 是 |
| MN-011 | 补齐竞态、恢复、兼容和页面回归测试 | High | Completed | MN-009、MN-010 | 是 |
| MN-012 | 全量验证、代码审查与文档同步 | High | Completed | MN-011 | 是 |

## 3. 实施步骤

### MN-001 复核设计和建立失败测试基线

文件：

- 修改 `internal/config/config_test.go`
- 修改 `cmd/server/main_test.go`
- 修改 `internal/httpapi/monitor/handler_test.go`，后续随包迁移改名

步骤：

1. 记录当前 `go test ./... -count=1` 基线。
2. 增加“零 upstream 配置可加载”“`/manager` 路径存在”“节点表迁移到 version 4”的最小失败测试。
3. 运行对应包测试，确认失败原因正是尚未实现本变更，不修改生产代码使其假通过。

完成判据：失败测试覆盖配置、路由和数据库三条主链路。

### MN-002 增加 v4 节点配置数据库迁移

文件：

- 新增 `migrations/004_model_service_nodes.sql`
- 修改 `migrations/embed.go`
- 修改 `internal/store/sqlite/store.go`
- 修改 `internal/store/sqlite/store_test.go`

步骤：

1. 先写从 v3 升级和空数据库初始化测试，断言两张表、约束、索引和 migration version。
2. 按 `DATABASE_DELTA.md` 创建 `model_service_nodes` 与 `node_config_bootstrap`。
3. 将 v4 纳入现有事务迁移流程，重复打开数据库必须幂等。
4. 运行 `go test ./internal/store/sqlite -run 'Migration|ModelNode' -count=1`。

完成判据：新旧数据库都升级成功，v1-v3 任务数据保持不变。

### MN-003 实现节点 Store 与一次性 YAML 导入

文件：

- 新增 `internal/domain/model_node.go`
- 新增 `internal/store/sqlite/model_node.go`
- 新增 `internal/store/sqlite/model_node_test.go`

步骤：

1. 为 List/Get/Create/Update/Delete、版本冲突、ID 永久保留和活动任务限制分别写失败测试。
2. 实现节点 DTO 与 Store 方法；所有修改使用事务和条件更新。
3. 为“导入成功、非法输入前不写入、零节点标记、标记幂等、表非空不合并”写失败测试。
4. 实现 `BootstrapState` 和 `ImportLegacyNodes`，再次在事务内检查标记和表状态。
5. 扩展 `ClaimNext` 接收节点配置版本，并在领取事务校验节点未删除、启用且版本匹配；增加 Claim 与更新/停用/删除并发测试。
6. 增加并发更新/删除测试，确保同版本只有一个写入者。
7. 运行 `go test ./internal/store/sqlite -run 'ModelNode|LegacyNodeImport|Claim' -count=20`。

完成判据：节点真相和导入幂等均由 SQLite 原子约束保护。

### MN-004 重构配置加载为旧节点延迟解析

文件：

- 修改 `internal/config/config.go`
- 修改 `internal/config/config_test.go`
- 修改 `config.example.yaml`
- 修改 `config.docker.yaml`

步骤：

1. 先测试常规配置没有 upstream 也能加载，并保留旧节点原始值。
2. 把 URL 和时长转换提取为仅首次导入调用的解析函数；常规 `validate` 不再要求节点。
3. 测试导入待执行时非法旧节点返回原有明确错误，导入完成路径不解析旧节点。
4. 从示例配置移除日常 `upstreams`，增加升级导入说明但不要求新安装填写。
5. 运行 `go test ./internal/config -count=1`。

完成判据：数据库决定是否解析旧节点，导入完成后 YAML 节点变化不影响启动。

### MN-005 实现动态 Scheduler 与 Monitor 运行单元

文件：

- 修改 `internal/scheduler/scheduler.go`
- 修改 `internal/scheduler/scheduler_test.go`
- 修改 `internal/monitor/cache.go`
- 修改 `internal/monitor/cache_test.go`
- 修改 `internal/monitor/collector.go`
- 修改 `internal/monitor/collector_test.go`

步骤：

1. 为运行中 Add/Replace/Remove、重复注册幂等、取消等待和单节点唯一循环写失败测试。
2. 将 Scheduler 从固定切片改为线程安全动态槽生命周期，槽携带节点配置版本并保持现有 `Wake` 语义。
3. 为 Cache 增加 `Enabled`、`Applying` 和 `Delete`；可用性只统计启用健康节点。
4. 将 Collector 拆出可由 Registry 启停的单节点运行入口，并保留 Gate 串行约束。
5. 运行 `go test ./internal/scheduler ./internal/monitor -count=20`，环境可用时增加 `-race`。

完成判据：任意 Reconcile 重复和并发调用都不会为一个节点留下两个活动槽或采集器。

### MN-006 实现节点 Registry 周期协调

文件：

- 新增 `internal/upstream/registry/registry.go`
- 新增 `internal/upstream/registry/registry_test.go`
- 按需要新增同包小型 factory 文件，禁止把业务逻辑堆入 `cmd/server`

步骤：

1. 使用 fake Store、Scheduler 和 Collector 写新增、版本替换、停用、启用、删除和通知丢失测试。
2. 实现按 `id/version/deleted_at` 对账的 Registry，提供 `Run(ctx)` 与非阻塞 `Wake()`。
3. 实现停用排空：有活动任务时保留恢复槽，无活动任务时健康门拒绝 Claim；采集继续运行。
4. 配置替换时先取消并等待旧运行单元，再重置缓存并启动新单元。
5. 协调失败只记录稳定错误并在下周期重试，不记录 URL。
6. 运行 `go test ./internal/upstream/registry -count=20`，环境可用时增加 `-race`。

完成判据：DB 写成功后即使 Wake 丢失也能最终应用，停用节点任务不会因重启失去 Worker。

### MN-007 将管理接口与会话迁移到 `/manager`

文件：

- 移动 `internal/httpapi/monitor/` 到 `internal/httpapi/manager/`
- 修改迁移后 `handler.go`、`web.go`、嵌入资源和对应测试
- 修改 `cmd/server/main.go` 与 `cmd/server/main_test.go`

步骤：

1. 先把现有路由测试复制为 `/manager` 预期，并增加三个旧 GET 路径 308 测试。
2. 重命名包、路径、页面标题、资源引用和 JS 请求前缀。
3. Cookie 改为 `manager_session` 且 Path 为 `/manager`；测试 HttpOnly、SameSite、Secure 和退出过期行为。
4. 明确测试旧 `/monitor/api/*` 不暴露管理写接口。
5. 运行 `go test ./internal/httpapi/manager ./cmd/server -count=1`。

完成判据：所有现有任务管理行为在新路径无回归，旧页面地址只做安全 GET 跳转。

### MN-008 实现节点配置和连接测试 API

文件：

- 新增 `internal/httpapi/manager/nodes.go`
- 新增 `internal/httpapi/manager/nodes_test.go`
- 修改 `internal/httpapi/manager/handler.go`

步骤：

1. 为 5 个接口的鉴权、成功、严格 JSON、字段边界和稳定错误映射写失败测试。
2. 实现列表、创建、全量更新和带版本软删除；成功写入后调用 Registry Wake。
3. 实现草稿测试接口，并行探测 Gradio/Jobs，限制总超时和响应体，不写 DB/缓存。
4. 捕获日志验证不包含完整 URL、请求体和上游响应。
5. 运行 `go test ./internal/httpapi/manager -run 'Node|Session|Task' -count=1`。

完成判据：接口与 `api-modules/manager-nodes.md` 一致，所有冲突有稳定错误码。

### MN-009 实现管理后台配置弹窗与节点状态

文件：

- 修改 `internal/httpapi/manager/web/monitor.html`，并重命名为 `manager.html`
- 修改 `internal/httpapi/manager/web/monitor.js`，并重命名为 `manager.js`
- 修改 `internal/httpapi/manager/web/login.html`
- 修改 `internal/httpapi/manager/web/login.js`
- 修改 `internal/httpapi/manager/web/styles.css`
- 修改 `internal/httpapi/manager/handler_test.go`

步骤：

1. 先增加嵌入资源测试，断言新路径、配置按钮、弹窗字段、确认和 busy guard 存在。
2. 实现节点列表、表单默认值、字段错误、版本提交和测试连接反馈。
3. 实现未保存修改确认、停用说明、删除二次确认和冲突后重新加载。
4. 扩展节点快照展示 `enabled/applying`，补齐空、加载、失败、停用和未知状态。
5. 检查桌面及窄屏固定尺寸、横向表格和弹窗滚动，避免文本和操作重叠。
6. 运行 Manager 包测试；启动本地服务后使用 Playwright 截图检查桌面和移动视口。

完成判据：原任务操作无回归，节点配置完整可用且所有危险动作有明确确认。

### MN-010 重构服务装配和启动导入顺序

文件：

- 修改 `cmd/server/main.go`
- 修改 `cmd/server/main_test.go`
- 按需要新增 `cmd/server/bootstrap_test.go`

步骤：

1. 先写旧 YAML 导入、导入后忽略、零节点启动和停用活动任务恢复的装配测试。
2. 按“加载常规配置 -> 打开/迁移 DB -> 判断并执行旧节点导入 -> 启动 Registry -> 启动 HTTP”重构。
3. 移除 `main.go` 中固定 slots/collectorNodes 构建，把依赖工厂交给 Registry。
4. V2 availability 改为只统计启用、健康且新鲜的动态快照。
5. 启动日志只输出节点数量和阶段，不输出地址；关闭时等待 Registry 收口。
6. 运行 `go test ./cmd/server -count=1`。

完成判据：服务可在零节点启动，节点数据库化后无需重启即可加入调度。

### MN-011 补齐竞态、恢复、兼容和页面回归测试

文件：覆盖上述修改包的 `*_test.go`，不创建依赖真实服务的自动化测试。

步骤：

1. 增加更新与 Claim、停用与完成、删除与恢复的并发测试。
2. 增加节点配置重载期间旧健康快照不被复用的回归测试。
3. 增加软删除节点历史任务仍能列出的回归测试。
4. 增加现有任务中止、删除、视频播放和状态文案页面资源回归测试。
5. 运行 `go test ./... -count=1` 和关键包 `-count=20`。

完成判据：`TEST_ACCEPTANCE.md` 中所有“无需人工确认”的用例都有自动化覆盖或明确对应测试。

### MN-012 全量验证、代码审查与文档同步

文件：所有变更文件、`README.md`、配置示例和本变更文档。

步骤：

1. 对全部修改 Go 文件运行 `gofmt`。
2. 运行 `go test ./... -count=1`、`go vet ./...`、`go build ./cmd/server ./cmd/healthcheck`。
3. 环境具备 CGO 和编译器时运行 `go test -race ./...`；否则在测试文档记录环境限制。
4. 执行代码审查，重点检查事务边界、协程退出、槽唯一性、敏感日志和 URL 校验，修复所有阻塞问题。
5. 同步 README 的 `/manager` 使用方式、首次导入和回滚说明；回写任务状态与自动化证据。
6. 真实节点和 Docker 联调仍只留在 `TEST_ACCEPTANCE.md`，不得标记为已完成。

完成判据：自动化、静态检查和构建通过，代码审查无未处理阻塞问题，文档与实现一致。

## 4. 完成标准

- [x] v4 迁移和一次性 YAML 导入原子、幂等且可测试。
- [x] SQLite 成为节点唯一真相源，零节点不阻止管理服务启动。
- [x] 动态节点增删改、停用排空和重启恢复不产生重复槽或假运行任务。
- [x] `/manager`、节点配置弹窗和 5 个节点接口满足设计契约。
- [x] 原任务查询、中止、删除、播放和公共 V2 行为无回归。
- [x] 全量测试、静态检查、构建与代码审查完成。
- [x] 人工联调项保持待人工确认，未被自动标记完成。
