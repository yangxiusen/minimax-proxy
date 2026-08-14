# 运行中任务中止一致性变更说明

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 目标版本 | `version_0.0.1` |
| 变更编号 | `009-running-task-cancellation-consistency` |
| 变更类型 | 缺陷修复 / 状态流转与调度可靠性 |
| 优先级 | High |
| 提出日期 | 2026-08-15 |
| 负责人 | 待人工填写 |
| Preflight 执行情况 | 已读取 `specs/README.md`、`PROJECT_OVERVIEW.md`、`ARCHITECTURE.md`、版本 PRD、002/004 变更和 Node 取消契约 |

## 2. 变更背景

- 当前行为：管理员在 Manager 中中止运行中的 H3 Node 任务后，Proxy 可能先把本地任务置为 `cancelled` 并释放阶段租约，却没有确认 Node execution 已停止。Node 仍继续原任务，新任务进入该 Node 后只能排队。
- 根因：本地任务终态、Node execution 终态和 Node 调度资格没有形成同一条可恢复状态链；取消调用的响应和错误被忽略，调度阻塞只存在于内存。
- 触发原因：真实操作已复现“旧任务继续执行，后续任务长期排队”。
- 既有契约：Node 已提供幂等的 `POST /internal/v1/executions/{execution_id}/cancel` 和 execution 查询接口，Proxy 应持续对账到终态。

## 3. 目标结果

- 管理员中止后，本地任务立即进入 `cancelled`，不长期停留在“中止中”，也不再参与 FIFO。
- 为仍可能运行旧 execution 的 Node 建立持久化调度屏障；屏障只影响该 Node，不阻塞其他健康 Node 领取后续任务。
- Proxy 使用原 attempt 的 operation/execution 身份幂等恢复、取消和查询，确认远端不再运行后才解除屏障。
- Proxy 重启、Node 暂时离线或取消响应丢失时，屏障和对账上下文不丢失。
- 单 Node 且取消无法确认时，不伪装新任务正在执行；后续任务保持排队，直到该 Node 对账完成或恢复。

## 4. 影响范围

| 影响项 | 是否影响 | 说明 |
| --- | --- | --- |
| 产品流程 | Y | 运行中任务中止从“释放后尽力取消”改为“任务终态与 Node 屏障分离” |
| 业务逻辑 | Y | 新增取消屏障创建、恢复、远端确认和解除流程 |
| 数据库 | Y | 新增持久化 Node 调度屏障表，升级数据库版本 |
| 接口契约 | Y | 路径和字段不变，明确 Manager 202 与 Node 取消确认的行为语义 |
| 页面或交互 | N | 沿用现有确认框、任务状态和 Node 异常展示 |
| 原型 | N | 无页面结构或核心交互变化 |
| 配置部署 | N | 不新增配置项；恢复节奏复用 Node 的轮询间隔和请求超时 |
| 回归测试 | Y | 覆盖取消竞争、网络错误、重启恢复、多 Node 和单 Node |

## 5. 文档生成决策

| 文档 | 是否生成 | 原因 |
| --- | --- | --- |
| `CHANGE_SPEC.md` | Y | 固定生成 |
| `task.md` | Y | 固定生成 |
| `TEST_ACCEPTANCE.md` | Y | 固定生成 |
| `PRD_DELTA.md` | Y | 中止流程、调度隔离和验收规则变化 |
| `PROTOTYPE_DELTA.md` | N | 页面结构和交互不变 |
| `TECH_SOLUTION.md` | Y | 跨 Store、Orchestrator、Registry 和 Node API |
| `API_DELTA.md` | Y | 既有接口行为语义需要补充 |
| `API_INTERFACE.md` | N | 无新增或重构接口 |
| `DATABASE_DELTA.md` | Y | 新增屏障表与迁移 |
| `DATABASE_DESIGN.md` | N | 局部数据模型增量足以表达 |

## 6. 变更详情

### 6.1 变更前

- 活动 stage 中止时，本地任务、stage 和 attempt 可直接进入终态并释放 Node 归属。
- Orchestrator 即使调用 Node 取消，也可能忽略错误并立即返回。
- 调度器随后认为 Node 可用并提交新任务；Node 内的旧 execution 仍占用执行槽。
- 内存中的 `SchedulingBlocked` 无法覆盖 Proxy 重启恢复。

### 6.2 变更后

1. 管理员中止与正常完成使用条件更新竞争，只有一个事务获胜。
2. 若活动 stage 尚未创建 attempt，不存在远端执行风险，直接终结本地任务。
3. 若已有非终态 attempt，同一事务创建 `node_dispatch_barriers`，保存 Node、任务、stage、attempt、operation 和 execution 身份，再终结本地任务。
4. Node 运行时发现屏障后优先执行取消对账，不领取新 stage；其他 Node 的运行时不受影响。
5. execution ID 未绑定时，使用原 operation 幂等恢复 `CreateExecution` 的结果，再发起取消，禁止创建新 attempt 或新 operation。
6. Node 返回 `cancelled/failed/succeeded` 后解除屏障；execution 不存在时，必须同时确认 Node 队列为空才解除。
7. 网络错误、认证错误、5xx 或状态未知时保留屏障并退避重试，重启后继续。
8. Node 对同一取消 operation 返回缓存 `cancelling` 时重新执行安全的中断检查；迟到结果发现 execution 已在 `cancelling` 时丢弃产物并收口为 `cancelled`。

### 6.3 不在本次范围

- 不新增 Manager 页面、按钮、人工“强制解除屏障”操作或新的公开 V2 状态。
- 不修改 Node 取消路由、请求字段或鉴权方式；允许修复 Node 内部幂等重放和终态收口实现。
- 不保证单个故障 Node 在旧 execution 未清空时继续执行新任务。
- 不修改 legacy Gradio 任务的既有取消协议；本变更聚焦 `h3-node-v1` stage execution。

## 7. 兼容性与风险

- 对外 V2 和 Manager 响应结构保持兼容。
- 新二进制依赖数据库迁移；回滚前必须使用升级前 SQLite 备份，不能让旧二进制直接写入升级后的活动屏障数据。
- 如果 Node 永久不可达，对应 Node 会持续隔离，但任务已进入终态，其他 Node 继续调度。
- 如果管理员中止与 Node 成功同时发生，以 Proxy 条件事务的获胜者为准；取消先提交时忽略迟到产物并只解除屏障。
- 不提供绕过屏障的自动超时释放，避免重新引入旧任务续跑和新任务排队问题。

## 8. 验收标准

- Given 运行中任务已绑定 execution，When 管理员中止，Then 任务变为 `cancelled`、建立 Node 屏障且同一 Node 不领取新任务。
- Given 另有健康 Node，When 某 Node 存在取消屏障，Then 后续可执行任务由其他 Node 正常领取。
- Given Node 取消成功并查询到终态，When 对账完成，Then 删除屏障并恢复该 Node 调度。
- Given 取消接口暂时失败或 Proxy 重启，When 运行时恢复，Then 使用相同 operation/execution 继续取消，不重复生成。
- Given execution ID 尚未绑定但 attempt 已创建，When 中止与提交响应竞争，Then 使用原 operation 找回 execution 后取消。
- Given execution 查询 404，When Node 队列仍非空，Then 保留屏障；只有队列确认为空才解除。
- Given 只有一个 Node 且取消无法确认，When 创建后续任务，Then 后续任务保持真实 `queued`，不得显示为正在执行。
