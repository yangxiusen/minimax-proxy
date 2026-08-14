# 管理后台与动态节点开发任务

> 本任务中涉及模型节点 Key ID、scope 和复合 Token 的未完成项已由 `006-node-single-key-conf/task.md` 取代，不得继续按旧凭据方案实施。

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
| MN-013 | 修复节点凭据模型和认证头 | Critical | Pending | MN-012 | 是 |
| MN-014 | 修复 Legacy 节点显式升级与保存校验 | High | Pending | MN-013 | 是 |
| MN-015 | 对齐 Node 错误包、授权能力与 OpenAPI 契约 | High | Pending | MN-013 | 是 |
| MN-016 | 增加 v9 H3 attempt 取消状态迁移 | High | Pending | MN-013 | 是 |
| MN-017 | 实现 H3 执行取消闭环 | High | Pending | MN-015、MN-016 | 是 |
| MN-018 | 实现提交/轮询不确定结果恢复 | High | Pending | MN-015、MN-016 | 是 |
| MN-019 | 修复 Profile 测试取消并移除无效 CFG | Medium | Pending | MN-013、MN-015 | 是 |
| MN-020 | 增加跨项目契约与故障回归测试 | High | Pending | MN-014 至 MN-019 | 是 |
| MN-021 | 全量验证、审查和真实节点联调准备 | High | Pending | MN-020 | 部分 |
| MN-022 | 修复 Node 异步 execution 回收 | Critical | Completed | MN-021 | 是 |
| MN-023 | 将 8188 运行指标接入 Node health | High | Completed | MN-022 | 是 |
| MN-024 | 同步 Stage、父任务和节点监控状态 | Critical | Completed | MN-022、MN-023 | 是 |
| MN-025 | 正式配置修复与真实闭环验收 | Critical | Partially completed | MN-024 | 部分 |
| MN-026 | 修复整任务 FIFO 阶段领取 | Critical | Completed | MN-025 | 是 |
| MN-027 | 增加 Proxy 公开根地址与绝对签名 URL | High | Completed | MN-025 | 是 |
| MN-028 | 接通 Manager artifact 播放地址 | High | Completed | MN-027 | 是 |
| MN-029 | 实现管理后台视频播放弹窗 | Medium | Completed | MN-028 | 是 |
| MN-030 | 全量验证、代码审查和文档同步 | High | Completed | MN-026 至 MN-029 | 是 |

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

## 5. H3 Node API 修复实施步骤

### MN-013 修复节点凭据模型和认证头

文件：`internal/upstream/nodeapi/client.go`、`client_test.go`、Registry/Prober/Artifact/Cleanup/Profile 的客户端工厂与测试。

1. 先把客户端测试改为分别传 `KeyID=proxy` 和 `Secret=secret`，断言请求头为 `Bearer proxy.secret`；旧构造器应编译失败或测试失败。
2. 新增 `Credentials` 并让 `NewClient` 接收结构化凭据，在唯一请求边界拼接 Token。
3. 修改所有生产调用点，不允许上层预拼接，也不允许日志格式化 credentials。
4. 增加 Key ID/Secret 缺失、Token 不泄漏和所有 9 条消费方法认证头一致的测试。

完成判据：用分离凭据启动的 Proxy 可通过 Node 认证；代码搜索不存在把解密 Secret 直接作为完整 Bearer Token 的调用。

### MN-014 修复 Legacy 节点升级与保存校验

文件：`internal/config/model_node.go`、`internal/domain/model_node.go`、`internal/httpapi/manager/nodes.go`、Manager 页面与测试。

1. 写复现测试：Legacy 节点 PUT H3 且省略 Secret，预期 400 `node_api_key_required`，数据库记录不变且没有内部错误日志。
2. 为 Node Key ID 建立独立 1-64 位无点号校验；增加点号、65 位、空 ID 表驱动测试。
3. 增加 `upgrade_protocol` 请求字段和协议状态迁移校验，禁止隐式升级、禁止降级。
4. H3 空 Secret 复用前校验当前密文/Nonce/指纹完整，Key ID 改变时强制新 Secret。
5. 页面显示真实 Legacy 选项和待升级状态，普通编辑只允许启停，升级使用独立动作并保留字段错误。

完成判据：用户当前数据库中的 Legacy 节点保存不再触发 500；只有显式提供完整凭据才能升级 H3。

### MN-015 对齐 Node 错误包、授权能力与 OpenAPI 契约

Node 文件：`h3_service/app.py`、`errors.py`、`routes/capabilities.py` 及测试。Proxy 文件：`internal/upstream/nodeapi/types.go`、`client.go`、Prober 与测试。

