# 管理后台与动态节点测试验收准备

> 模型节点凭据验收以 `006-node-single-key-conf/TEST_ACCEPTANCE.md` 为准；本文旧 Key ID、scope 和复合 Token 用例不再作为现行验收标准。

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 版本/变更 | `004-manager-node-configuration` |
| 测试范围 | YAML 首次导入、SQLite 节点配置、动态运行时、管理接口和页面 |
| 输入文档 | `CHANGE_SPEC.md`、`TECH_SOLUTION.md`、`DATABASE_DELTA.md`、`API_DELTA.md`、`PROTOTYPE_DELTA.md`、`task.md` |
| 状态 | 原动态节点功能自动化完成；H3 真实生成闭环已通过，取消/公网 callback 等发布门禁仍待验收 |

## 2. 自动化测试范围

| 模块 | 功能点 | 测试类型 | 是否人工确认 |
| --- | --- | --- | --- |
| Config/Bootstrap | 旧 YAML 延迟解析、零节点启动、一次性导入 | 兼容/故障 | 否 |
| SQLite Store | CRUD、软删除、版本冲突、活动任务约束 | 功能/并发/迁移 | 否 |
| Registry/Scheduler | 动态增删、版本替换、停用排空、重启恢复 | 功能/并发 | 否 |
| Monitor Cache | 启停、应用中、未知状态和可用性 | 功能 | 否 |
| Manager API | 鉴权、校验、节点 CRUD、连接测试 | 接口/安全 | 否 |
| Manager Web | 路径、弹窗、表单状态和危险操作确认 | UI/兼容 | 否 |
| 真实模型节点 | 运行中热更新、故障和恢复 | 联调/稳定性 | 是 |

## 3. 功能测试清单

| 用例ID | 场景 | 前置条件 | 操作步骤 | 预期结果 | 结果 |
| --- | --- | --- | --- | --- | --- |
| BASE-001 | 合法旧 YAML 首次导入 | v3 数据库，2 个合法 upstream | 启动新版本两次 | 第一次原子导入 2 条并写标记，第二次不重复 | 通过（自动化） |
| BASE-002 | 导入中存在非法节点 | v3 数据库，旧 YAML 有非法 URL | 启动服务 | 启动失败；节点和标记均未写入，修正后可重试 | 通过（自动化） |
| BASE-003 | 首次启动没有 YAML 节点 | 空数据库，无 upstream | 启动服务 | 服务和 `/manager` 正常，写入 0 条导入标记 | 通过（自动化/本地启动） |
| BASE-004 | 导入后 YAML 变更 | 已存在导入标记 | 保持 YAML 语法合法但把节点 URL 改为无效值，或删除 upstream 后重启 | 节点仍取数据库，启动不受旧节点内容影响 | 通过（自动化） |
| BASE-005 | 节点全删后重启 | 导入完成，节点均软删除 | 重启服务 | 不重新导入 YAML，节点列表保持空 | 通过（自动化） |
| BASE-006 | 新增启用节点 | 管理员已登录 | 保存合法节点 | DB version=1，Registry 应用，首次探测前不可调度 | 通过（自动化） |
| BASE-007 | 更新空闲节点连接 | 节点无活动任务 | PUT 新配置和当前版本 | 版本加一，旧槽退出且仅有一个新槽 | 通过（自动化） |
| BASE-008 | 活动任务时修改地址 | 节点有 running 任务 | PUT 新配置和当前版本 | 409 `node_has_active_task`，数据库不变 | 通过（自动化） |
| BASE-009 | 活动任务时停用 | 节点有 running 任务 | 仅把 enabled 改为 false | 保存成功，当前任务继续处理，不领取下一条 | 通过（自动化） |
| BASE-010 | 重启恢复停用节点任务 | 停用节点有 reconciling 任务 | 重启前置服务 | 为该节点恢复 Worker，任务进入终态后不领取新任务 | 通过（自动化） |
| BASE-011 | 重新启用节点 | 已停用且无活动任务 | 更新 enabled=true | 首次新健康检查通过后恢复调度 | 通过（自动化） |
| BASE-012 | 删除启用节点 | 节点 enabled=true | DELETE | 409 `node_must_be_disabled` | 通过（自动化） |
| BASE-013 | 删除停用活动节点 | enabled=false 且有活动任务 | DELETE | 409 `node_has_active_task` | 通过（自动化） |
| BASE-014 | 删除停用空闲节点 | enabled=false 且无活动任务 | DELETE | 软删除，停止运行条目并清缓存，历史任务 ID 保留 | 通过（自动化） |
| BASE-015 | 并发更新 | 两个请求使用相同 version | 并发 PUT | 仅一个成功，另一个 409 版本冲突 | 通过（自动化） |
| BASE-016 | 草稿连接测试 | 合法本地 HTTP fake | POST test | 并行检查 Gradio/Jobs，DB 和缓存不变 | 通过（自动化） |
| BASE-017 | Registry 通知丢失 | 配置已写但不调用 Wake | 等待兜底周期 | Registry 最终应用新版本 | 通过（自动化） |
| BASE-018 | 零可用节点创建任务 | 零节点或全停用 | 调用创建视频任务 | 返回现有服务不可用响应，不产生假运行任务 | 通过（自动化） |

