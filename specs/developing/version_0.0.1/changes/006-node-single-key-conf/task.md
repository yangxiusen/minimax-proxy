# Node 单 Key 与 conf.yml 开发任务

> 开发执行要求：本变更包经用户确认后使用 `/s-develop`。按 `test-driven-development` 先写失败测试再写最小实现；遇到异常使用 `systematic-debugging`；完成各阶段后使用 `requesting-code-review`。

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 版本/变更 | `006-node-single-key-conf` |
| 目标 | Node 以 `conf.yml` 管理一次生成的单 Key 和 WebUI 凭据，Proxy 节点配置只接收一个 Key |
| 跨仓库范围 | `E:/MiniMax-WorkFlow/Minimax-H3`、`E:/MiniMax-WorkFlow/MiniMax-H3-Proxy` |
| 输入文档 | 本目录 `CHANGE_SPEC.md`、`PRD_DELTA.md`、`PROTOTYPE_DELTA.md`、`TECH_SOLUTION.md`、`API_DELTA.md`、`DATABASE_DELTA.md` |

## 2. 任务总览

| ID | 任务 | 状态 | 依赖 |
| --- | --- | --- | --- |
| SK-001 | Preflight、影响分析与文档产物确认 | Completed | - |
| SK-002 | Node 凭据文件加载器 TDD | Completed | SK-001 |
| SK-003 | Node 单 Key API 与 WebUI 鉴权 TDD | Completed | SK-002 |
| SK-004 | Proxy 领域模型和 SQLite 兼容 TDD | Completed | SK-001 |
| SK-005 | Proxy Manager 单 Key API 与页面 | Completed | SK-004 |
| SK-006 | Proxy Node Client 全调用链回归 | Completed | SK-003、SK-005 |
| SK-007 | 配置安全清理与文档同步 | Completed | SK-003、SK-006 |
| SK-008 | 全量自测与静态检查 | Completed | SK-007 |
| SK-009 | 代码审查、修复与回归 | Completed | SK-008 |
| SK-010 | 变更文档回填和验收交接 | Completed | SK-009 |

## 3. 实施步骤

### SK-001 Preflight、影响分析与文档产物确认

1. 已读取项目导航、项目概览和架构设计，确认目标版本仅有 `version_0.0.1`。
2. 已扫描 developing 和 archive 的 change 编号，确定使用 `006-node-single-key-conf`。
3. 已确认涉及产品规则、页面字段、API 鉴权、数据库兼容语义和跨仓库技术方案，因此生成八份变更文档。
4. 已确认页面仅局部删除字段，不属于重大界面变化，不更新 `specs/product/<surface>`。

完成判据：本 change 包文档落盘并经用户确认后才进入开发。

### SK-002 Node 凭据文件加载器 TDD

涉及文件：

- 新增 `Minimax-H3/h3_service/credential_config.py`
- 新增 `Minimax-H3/tests/h3_service/test_credential_config.py`
- 修改 `Minimax-H3/start.py`
- 修改 `Minimax-H3/tests/h3_service/test_settings.py` 或相邻启动测试

步骤：

1. 先写失败测试覆盖：不存在时生成、Key 精确 32 位字母数字、用户名固定 admin、密码精确 8 位且含字母和数字、已有文件幂等读取、非法 YAML/缺失字段/额外字段/错误类型/非法值失败且不改文件、不同 CWD 仍按脚本目录定位、旧环境变量不生效、并发首次创建只产生一个赢家文件。
2. 运行：

   ```powershell
   walkingwithai\python.exe -m pytest tests\h3_service\test_credential_config.py tests\h3_service\test_settings.py -q
   ```

   预期先因加载器缺失而失败。
3. 实现不可变 `CredentialConfig`、安全随机生成、严格 YAML 解析和独占原子创建。禁止把凭据字段加入可被环境变量覆盖的 `ServiceSettings`。
4. `start.py` 使用 `Path(__file__).resolve().parent / "conf.yml"` 加载，并将凭据对象显式传给应用构造。
5. 重跑定向测试，预期全部通过；确认测试不打印真实随机值。

完成判据：首次创建并发安全，已存在文件永不被自动修改，旧凭据环境变量完全无效。

### SK-003 Node 单 Key API 与 WebUI 鉴权 TDD

涉及文件：

- `Minimax-H3/h3_service/auth.py`
- `Minimax-H3/h3_service/app.py`
- `Minimax-H3/tests/h3_service/test_auth.py`
- `Minimax-H3/tests/h3_service/test_ui_auth.py`
- `Minimax-H3/tests/h3_service/test_execution_api.py`
- `Minimax-H3/tests/h3_service/test_artifact_api.py`
- `Minimax-H3/tests/h3_service/test_cleanup_api.py`

步骤：