1. Node 先增加非法 Pydantic 请求的失败测试，断言默认 422 不符合统一错误包。
2. 注册 `RequestValidationError` handler，返回 `request_validation_failed/retryable=false/request_id/details`。
3. capabilities 基于认证 Principal 返回当前 Key ID 和排序后的 scopes；增加最小权限和完整权限测试。
4. Proxy `HTTPError` 保留 retryable/request ID，Prober 使用类型判断 401/403，不再解析错误字符串。
5. 从 FastAPI OpenAPI 生成归一化契约快照，并让 Proxy 校验 9 条消费路径和关键 schema；保留 12 路由摘要烟测。

完成判据：认证、参数校验和业务错误结构统一，Manager 可在不执行写操作的前提下验证完整权限。

### MN-016 增加 v9 H3 attempt 取消状态迁移

文件：`migrations/009_h3_stage_attempt_cancellation.sql`、`migrations/embed.go`、SQLite 迁移和 Stage Store 测试。

1. 写空库、v8 升级、已有 unknown attempt、索引/外键保持测试，先确认缺少 v9 时失败。
2. 按 `DATABASE_DELTA.md` 重建 `stage_attempts`，加入 cancelling/cancelled 状态。
3. 注册 v9，并将 `005-api-key-management` 的后续实施保持为 v10。
4. 增加 `MarkStageUnknown/MarkStageCancelling/CompleteStageCancelled` 的事务、幂等和并发测试。

完成判据：迁移不丢行、不破坏外键，取消终态与 callback 在同一事务只产生一次。

### MN-017 实现 H3 执行取消闭环

文件：`internal/orchestrator/orchestrator.go`、Stage Store、Manager/V2 取消相关接口测试、Profile Test Executor。

1. 扩展 Orchestrator NodeClient 接口包含 `CancelExecution`，写 running/unknown attempt 收到取消请求的失败测试。
2. 使用确定性 `cancel:<attempt_id>` 调用 Node，202 后继续查询原 execution。
3. 分别覆盖 Node 返回 cancelled、先 succeeded、failed、重复取消和进程重启恢复。
4. 确保 task/stage/attempt/callback 原子收口，节点仍活动时不释放给新任务。

完成判据：取消后不会继续对外成功，也不会留下远端运行；终态竞争忠实反映 Node 结果。

### MN-018 实现提交/轮询不确定结果恢复

文件：Orchestrator、Stage Store、Node Client 分类和测试。

1. 模拟 Node 已接受但 Proxy 在收到响应前断线，断言恢复仍使用相同 operation 和 attempt。
2. 模拟连续 Get 失败后恢复，断言始终查询相同 execution，不增加 attempt count。
3. 确定性 4xx 终止；429、retryable 和 5xx 进入带上限退避的 unknown 恢复。
4. 超过恢复预算后使用稳定错误码失败，错误日志不含 Node details、请求体和凭据。

完成判据：不确定网络故障不会重复生成，明确业务错误不会无意义重试。

### MN-019 修复 Profile 测试取消并移除无效 CFG

文件：`internal/profile`、Manager Profile API/Web 与相关配置测试。

1. Profile 测试超时/取消时，对已绑定 execution best-effort 调用 Cancel 并有限轮询。
2. 从新 Profile DTO、页面、校验、默认值和冻结流程移除 CFG。
3. 历史 config JSON 含 CFG 时可读取，但发布新版本时规范化删除；增加兼容测试。

完成判据：测试作业不在超时后遗留运行，页面不再承诺 Node 不支持的 CFG 能力。

### MN-020 增加跨项目契约与故障回归测试

1. Node 运行鉴权、统一错误、OpenAPI、执行取消、制品和清理测试。
2. Proxy 运行 Manager 保存回归、9 路由认证头、取消、未知态恢复、Profile 和清理测试。
3. 增加一个本地进程级联调：用真实 Argon2id 配置启动 Node，Proxy 使用分离 Key ID/Secret 完成 health、create、query、cancel 和 artifact 流程。
4. 验证 Node 发布 12 路由、Proxy 有意消费 9 路由，维护 3 路由没有被错误接入。

完成判据：本审计 H3-A01 至 H3-A11 每项都有代码修复和自动化证据。

### MN-021 全量验证、审查和真实节点联调准备