## 4. 接口与页面测试

| 场景 | 请求/操作重点 | 预期 | 结果 |
| --- | --- | --- | --- |
| 未登录访问节点接口 | 无 Cookie 调用 5 个节点接口 | 401，响应 no-store | 通过（自动化） |
| 非法/未知 JSON 字段 | POST/PUT/test | 400，不发起网络请求 | 通过（自动化） |
| URL 含凭据/查询/片段 | 节点表单提交 | 400，不在错误中回显 URL | 通过（自动化） |
| ID 复用软删除节点 | POST 相同 ID | 409 `node_id_conflict` | 通过（自动化） |
| 删除版本过期 | DELETE 旧 version | 409 `node_version_conflict` | 通过（自动化） |
| 连接测试部分失败 | Gradio 成功、Jobs 失败 | 502，只返回稳定错误码 | 通过（自动化） |
| 管理路径 | 访问 `/manager` 和静态资源 | 登录跳转和资源路径正确 | 通过（自动化/本地启动） |
| 旧页面路径 | 访问三个旧 GET 地址 | 308 到对应 manager 页面 | 通过（自动化） |
| 旧管理 API | 调用 `/monitor/api/*` | 不提供写接口别名 | 通过（自动化） |
| 弹窗未保存修改 | 编辑后切换/关闭 | 二次确认，取消后内容保留 | 通过（页面检查） |
| 保存/测试忙碌态 | 连续点击按钮 | 只发送一次请求，布局不跳动 | 通过（页面检查） |
| 节点空/失败/应用中 | 构造三种状态 | 文案和操作入口完整，状态不误判健康 | 通过（自动化/页面检查） |

## 5. 数据一致性检查

| 流程 | 涉及表 | 检查点 | 结果 |
| --- | --- | --- | --- |
| 首次导入 | 两张新表 | 节点和标记同事务提交；失败均不落库 | 通过（自动化） |
| 更新 | `model_service_nodes`、`video_tasks` | 活动检查、版本条件和更新原子 | 通过（自动化） |
| 删除 | `model_service_nodes`、`video_tasks` | 仅软删除，历史任务不变 | 通过（自动化） |
| 重启恢复 | 节点表、`video_tasks` | 停用节点活动任务仍可继续闭环 | 通过（自动化） |
| Registry 重复对账 | 节点表、内存运行条目 | 同节点同版本最多一个调度槽和采集器 | 通过（自动化重复执行） |
| 配置变更与任务领取并发 | 节点表、`video_tasks` | Claim 和配置写入只有一个先完成；旧版本槽不能领取 | 通过（自动化） |

## 6. 安全检查

| 检查项 | 标准 | 验证方式 | 结果 |
| --- | --- | --- | --- |
| 鉴权 | 节点读写和测试均要求管理会话 | `httptest` | 通过 |
| 会话路径 | Cookie 仅作用于 `/manager` | Handler 测试 | 通过 |
| 输入校验 | URL、路径、接口名、时长和版本均严格校验 | 表驱动测试 | 通过 |
| 请求限制 | JSON 超限、未知字段、尾随内容被拒绝 | Handler 测试 | 通过 |
| 敏感日志 | 不出现完整 URL、请求体、prompt、媒体和私有响应 | 捕获 logger 测试/人工复核 | 通过 |
| 上游响应 | 连接测试限制响应体并仅返回稳定结果 | 本地 HTTP fake | 通过 |