1. 先把鉴权测试改为共享 `CredentialConfig`，覆盖缺失 Header、错误 Scheme、错误 Key、旧复合 Token、正确单 Key以及全部受保护路由使用同一 Key。
2. WebUI 测试覆盖 `admin` + 配置密码成功、错误用户/密码失败以及常量时间比较边界。
3. 运行定向测试，确认旧 `APIKeyConfig`、scope 或复合 Token 断言导致失败。
4. 将 API 鉴权器收敛为一个 Key，严格解析 `Bearer ` 并使用常量时间比较；移除 Key ID、scope、Argon2 密码哈希配置和匿名旁路。
5. `create_app` 与 WebUI 鉴权构造显式接收同一个凭据对象；禁止从环境变量或全局单例重新读取。
6. 运行：

   ```powershell
   walkingwithai\python.exe -m pytest tests\h3_service\test_auth.py tests\h3_service\test_ui_auth.py tests\h3_service\test_execution_api.py tests\h3_service\test_artifact_api.py tests\h3_service\test_cleanup_api.py -q
   ```

完成判据：所有 Node 受保护路由只接受 `Bearer <key>`，不存在 scope 和复合 Token 活动逻辑。

### SK-004 Proxy 领域模型和 SQLite 兼容 TDD

涉及文件：

- `internal/domain/model_node.go`
- `internal/config/model_node.go`
- `internal/store/sqlite/model_node.go`
- `internal/store/sqlite/model_node_test.go`
- `internal/store/sqlite/migration_v7_test.go`

步骤：

1. 先写失败测试：创建/更新 H3 节点不含领域 `APIKeyID`；原始数据库列为非空空字符串；历史非空 Key ID 可读取但不暴露；Legacy 行约束不变。
2. 运行：

   ```powershell
   go test ./internal/config ./internal/store/sqlite -run 'ModelNode|SingleEndpoint|Migration' -count=1
   ```

3. 从节点领域输入、配置归一化、持久化比较和返回对象中移除 `APIKeyID`。
4. SQL 列表继续保留 `api_key_id`：H3 写 `""`，读取扫描到局部忽略变量，Legacy 继续使用 `NULL`。
5. 不创建迁移，不修改 Proxy 外部客户 API Key、任务 `APIKeyID` 或产物所有权字段。
6. 重跑定向测试。

完成判据：v7 历史库可直接使用，旧列不再具有模型节点业务语义。

### SK-005 Proxy Manager 单 Key API 与页面

涉及文件：

- `internal/httpapi/manager/nodes.go`
- `internal/httpapi/manager/nodes_test.go`
- `internal/httpapi/manager/handler_test.go`
- `internal/httpapi/manager/web/manager.html`
- `internal/httpapi/manager/web/manager.js`

步骤：

1. 先写失败测试：Key 仅接受 `^[A-Za-z0-9]{32}$`；31/33 位和特殊字符拒绝；`api_key_id` 作为未知字段拒绝；创建/Legacy 升级必须有 Key；编辑留空复用完整密文；GET 不返回废弃字段或明文。
2. 添加嵌入页面契约断言：无 Key ID 文案/字段；API Key 输入含 32 位长度与字母数字 pattern；编辑提示明确留空复用。
3. 运行：

   ```powershell
   go test ./internal/httpapi/manager -run 'Node|Web' -count=1
   ```

4. 删除 `nodeRequest`、`nodeDTO` 和前端 payload/fill 中的 `api_key_id`；服务端使用正则精确校验并返回稳定中文错误。
5. 在进入 Store 前拒绝缺 Key 的 Legacy-to-H3 变更，避免 SQLite 约束错误变成 500。
6. 前端仅保留密码型 Key 输入，不回显存储值；保留现有请求防重入与错误反馈。
7. 运行 Manager 定向测试及：

   ```powershell
   node --check internal/httpapi/manager/web/manager.js
   ```

完成判据：页面和 Manager API 均只有单 Key 契约，编辑留空安全复用。

### SK-006 Proxy Node Client 全调用链回归

涉及文件：

- `internal/upstream/nodeapi/client_test.go`
- `internal/upstream/registry/prober_test.go`
- `internal/upstream/registry/runtime_test.go`
- `internal/artifact/service_test.go`
- `internal/cleanup/worker_test.go`
- `internal/profile/node_test_executor_test.go`

步骤：

1. 将测试夹具统一为一个合法 32 位 Key，删除仅属于模型节点的 `APIKeyID` 字段。
2. 每个本地 HTTP fake 明确断言 `Authorization` 等于原样的 `Bearer <key>`，覆盖健康/能力、执行、产物、清理和测试任务调用。
3. 先运行定向包确认陈旧字段或复合 Token 断言失败，再清理所有调用点。
4. 运行：

   ```powershell
   go test ./internal/upstream/nodeapi ./internal/upstream/registry ./internal/artifact ./internal/cleanup ./internal/profile -count=1
   ```

完成判据：所有 Proxy 到 Node 的调用只携带一个未拼接 Key。

### SK-007 配置安全清理与文档同步

涉及文件：

- Node：`.gitignore`、`.env.example`、`conf.example.yml`、`README.md`、`SECURITY.md`、`requirements.txt`
- Proxy：`README.md`、`config.example.yaml`、`specs/developing/version_0.0.1/changes/004-manager-node-configuration/`

