# 对外 API Key 管理开发任务

> 开发执行要求：进入 `/s-develop` 后按 `test-driven-development` 逐项先写失败测试，再写最小实现；遇到异常使用 `systematic-debugging`；完成各阶段后使用 `requesting-code-review`。

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 版本/变更 | `005-api-key-management` |
| 目标 | 在 Manager 中管理数据库 Key，并通过共享内存快照鉴权 |
| 架构 | SQLite 真相源 + 单进程原子不可变快照 + 写后同步刷新 + 周期对账 |
| 技术栈 | Go、SQLite、标准库 HTTP/crypto/atomic、嵌入式 HTML/CSS/JS |
| 输入文档 | `CHANGE_SPEC.md`、`PRD_DELTA.md`、`PROTOTYPE_DELTA.md`、`TECH_SOLUTION.md`、`API_DELTA.md`、`DATABASE_DELTA.md` |

## 2. 文件映射

| 文件 | 操作 | 职责 |
| --- | --- | --- |
| `migrations/010_external_api_keys.sql` | 新增 | Key 表、导入标记与索引 |
| `migrations/embed.go` | 修改 | 嵌入 v10 SQL |
| `internal/domain/external_api_key.go` | 新增 | 实体、输入、筛选和稳定错误 |
| `internal/store/sqlite/external_api_key.go` | 新增 | CRUD、引用保护、导入和快照读取 |
| `internal/store/sqlite/external_api_key_test.go` | 新增 | Store 和事务回归 |
| `internal/store/sqlite/store.go` | 修改 | 注册 v10 迁移 |
| `internal/store/sqlite/store_test.go` | 修改 | 空库和升级迁移断言 |
| `internal/authkey/authenticator.go` | 新增 | Key 生成、摘要、原子快照和认证 |
| `internal/authkey/authenticator_test.go` | 新增 | 生成、空快照、并发和刷新测试 |
| `internal/authkey/service.go` | 新增 | 管理 CRUD 后同步刷新和周期对账 |
| `internal/authkey/service_test.go` | 新增 | 写后刷新异常和一次性值测试 |
| `internal/config/config.go` | 修改 | 旧 YAML Key 延迟导入，允许零 Key |
| `internal/config/config_test.go` | 修改 | 空配置、已导入忽略和旧值校验 |
| `cmd/server/main.go` | 修改 | 导入、共享 Authenticator 和对账装配 |
| `cmd/server/main_test.go` | 修改 | 启动导入顺序和 Handler 共享认证测试 |
| `internal/httpapi/manager/api_keys.go` | 新增 | 4 个 Manager API |
| `internal/httpapi/manager/api_keys_test.go` | 新增 | 严格 JSON、权限、错误映射和敏感信息测试 |
| `internal/httpapi/manager/handler.go` | 修改 | 注入服务和注册路由 |
| `internal/httpapi/manager/handler_test.go` | 修改 | 资源/路由回归 |
| `internal/httpapi/v2/handler.go` | 修改 | 使用共享 BearerAuthenticator |
| `internal/httpapi/v2/handler_test.go` | 修改 | 动态启停和空快照回归 |
| `internal/httpapi/v2/files.go` | 修改 | Bearer 下载使用共享认证器 |
| `internal/httpapi/v2/files_test.go` | 修改 | 动态启停、签名 URL 不受影响 |
| `internal/httpapi/manager/web/manager.html` | 修改 | 顶栏入口和两个对话框 |
| `internal/httpapi/manager/web/manager.js` | 修改 | 列表、CRUD、复制和一次性清理 |
| `internal/httpapi/manager/web/styles.css` | 修改 | 紧凑列表、警告和 Key 展示样式 |
| `config.example.yaml`、`config.docker.yaml` | 修改 | 移除新部署 `api_keys` |
| `.env.docker.example` | 修改 | 移除外部 Key 环境变量 |
| `README.md` | 修改 | 后台创建、升级导入和回滚说明 |

## 3. 任务总览