## 7. 稳定性与并发检查

| 检查项 | 目标 | 验证方式 | 结果 |
| --- | --- | --- | --- |
| 动态槽唯一性 | 每个节点最多一个运行槽 | 高重复 Reconcile 单元测试 | 通过 |
| 关闭收口 | 取消上下文后全部节点协程退出 | 泄漏/超时测试 | 通过 |
| 状态竞态 | 更新、删除、Claim 只有合法单一结果 | 并发测试与 `go test -race` | 并发测试通过；race 因本机无 C 编译器未执行 |
| 对账延迟 | Wake 丢失后一个兜底周期内生效 | fake clock 测试 | 通过 |
| 管理响应 | 小规模节点列表无明显阻塞 | 本地基准/人工观察 | 待人工确认 |

## 8. 本地验证命令

| 类型 | 命令 | 预期 |
| --- | --- | --- |
| 格式化 | `gofmt -w <changed-go-files>` | 无格式差异 |
| 单元与集成 | `go test ./... -count=1` | 通过 |
| 关键并发重复 | `go test ./internal/store/sqlite ./internal/scheduler ./internal/upstream/registry -count=20` | 通过 |
| 竞态 | `go test -race ./...` | 环境支持 CGO 和编译器时通过 |
| 静态检查 | `go vet ./...` | 无阻塞问题 |
| 构建 | `go build ./cmd/server ./cmd/healthcheck` | 成功 |

执行记录（2026-08-11）：

- `go test ./... -count=1`、关键包 `-count=20`、`go vet ./...`、双入口构建和 Manager JavaScript 语法检查均通过。
- `go test -race ./...` 已尝试；本机缺少 `gcc`，`runtime/cgo` 无法编译，因此未执行竞态检测，未修改默认 CGO 环境。
- 使用本地零节点数据库启动服务，并通过 Playwright 检查桌面、移动视口和配置弹窗滚动，未发现重叠或不可达操作。
- 独立代码审查提出的环境变量强类型展开和旧 `/monitor` 子树路由问题已经修复并补回归测试。

## 9. 人工联调准备

| 页面/端 | 检查点 | 问题记录位置 |
| --- | --- | --- |
| `/manager` | 桌面和移动尺寸、配置弹窗、键盘操作、错误反馈 | 本变更“问题记录” |
| 真实 Gradio/Jobs | 新增和连接测试、健康转变、视频生成 | 本变更“问题记录” |
| Docker 升级 | 挂载旧 YAML/SQLite 后首次导入和重启幂等 | 本变更“问题记录” |
| 故障演练 | 运行中停用、前置重启、模型服务崩溃恢复 | 本变更“问题记录” |

## 10. 人工确认项

| 确认项 | 原因 | 负责人/角色 | 状态 |
| --- | --- | --- | --- |
| 真实模型节点热更新联调 | 本地 fake 无法证明私有版本兼容 | 研发/测试 | 待人工确认 |
| Docker 旧配置首次导入 | 需要生产等价挂载和真实 SQLite 备份 | 运维/研发 | 待人工确认 |
| 活动任务停用和重启演练 | 需要真实长任务与故障窗口 | 测试/研发 | 待人工确认 |
| 管理后台业务验收 | 需要用户确认字段和交互 | 产品/用户 | 待人工确认 |
| 发布与回滚确认 | 需要发布流程和旧 YAML 核对 | 运维/负责人 | 待人工确认 |

## 11. 问题记录

| 问题ID | 问题 | 严重程度 | 复现步骤 | 处理状态 |
| --- | --- | --- | --- | --- |
| H3-A01 | Proxy 只发送 Secret，未按 Key ID + Secret 组装 Node Token | Critical | 新建真实 H3 节点后测试/探测返回 401 | 待修复 |
| H3-A02 | Legacy 节点编辑时隐式切换 H3 且复用空密钥，SQLite CHECK 返回 500 | High | 编辑首次导入节点并直接保存 | 待修复 |
| H3-A03 | H3 Orchestrator 未调用取消接口 | High | 运行中任务发起管理员取消 | 待修复 |
| H3-A04 | 提交/轮询未知可能创建新 operation 并重复生成 | High | Node 接收提交后断开响应或轮询断网 | 待修复 |

## 12. H3 契约修订自动化验收