1. Proxy 执行 `gofmt`、`go test ./... -count=1`、关键故障测试重复、`go vet ./...` 和双入口构建。
2. Node 执行 H3 Service 全量 pytest，并验证归一化 OpenAPI 无未评审漂移。
3. 审查凭据泄漏、SQLite 状态原子性、取消竞争、幂等 operation 和响应向前兼容。
4. 更新 README 的 Key ID/Secret 来源、必需 scopes、Legacy 升级步骤与回滚限制。
5. 准备真实节点认证、生成、取消和断网演练；未执行前在验收文档保持待人工确认。

完成判据：自动化与构建通过，阻塞审查问题清零；真实 GPU 环境结果不伪报完成。

## 6. 修订完成标准

- [ ] 分离 Key ID/Secret 能正确生成 Node Bearer Token。
- [ ] Legacy 节点普通保存不再 500，显式升级具备稳定校验。
- [ ] Node 统一错误包和授权 scope 可被 Proxy 正确解释。
- [ ] H3 取消、提交未知和轮询未知可跨重启闭环且不重复生成。
- [ ] CFG 从 H3 可编辑能力中移除，历史数据可兼容读取。
- [ ] 跨项目契约、全量自动化、构建和代码审查通过。
- [ ] 真实 Node 联调和发布回滚检查保持人工确认。

## 7. 真实节点完成回收与监控闭环修复

### MN-022 修复 Node 异步 execution 回收（Completed）

Node 文件：`h3_service/routes/executions.py`、`h3_service/services/execution_service.py` 及测试。

1. 先增加真实 ASGI 异步查询回归测试：8188 history 已完成时，查询 execution 不得在事件循环内调用 `asyncio.run()`。
2. 将 execution 查询与 reconcile 改为异步调用链；同步 UI 调用保留明确的同步适配入口。
3. 已处于 `unknown` 的 execution 必须能从相同 `comfy_job_id` 恢复为 succeeded/failed，不创建新 execution。
4. 成功时注册不可变 artifact，失败时保留稳定错误码，均释放输入 artifact 锁。

完成判据：真实 FastAPI 查询可把已完成的 8188 作业收口为 `succeeded` 并返回 `result_artifact_id`。

### MN-023 将 8188 运行指标接入 Node health（Completed）

Node 文件：`h3_service/routes/health.py`、8188 运行时客户端与测试。Proxy 文件：`internal/upstream/nodeapi/types.go`、Registry 与监控测试。

1. Node 从 `/system_stats` 读取内存/显存容量，从 `/queue` 读取 running/pending 数量。
2. `/internal/v1/health` 增加可选 `runtime`；上游不可用时组件状态必须反映失败，不伪报 `starting/unknown`。
3. Proxy 解析 `runtime`，计算内存/显存使用率并写入 `PrivateQueue/MemoryPercent/VRAMPercent`；缺失字段保持 `null`。
4. CPU/GPU 实时利用率仅在数据源真实提供时展示，不用容量指标冒充利用率。

完成判据：Manager 可显示真实私有队列、内存和显存；8188 不可达时节点不可调度。

### MN-024 同步 Stage、父任务和节点监控状态（Completed）

文件：`internal/store/sqlite/stage_store.go`、Orchestrator、Registry/Monitor 及测试。

1. Stage 绑定 execution 时在同一事务将父任务更新为 `running`，写入 `upstream_id`、`active_stage_id` 和 `started_at`。
2. Stage 成功、失败、重试和取消时原子更新父任务、active stage、artifact 和 callback。
3. Node API 探测根据数据库活动 Stage 恢复 `runtime/current_task`，不得每次无条件覆盖为 idle。
4. 增加重启恢复、完成竞争、页面快照和对外单任务/列表状态回归测试。

完成判据：节点实际运行时 Proxy 单任务、列表和 Manager 均显示 running + 节点 ID；完成后显示 succeeded 和可下载 URL。

### MN-025 正式配置修复与真实闭环验收（Partially completed）

1. 备份正式 SQLite 后，将 H3 `service_url` 从 `http://host/ui` 修正为 `http://host`；管理 API 拒绝 H3 URL 的 `/ui` 路径。
2. 停止重复测试 Proxy，只保留目标正式实例，确认数据库迁移到当前版本。
3. 真实执行 `Proxy create -> Node execution -> 8188 -> Node artifact -> Proxy succeeded -> file download`。
4. 使用本地 callback 接收器验证 queued/running/succeeded；网络失败重试仍由现有 Worker 覆盖。

完成判据：日志不再出现 `/ui/internal/v1/health`，真实任务的单查、列表、下载和成功 callback 全部形成证据；发布审批仍待人工确认。

执行证据（2026-08-13）：