| ID | 任务 | 状态 | 依赖 |
| --- | --- | --- | --- |
| AK-001 | 建立 v10 迁移失败测试并实现 DDL | Completed | - |
| AK-002 | 实现 Key Store、引用保护和 YAML 导入 | Completed | AK-001 |
| AK-003 | 实现共享 Authenticator 与管理 Service | Completed | AK-002 |
| AK-004 | 调整配置加载和启动导入装配 | Completed | AK-002、AK-003 |
| AK-005 | 实现 Manager Key API | Completed | AK-003 |
| AK-006 | 将 V2 与文件下载切换到共享鉴权 | Completed | AK-003 |
| AK-007 | 实现管理后台交互 | Completed | AK-005 |
| AK-008 | 安全清理配置与同步说明 | Completed | AK-004 |
| AK-009 | 并发、安全、回归测试与代码审查 | Completed（race 因 CGO_DISABLED 未执行） | AK-005 至 AK-008 |
| AK-010 | 文档同步和开发交接 | Completed（人工验收项已移交） | AK-009 |

## 4. 实施步骤

### AK-001 v10 数据库迁移

1. 在 `store_test.go` 增加空库和 v9 数据库升级测试，断言 `PRAGMA user_version=10`、两张新表、三个索引和字段约束；运行 `go test ./internal/store/sqlite -run 'Migration|ExternalAPIKeySchema' -count=1`，确认因 v10 缺失失败。
2. 新增 `010_external_api_keys.sql`，严格采用 `DATABASE_DELTA.md` 的 DDL；在 `migrations/embed.go` 嵌入变量 `ExternalAPIKeys`。
3. 在 `Store.migrate` 将 `{version: 9, name: "对外 API Key", sql: migrations.ExternalAPIKeys}` 追加到 v8 后。
4. 重跑定向测试，预期通过；再运行 `go test ./internal/store/sqlite -run Migration -count=1`，确认旧迁移回归通过。

完成判据：迁移原子、幂等，新旧数据库数据不丢失。

### AK-002 Store、引用保护和首次导入

1. 新增 `external_api_key_test.go`，先覆盖：列表排序、创建、大小写名称冲突、摘要冲突、版本更新、未找到、版本冲突、无引用删除、任务引用冲突、幂等引用冲突。
2. 运行 `go test ./internal/store/sqlite -run ExternalAPIKey -count=1`，确认因方法缺失失败。
3. 在领域文件定义 `ExternalAPIKey`、`ExternalAPIKeyInput`、`ExternalAPIKeyCredential` 及 `ErrAPIKeyNotFound/NameConflict/VersionConflict/InUse`；在 Store 文件实现最小 CRUD。
4. 删除必须使用现有 `immediate` 事务，在同一事务查询两个引用表再删除，不先查后开事务。
5. 增加导入测试：合法全量、空输入、坏条目全回滚、大小写重复、摘要重复、已完成跳过、表已有记录时不混合、旧 ID 原样保留。
6. 实现 `APIKeyBootstrapState` 和 `ImportLegacyAPIKeys`；事务内二次检查标记和表状态，任何失败不写完成标记。
7. 运行 `go test ./internal/store/sqlite -run 'ExternalAPIKey|APIKeyBootstrap' -count=1`，预期通过。

完成判据：Store 不接触完整 Key 生成，导入明文不进入日志或返回对象。

### AK-003 共享 Authenticator 与管理 Service

1. 在 `authenticator_test.go` 先写失败测试：随机源失败、Key 格式/长度、摘要稳定、空快照拒绝、启用集合认证、Reload 原子替换、并发 Authenticate/Reload 无竞态。
2. 实现 `Generate(io.Reader)`、`Digest(string)`、`Authenticator.Authenticate` 和 `Authenticator.Reload`；快照使用 `atomic.Pointer`，不得原地修改 map。
3. 在 `service_test.go` 写失败测试：创建只返回一次完整 Key、名称规范化、CRUD 后同步 Reload、Reload 失败返回稳定错误、周期 `Run` 能从瞬时失败恢复。
4. 实现 `Service`，注入 Store、Authenticator、随机源和时钟。创建流程只在方法返回值短暂携带完整 Key；日志由上层只记录 ID。
5. 明确 DB 写成功但 Reload 失败返回 `ErrCacheRefresh`；周期对账每秒调用 Reload，context 取消后退出。
6. 运行 `go test ./internal/authkey -count=1` 与 `go test -race ./internal/authkey -count=1`；若主机 race 环境不可用，在验收记录原因，不静默跳过。

完成判据：所有 HTTP 消费者可共享一个认证器，读路径无锁且无 DB I/O。

### AK-004 配置与服务启动装配