| 用例ID | 场景 | 预期 | 状态 |
| --- | --- | --- | --- |
| H3-001 | 分离 `proxy`/`secret` 构造客户端 | 请求头为 `Bearer proxy.secret`，日志无 Token | Pending |
| H3-002 | Legacy 节点空 Secret 普通保存 | 400 稳定错误或仅启停成功，不写 DB、不返回 500 | Pending |
| H3-003 | Legacy 显式升级 | Key ID/Secret 完整时升级；活动任务、缺 Secret、隐式升级均拒绝 | Pending |
| H3-004 | Key ID 边界 | 点号、65 位、空值拒绝；下划线和连字符接受 | Pending |
| H3-005 | Node Pydantic 校验失败 | 422 统一错误包，`retryable=false` | Pending |
| H3-006 | Scope 检查 | 缺任一必需 scope 时连接测试失败并列出缺失项 | Pending |
| H3-007 | 运行任务取消 | 调用相同 cancel operation，Node/Proxy 收口 cancelled | Pending |
| H3-008 | 取消与成功竞争 | 以 Node 真实 succeeded 为准，不伪造 cancelled | Pending |
| H3-009 | 提交响应未知 | 重试相同 operation，最多一个 Node execution | Pending |
| H3-010 | 轮询短暂中断 | 保留同一 execution/attempt，恢复后正常完成 | Pending |
| H3-011 | 确定性 4xx | 不重试、不创建新 attempt | Pending |
| H3-012 | v8 升级 v9 | 新状态约束、行数、索引和外键全部正确 | Pending |
| H3-013 | Profile 测试超时 | 尽力取消远端 execution，无长期遗留作业 | Pending |
| H3-014 | 历史 CFG 配置 | 可读，发布新配置时删除，页面不展示 CFG | Pending |
| H3-015 | 发布/消费接口矩阵 | Node 12 路由存在；Proxy 9 路由完整；3 条 maintenance 不消费 | Pending |
| H3-016 | ASGI 内查询已完成 execution | 不调用嵌套 `asyncio.run`；返回 succeeded + artifact | Passed（自动化 + 真实 Node） |
| H3-017 | 8188 队列和容量转发 | health/Manager 显示真实队列、CPU、GPU、内存、显存 | Passed（自动化 + 真实 Node） |
| H3-018 | Stage 开始同步父任务 | 单查、列表、Manager 均显示 running + node ID | Passed（自动化 + 真实任务） |
| H3-019 | Stage 完成同步与回调 | Proxy succeeded、签名 URL 可下载、callback 收到 succeeded | Partially passed：真实生成/查询/列表/下载通过；callback 状态与投递自动化通过，公网地址待提供 |
| H3-020 | H3 URL 含 `/ui` | Manager 保存返回 400；运行请求不出现 `/ui/internal/v1/*` | Passed（自动化 + 正式日志） |

## 13. 验证命令与人工门禁

Proxy 自动化：

```powershell
go test ./internal/upstream/nodeapi ./internal/httpapi/manager ./internal/orchestrator ./internal/profile ./internal/cleanup ./internal/artifact -count=1
go test ./... -count=1
go vet ./...
go build ./cmd/server ./cmd/healthcheck
```

Node 自动化：

```powershell
walkingwithai\python.exe -m pytest tests\h3_service -q
```

2026-08-13 最终执行：Node `231 passed`；Proxy `go test ./... -count=1`、`go vet ./...`、`go build ./cmd/server ./cmd/healthcheck` 全部通过。真实任务、下载摘要和运行快照证据见 `task.md` 的 MN-025。

人工门禁：真实 Key ID/Secret 认证、全部必需 scope、真实 GPU 生成取消、提交响应未知、轮询断网恢复、升级备份与回滚演练。任何一项未执行都不得宣称生产联调完成。

## 14. FIFO、播放与绝对 URL 自动化验收