- 正式数据库升级前已生成一致性备份 `data/minimax-before-004-node-api-fix-20260813-175226.db`，`integrity_check=ok`；当前数据库 `user_version=12`。
- 正式节点 `service_url` 已修正为 `http://127.0.0.1:7860`，配置层拒绝 `/ui`；正式日志 `/ui/internal/v1/health` 命中数为 0。
- 真实任务 `693980501134541381` 完成 `Proxy create -> Node exe_f4e71082a6444338aa5102492e2d192f -> 8188 -> Node artifact -> Proxy succeeded`。
- 对外单查和列表均返回该任务 `succeeded`；签名文件下载 HTTP 200、`video/mp4`、335495 bytes，SHA-256 `e3b2f170fdad487b6a0363225e07ee1db840787fea867ad677c3ec2bc185efbc` 与数据库一致。
- Manager 快照为 `healthy/idle`、队列 0，CPU/GPU/内存/显存均为真实采样，并显示最新成功任务 `693980501134541381`。
- callback 创建、状态事件 `queued -> running -> succeeded`、签名、成功投递和网络/429/5xx 重试均由自动化覆盖。真实外部 callback 地址未提供，且安全策略有意拒绝本机/私网地址，因此公网投递仍保留为发布门禁。
- 重复测试 Proxy `18080` 已关闭；正式 Proxy `18081` 与 Node `7860/8188` 保持运行。

## 8. FIFO、播放与绝对 URL 开发任务

| 任务ID | 任务名称 | 优先级 | 状态 | 依赖任务 | AI可完成 | 说明 |
| --- | --- | --- | --- | --- | --- | --- |
| MN-026 | 修复整任务 FIFO 阶段领取 | Critical | Completed | MN-025 | 是 | 父任务 queue_seq 优先，保留节点可执行性和恢复约束 |
| MN-027 | 增加 Proxy 公开根地址与绝对签名 URL | High | Completed | MN-025 | 是 | 新增 server.public_base_url，单查/列表返回绝对 URL |
| MN-028 | 接通 Manager artifact 播放地址 | High | Completed | MN-027 | 是 | 管理摘要读取 artifact ID并签发 video_url |
| MN-029 | 实现管理后台视频播放弹窗 | Medium | Completed | MN-028 | 是 | 播放、加载错误、关闭资源释放、移动端布局 |
| MN-030 | 全量验证、代码审查和文档同步 | High | Completed | MN-026 至 MN-029 | 是 | 自动化、静态检查、构建、审查和运行配置同步 |

### MN-026 修复整任务 FIFO 阶段领取

文件：`internal/store/sqlite/stage_store.go`、`internal/store/sqlite/stage_store_test.go`。

1. 先写回归测试：创建早任务的 generation+restoration 和后任务 generation，完成早任务 generation 后断言 `ClaimStage` 返回早任务 restoration；运行测试确认旧 SQL 失败。
2. 增加同任务前序阻塞、两节点并发、preferred node 不匹配、retry time 未到和恢复阶段 current node 限制测试。
3. 候选 SQL 关联 `video_tasks`，保留所有现有可领取条件，将排序改为 `video_tasks.queue_seq, task_stages.stage_order, task_stages.id`。
4. 保持 immediate 事务和条件 UPDATE 不变，重复运行竞争测试并使用 `EXPLAIN QUERY PLAN` 记录查询计划。

完成判据：单节点不再出现后任务插入前任务阶段链；多节点可并行且同一阶段只有一个领取者。

### MN-027 增加 Proxy 公开根地址与绝对签名 URL

文件：`internal/config/config.go`、`config_test.go`、`internal/artifact/service.go`、`service_test.go`、`cmd/server/main.go`、`main_test.go`、`config.example.yaml`、`config.docker.yaml`、`config.yaml`、`README.md`。

1. 先写配置表驱动测试，覆盖 HTTP/HTTPS 根地址、尾斜杠规范化及 userinfo/query/fragment/子路径/缺 host 拒绝。
2. 为 `ServerConfig` 增加 `PublicBaseURL`，从 `server.public_base_url` 解析；缺失时明确返回配置错误。
3. 启动装配把 Artifact Service `URLPrefix` 设置为 `{public_base_url}/v2/files`，签名服务测试断言返回绝对 URL。
4. V2 单查/列表测试断言 URL 以配置地址开头，恶意 Host 和 Forwarded 头不能改变结果。
5. 同步示例、Docker 和正式配置；正式本机实例使用 `http://127.0.0.1:18081`。

完成判据：示例 JSON 中的 URL 可直接 GET；代码不存在将 `/v2/files` 拼到 Node `service_url` 的路径。

### MN-028 接通 Manager artifact 播放地址