1. 在 `config_test.go` 先增加：无 `api_keys` 可加载、旧 Key 仍原样保留供导入、常规校验不再要求启用 Key、未知字段仍失败。
2. 修改 `Config` 将 `APIKeys` 明确改为 `LegacyAPIKeys` 或等价兼容字段；移除 `validate` 中“至少一个启用 Key”的启动约束，保留旧值的延迟导入校验。
3. 在 `main_test.go` 增加：首次导入、空导入、完成后忽略坏 YAML、导入失败拒绝启动、V2 与 Files 注入同一认证器。
4. 修改 `main.go`：打开 DB 后检查 Key 导入标记，按需导入，再创建 Authenticator 并初始 Reload；初始 Reload 失败返回启动错误。
5. 将 `authkey.Service.Run(ctx,time.Second)` 作为后台对账启动并在根 context 取消时退出。
6. 运行 `go test ./internal/config ./cmd/server -count=1`。

完成判据：零 Key、零节点的新安装可进入 Manager，旧 Key 升级不中断。

### AK-005 Manager API

1. 在 `api_keys_test.go` 先覆盖 4 条路由的未登录、正常响应、严格 JSON、未知/重复字段、名称校验、版本校验及全部稳定错误码。
2. 增加专门测试，扫描响应和捕获日志，确保列表/更新/错误不出现摘要或完整 Key，创建响应之外不出现完整 Key。
3. 在 `handler.go` 的 `Dependencies` 注入最小 `APIKeyService` 接口，并调用 `registerAPIKeyRoutes`；避免让 Handler 直接访问 SQLite。
4. 在 `api_keys.go` 实现 DTO、严格解码、路径/查询校验和错误映射。创建返回 `201 + Location`；删除返回 `204`。
5. 对 `ErrCacheRefresh` 返回 `503 cache_refresh_failed`，并在日志只记录 ID、动作和稳定错误码。
6. 运行 `go test ./internal/httpapi/manager -run 'APIKey|ManagerWeb' -count=1`。

完成判据：接口与 `API_DELTA.md` 完全一致，管理会话边界不变。

### AK-006 V2 和文件下载共享鉴权

1. 修改 V2 测试，构造可变测试认证器，先验证同一 Handler 在停用后无需重建便从成功变为 `401`；零快照也返回 `401`。
2. 修改文件测试，验证 Bearer 所有权使用同一认证器动态变化，签名 URL 分支保持通过。
3. 将 `Dependencies.APIKeys []config.APIKeyConfig` 替换为最小 `BearerAuthenticator` 接口，删除两个 Handler 内部复制 `authKey` 的逻辑。
4. 保留严格 `Bearer ` Header 解析；将 token 传给认证器并把返回 ID放入 request context 或 Artifact Authorization。
5. 运行 `go test ./internal/httpapi/v2 -count=1`。

完成判据：任务接口与文件下载使用同一快照，公共契约无变化。

### AK-007 管理后台交互

1. 先在 `handler_test.go` 增加静态资源断言：顶栏入口、Key 对话框、一次性弹窗、4 条 API 路径、Clipboard 和 DOM 清理逻辑存在。
2. 在 `manager.html` 添加顶栏入口、密钥列表对话框、创建/重命名控件和一次性 Key 对话框；使用稳定尺寸和可滚动容器。
3. 在 `manager.js` 增加独立 `apiKeys` 状态、请求 generation/busy 防重入、列表渲染、CRUD、复制和关闭清理。所有动态文本用 `textContent`/`replaceChildren`。
4. 关闭一次性弹窗时把 DOM 文本、输入选区和 JS 中完整 Key 引用清空；不得使用 LocalStorage、SessionStorage 或 URL 参数。
5. 在 `styles.css` 沿用现有色板和组件，加入零启用警告、掩码等宽文本和移动端防溢出规则。
6. 运行 Manager 包测试，并用 Playwright 在 `1280x800`、`390x844` 检查无重叠、无溢出、创建弹窗和错误态可用。

完成判据：页面符合 `PROTOTYPE_DELTA.md`，不改变现有任务和节点工作流。

### AK-008 配置安全清理