步骤：

1. Node `.gitignore` 加入 `conf.yml`；创建只含三项凭据的 `conf.example.yml`，使用满足格式但不属于任何现场环境的示例值。
2. 从 `.env.example` 和文档移除旧 API Key、Key ID、scope、WebUI 密码哈希来源；仅在迁移说明中保留已废弃名称。
3. 确认运行时代码无 Argon2 依赖后再从 `requirements.txt` 移除 `argon2-cffi`。
4. Proxy README、示例配置和 `004` 变更包同步为单 Key 规则；历史审计材料可保留旧行为证据，但必须标注被 `006` 取代。
5. 执行静态搜索，活动规则不得出现 `key_id.secret`、Node scope、Key ID 或旧凭据环境变量；`api_key_id` 仅允许出现在迁移、SQL 兼容代码、测试及明确标注忽略的文档中。

完成判据：配置来源唯一，示例和说明不再指导使用旧契约。

### SK-008 全量自测与静态检查

1. 仅格式化本变更触及的 Go 文件。
2. 在 Node 仓库运行：

   ```powershell
   walkingwithai\python.exe -m pytest tests\h3_service -q
   ```

3. 在 Proxy 仓库运行：

   ```powershell
   go test ./... -count=1
   go test -race ./... -count=1
   go vet ./...
   go build ./cmd/server ./cmd/healthcheck
   node --check internal/httpapi/manager/web/manager.js
   git diff --check
   ```

4. 若 race 因 CGO/编译器环境不可用，记录具体错误，不以未执行冒充通过。
5. 搜索日志和源码，确认未记录 Authorization、Key、密码、配置全文或密文。

完成判据：所有可自动执行的质量门通过，环境限制有明确证据。

### SK-009 代码审查、修复与回归

1. 使用 `requesting-code-review` 审查跨仓库差异，重点检查配置覆盖旁路、原子创建、未知 YAML 字段、Header 解析、密钥泄露、旧库约束和 Proxy 外部 Key 误伤。
2. Critical/Important 问题必须按 `systematic-debugging` 定位，补失败回归测试后修复。
3. 重跑受影响的定向测试和 SK-008 全量门禁。
4. 检查两个仓库 `git status --short`，不得把用户无关改动纳入本变更提交。

完成判据：无未处理高优先级审查问题，验证证据为最新运行结果。

### SK-010 变更文档回填和验收交接

1. 回填任务状态、实现差异、测试结果和环境限制。
2. 同步版本总技术/API/数据库文档中与本变更冲突的活动规则。
3. 更新 `changes/README.md`；人工验收未完成前状态保持 `In Progress`。
4. 将现场重启、真实 Node 连接、GPU 执行、配置 ACL 和发布回滚交给 `TEST_ACCEPTANCE.md` 对应负责人。

完成判据：代码、版本文档和现场验收清单可相互追溯。

## 4. Done Criteria

- [x] 变更目标由失败测试驱动实现。
- [x] Node `conf.yml` 是唯一凭据来源且生成/读取边界符合设计。
- [x] Node 和 Proxy 使用同一 32 位单 Key 契约。
- [x] SQLite 无新增迁移且历史库兼容。
- [x] Manager 页面、API、配置示例和文档已同步。
- [x] 自动化测试、静态检查和代码审查通过或记录明确环境限制。
- [ ] 人工验收由对应负责人在 `TEST_ACCEPTANCE.md` 确认，AI 未代签。

## 5. 开发执行记录（2026-08-13）

- Node 已实现根目录 `conf.yml` 的严格加载、首次并发安全生成、单 Key API 鉴权和 WebUI 凭据注入；Node 全量 `tests/h3_service` 为 `226 passed`。
- Proxy 已移除模型节点 Key ID 业务语义，旧 SQLite 列继续兼容；Manager 页面/API 和全部 Node 调用统一为 32 位字母数字单 Key。
- 独立代码审查无 Critical；两个 Important 已修复并补回归测试：编辑显式空 Key 复用旧密钥、使用已存 Key 测试时保留表单的新地址和超时；额外补充 Legacy 节点无已存 Key 时返回 400 的防护测试。
- Proxy 全量测试曾在 006 实现后通过；审查修复后的最新包级复跑被同工作区未完成的 `007-request-profile-simplification` 编译错误阻断。006 定向 Store、Node Client 和 Registry 测试此前通过；三条最新 Manager 审查回归通过文件级隔离编译执行验证。
- `go test -race` 因当前 Go 环境未启用 CGO 而无法执行；Windows `conf.yml` ACL、真实 Node/Proxy 联调和 GPU 任务保留为人工验收。

## 6. 下游触发点

- `/s-develop`：严格对齐 `test-driven-development`、`systematic-debugging`、`requesting-code-review`。
- `/s-archive`：人工验收完成后，对齐 `verification-before-completion`、`finishing-a-development-branch`。