文件：`internal/domain/task.go`、`internal/store/sqlite/store.go`、`admin_store_test.go`、`internal/httpapi/manager/handler.go`、`handler_test.go`、`cmd/server/main.go`。

1. 先写 Store 测试，断言 H3 成功任务摘要包含 `result_artifact_id`，API 响应不直接暴露该字段。
2. 扩展 `AdminTaskSummary/adminTaskSelect/scanAdminTaskSummary`，保持现有筛选和分页形状。
3. Manager Handler 注入 `ArtifactURLSigner`；artifact 成功任务签发 `video_url`，历史 `result_public_url` 保持兼容，非成功任务为 null。
4. 增加签名失败、空 artifact、历史任务和敏感字段不泄漏测试。

完成判据：真实 H3 artifact 成功任务的 Manager 列表出现可访问 `video_url`。

### MN-029 实现管理后台视频播放弹窗

文件：`internal/httpapi/manager/web/manager.html`、`manager.js`、`styles.css`、`internal/httpapi/manager/handler_test.go`。

1. 先扩展页面契约测试，要求任务播放 dialog、原生 video controls、关闭清理和受控错误文案存在。
2. 新增视频播放 dialog；点击现有“播放”按钮设置任务标题和 video src，不再打开不受控新窗口。
3. 处理 loading/canplay/error；关闭时 pause、移除 src、load 并清空状态。
4. 保证 5 秒任务轮询不修改打开播放器；补齐桌面/480px 移动端尺寸和无溢出样式。
5. 运行 `node --check`，用 Playwright 检查成功任务按钮、弹窗、关闭和移动端布局；真实视频播放保留人工验收。

完成判据：已完成任务可在后台弹窗播放，关闭后不继续下载/播放且页面无布局回归。

### MN-030 全量验证、代码审查和文档同步

1. 运行 TD-001 至 TD-016 对应定向测试、`go test ./... -count=1`、`go vet ./...`、双入口构建和 JS 语法检查。
2. 审查 FIFO 并发原子性、节点可执行过滤、URL 来源信任、签名所有权、Node 地址泄漏和播放器资源释放。
3. 使用独立本地验收配置启动 Proxy，验证公开地址、节点状态，并用测试 artifact 检查单查、列表、Manager video_url 和 Range 下载；正式实例重启与数据验证保留人工执行。
4. 同步 CHANGE/PRD/TECH/API/DATABASE/PROTOTYPE/TEST/task、README 与配置说明；真实外网、多节点和业务播放验收保持人工确认。

完成判据：自动化与构建通过、阻塞审查问题清零、本地验收实例使用绝对 Proxy URL；正式实例与真实外网验收不伪报完成。

执行证据（2026-08-13）：

- FIFO 定向测试先复现旧 SQL 越过早任务后序阶段，再改为按父任务 `queue_seq`、阶段 `stage_order` 领取；新增取消/终态/软删除父任务门禁、前序阻塞、重试到期、两节点并发以及 running/reconciling 原节点恢复测试。`go test ./internal/store/sqlite -run 'TestClaimStage' -count=20` 通过。
- `EXPLAIN QUERY PLAN` 使用 `idx_stages_claim`、`idx_stages_task` 和 `video_tasks(task_id)` 主键；当前任务上限 100 下保留临时排序，不新增索引。
- 独立本地实例 `127.0.0.1:18082` 验证 V2 单查、列表和 Manager 均返回绝对 Proxy 签名 URL；恶意 Host/Forwarded 头不改变前缀，Manager 不暴露 artifact ID 或 Node URL。
- Playwright 验证桌面和 480px 播放弹窗、原生 controls、5 秒轮询不干扰、错误状态以及关闭后 pause/remove src/load；截图保存在 `output/playwright/mn029-player-desktop.png` 和 `mn029-player-mobile.png`。
- 两轮只读代码审查提出的父任务终态领取、历史 URL 直出、查询计划缺口和非规范 IPv4 绕过均已修复；合法历史 URL 仅兼容 HTTP(S) 绝对地址，拒绝私网/本机 IP、localhost/.local 和数值 IPv4 变体。
- `go test ./... -count=1`、`go vet ./...`、`go build ./cmd/server ./cmd/healthcheck`、`node --check internal/httpapi/manager/web/manager.js` 和 `git diff --check` 全部通过。`go test -race` 已尝试，但本机没有 `gcc`，无法执行竞态检测。
- 正式 `18081` 实例未在本任务中重启；真实外网可达性、真实多节点 GPU 吞吐和大视频拖动仍按验收文档保留人工确认。