1. 修改配置测试，使无 `api_keys` 的 `config.example.yaml` 与 Docker 配置可通过加载。
2. 从 `config.example.yaml`、`config.docker.yaml` 移除常规 `api_keys`，加入新安装从 Manager 创建、旧安装一次性导入的注释。
3. 从 `.env.docker.example` 移除 `MINIMAX_API_KEY_CUSTOMER_1`；保留管理员密码和 Proxy 主密钥。
4. 更新 `README.md` 的启动、凭据、升级和回滚说明，不再要求新安装预设外部 Key。
5. 实际 `.env.docker` 和 `config.yaml` 属于被忽略的本机部署文件：按 `TEST_ACCEPTANCE.md` 完成导入验证后，人工删除外部 Key及三个无引用旧上游 URL；开发任务不得在验证前破坏现有调用。
6. 使用 `rg -n 'MINIMAX_API_KEY_CUSTOMER|MINIMAX_(UPSTREAM|PUBLIC_UPSTREAM|UPSTREAM_JOBS)_URL|^api_keys:' README.md config.example.yaml config.docker.yaml .env.docker.example`，预期仅允许迁移说明中的文字引用，不再存在生效配置。

完成判据：新安装配置无外部 Key 明文入口，运行必需配置未被误删。

### AK-009 全量验证与代码审查

1. 运行 `gofmt -w` 格式化本变更 Go 文件。
2. 运行 `go test ./... -count=1`，预期全部通过。
3. 运行 `go test -race ./... -count=1`，重点验证 Authenticator 并发；记录主机 CGO/编译器限制。
4. 运行 `go vet ./...` 和 `go build ./cmd/server ./cmd/healthcheck`。
5. 启动本地服务，用 Manager API 创建 Key，验证 V2 `401 -> 成功 -> 停用后 401 -> 启用后成功`；模型生成是否成功不作为本地自动判据。
6. 使用 `requesting-code-review` 审查相对开发基线的变更，修复 Critical/Important 发现后重跑对应测试。
7. 检查 `git diff --check`、敏感值搜索和日志断言，确保无完整 Key 写入仓库、日志或数据库测试快照。

完成判据：自动化验证通过，代码审查无未处理的高优先级问题。

### AK-010 文档同步与交接

1. 将实现结果、验证命令和已知限制回填本 change 包。
2. 同步 `specs/developing/version_0.0.1/API_INTERFACE.md`、`DATABASE_DESIGN.md`、`TECHNICAL_SOLUTION.md` 中与外部 Key 来源冲突的描述。
3. 更新 `changes/README.md` 状态；人工验收未完成前不得标为 `Completed`。
4. 准备 `TEST_ACCEPTANCE.md` 中的真实升级、调用方换 Key、Docker 和回滚人工验证材料。

完成判据：代码、版本总文档、README 和实际行为一致。

## 5. Done Criteria

- [ ] 变更目标已实现且所有生产代码由失败测试驱动。
- [ ] 数据库、API、页面、配置和回滚文档已同步。
- [ ] 自动测试、race、vet、build 和页面检查完成或记录明确环境限制。
- [ ] 代码审查 Critical/Important 问题已修复。
- [ ] 未把真实升级、业务验收、性能验收或发布确认标记为 AI 已完成。
- [ ] 用户按 `TEST_ACCEPTANCE.md` 完成人工验收并确认。

## 6. 下游触发点

- `/s-develop`：严格对齐 `test-driven-development`、`systematic-debugging`、`requesting-code-review`。
- `/s-archive`：人工验收完成后，对齐 `verification-before-completion`、`finishing-a-development-branch`。

## 7. 开发执行记录

- 已实现 v10 迁移、SQLite CRUD 与引用保护、旧 YAML 一次性导入、共享原子鉴权快照、Manager API/UI、V2 与文件下载动态鉴权和配置模板清理。
- 已执行 `go test ./... -count=1`、`go vet ./...`、`go build ./cmd/server ./cmd/healthcheck`、`node --check internal/httpapi/manager/web/manager.js` 与 `git diff --check`。
- 本地运行验证完成：旧 Key 导入后返回 200；新建 Key 返回 200；停用后立即 401；重新启用后立即恢复 200；测试 Key 已删除。
- `go test -race` 因当前环境 `CGO_ENABLED=0` 无法执行，错误为 `-race requires cgo`。
- 真实部署备份、生产升级、回滚演练、业务验收与发布审批仍由人工按 `TEST_ACCEPTANCE.md` 确认。