| 用例ID | 场景 | 预期 | 状态 |
| --- | --- | --- | --- |
| TD-001 | 早任务含 generation+restoration，后任务仅 generation | 早任务 generation 成功后下一次领取 restoration，不领取后任务 generation | Passed（自动化） |
| TD-002 | 同一任务三阶段 | 严格按 stage_order 领取，前序未成功时后序不可领取 | Passed（自动化） |
| TD-003 | 两节点并行 | 节点 A 已运行早任务时，节点 B 可领取其最早可执行任务，不重复领取阶段 | Passed（自动化并发） |
| TD-004 | 最早阶段限定其他节点 | 当前节点跳过不可执行阶段，领取对其最早的可执行任务 | Passed（自动化） |
| TD-005 | 最早阶段在 retry_wait | `next_attempt_at` 未到时不阻塞后续可执行任务；到期后恢复 FIFO | Passed（自动化） |
| TD-006 | 重启恢复活动阶段 | 使用原 current node/operation/execution 恢复，不因排序新建执行 | Passed（自动化） |
| TD-007 | public_base_url 合法性 | 合法 HTTP/HTTPS 根地址规范化；凭据、query、fragment、子路径和缺 host 拒绝启动 | Passed（自动化） |
| TD-008 | V2 单任务成功 | `content.url` 为配置的 Proxy 绝对地址且签名可下载 | Passed（自动化/独立本地实例） |
| TD-009 | V2 列表混合状态 | 仅成功项有绝对 content URL，其他响应字段不变 | Passed（自动化/独立本地实例） |
| TD-010 | Host 头注入 | 恶意 Host/X-Forwarded 不改变返回 URL 前缀 | Passed（自动化） |
| TD-011 | Manager H3 artifact 任务 | 列表返回非空绝对 `video_url`，不返回 artifact ID/Node URL | Passed（自动化/独立本地实例） |
| TD-012 | Manager 历史任务 | 无 artifact、有合法历史 public URL 时仍可播放 | Passed（自动化，仅兼容安全 HTTP(S) 绝对地址） |
| TD-013 | 播放按钮显隐 | 仅 video_url 非空的成功任务显示“播放” | Passed（页面契约/Playwright） |
| TD-014 | 播放弹窗生命周期 | 打开可播放；关闭暂停、清 src、释放资源；列表轮询不干扰 | Passed（Playwright 桌面/480px） |
| TD-015 | Range 播放 | 浏览器 Range 请求得到 206 和正确 Content-Range，可拖动进度 | Partially passed（206/Content-Range 自动化通过；真实大视频拖动待人工） |
| TD-016 | URL 过期或文件故障 | 文件 API 保持稳定错误，播放弹窗显示受控失败文案 | Passed（自动化/Playwright） |

自动化命令准备：

```powershell
go test ./internal/store/sqlite -run 'TestClaimStage' -count=1
go test ./internal/config ./internal/artifact ./internal/httpapi/v2 ./internal/httpapi/manager ./cmd/server -count=1
node --check internal/httpapi/manager/web/manager.js
go test ./... -count=1
go vet ./...
go build ./cmd/server ./cmd/healthcheck
```

执行记录（2026-08-13，MN-026 至 MN-030）：

- ClaimStage 定向测试重复 20 次通过；查询计划使用 claim、前序阶段和任务主键索引。取消中、已取消、其他终态和软删除父任务均不可再领取。
- 独立本地 Proxy `18082` 完成绝对 URL、单查、列表、Manager 和播放器验证；正式 `18081` 未重启，不将本地验证误报为正式发布。
- 历史 `result_public_url` 不做 DNS 解析；为避免请求期 DNS/网络副作用，兼容层拒绝已知本机/私网字面地址和非规范数值 IPv4，但历史域名的当前 DNS 指向仍依赖部署方维护可信数据。新 H3 任务不走该路径，统一使用 Proxy artifact 签名 URL。
- 全仓测试、vet、双入口构建、JS 语法和 diff 检查通过；race 因本机缺少 `gcc` 未执行。

## 15. 新增人工确认项

| 确认项 | 原因 | 负责人/角色 | 状态 |
| --- | --- | --- | --- |
| 外部地址可达性 | 需要从非 Proxy 主机验证真实域名、防火墙和反向代理 | 运维/测试 | 待人工确认 |
| 真实多节点 FIFO 与吞吐 | 需要至少两台可运行 Node，且任务耗时不可由单测等价模拟 | 测试/研发 | 待专用环境确认 |
| 大视频播放和拖动 | 需要真实 MP4、浏览器和网络条件验证 Range 体验 | 测试/业务 | 待人工确认 |
| HTTPS 混合内容检查 | 需要真实 HTTPS Manager 页面确认视频 URL 也为 HTTPS | 运维/测试 | 待人工确认 |
