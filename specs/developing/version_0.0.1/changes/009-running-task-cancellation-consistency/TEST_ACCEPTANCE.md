# 运行中任务中止一致性测试与验收准备

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 版本/变更 | `009-running-task-cancellation-consistency` |
| 测试范围 | Manager 中止、SQLite 屏障、Orchestrator 对账、Registry 多 Node 调度 |
| 输入文档 | `CHANGE_SPEC.md`、`TECH_SOLUTION.md`、`DATABASE_DELTA.md`、`API_DELTA.md`、`task.md` |
| 状态 | In Progress（自动化完成，待人工联调） |

## 2. 自动化测试范围

| 模块 | 功能点 | 测试类型 | 是否人工确认 |
| --- | --- | --- | --- |
| SQLite Store | 中止事务、屏障、竞态、迁移 | 单元/并发 | 否 |
| Orchestrator | execution 恢复、取消、查询和解除 | 单元 | 否 |
| Registry/Scheduler | Node 级隔离、多 Node 继续调度 | 单元/集成 | 否 |
| Manager | 202 兼容和 Wake | HTTP 单元 | 否 |
| H3 Node execution service | 取消幂等重放、迟到结果收口 | Python 单元/API | 否 |
| 真实 H3 Node | GPU 中断、Comfy 队列清理 | 真实联调 | 是 |

## 3. 功能测试清单

| 用例ID | 场景 | 前置条件 | 操作步骤 | 预期结果 | 结果 |
| --- | --- | --- | --- | --- | --- |
| FT-001 | 已绑定 execution 正常中止 | task/stage/attempt 运行中 | 管理员中止，Node 返回 cancelled | task cancelled；屏障建立后解除；Node 可继续调度 | 自动化通过 |
| FT-002 | attempt 已建但 execution 未绑定 | CreateExecution 响应未知 | 管理员中止 | 使用原 operation 找回 execution 并取消，不创建新 attempt | 自动化通过 |
| FT-003 | 取消请求暂时失败 | Node 返回 5xx/连接错误 | 管理员中止并触发多轮对账 | task cancelled；屏障保留；同 Node 不领取新 stage | 自动化通过 |
| FT-004 | Proxy 重启恢复 | 数据库存在屏障 | 重启 Store/Registry | 屏障仍生效并使用相同 operation 继续对账 | Store 重开与 Registry 恢复自动化通过；真实进程待人工确认 |
| FT-005 | execution 404 且队列非空 | Node 健康队列计数大于 0 | 对账 | 屏障不解除 | 自动化通过 |
| FT-006 | execution 404 且队列为空 | Node 健康队列计数均为 0 | 对账 | 屏障解除，Node 恢复调度 | 自动化通过 |
| FT-007 | 双 Node 隔离 | Node A 有屏障，Node B 健康 | 后续任务入队 | Node B 继续领取，Node A 只做取消对账 | Store/Runtime 自动化通过；真实环境待人工确认 |
| FT-008 | 单 Node 隔离 | 唯一 Node 有屏障 | 后续任务入队 | 后续任务保持 queued，不显示 running | 自动化通过；页面观感待人工确认 |
| FT-009 | 取消与完成竞争 | 管理员中止和 CompleteStage 并发 | 同时释放并等待结果 | 仅一个事务获胜，无 cancelled 后覆盖 succeeded 或反向覆盖 | 自动化通过 |
| FT-010 | 取消与 attempt 创建竞争 | stage 已 leased 未创建 attempt | 并发中止和 CreateStageAttempt | 取消先胜则禁止远端提交；attempt 先胜则建立屏障 | 自动化通过 |
| FT-011 | Node 取消重放 | 首次 adapter cancel 返回 false | 使用同一 operation 再次取消 | 再次检查同一 job，成功后 operation/execution 均为 cancelled | 自动化通过 |
| FT-012 | Node 迟到结果 | execution 已为 cancelling | 后台执行返回成功产物 | 不注册产物，清理输出并收口 cancelled | 自动化通过 |
| FT-013 | 运行中 generation 取消 | Comfy job 正在运行 | 按 job 中断并查询 execution | 中断响应不提前报 cancelled；底层终止后收口；瞬时查询失败保持 cancelling | 自动化通过 |
| FT-014 | 取消与迟到异常竞争 | execution 已为 cancelling | 后台执行抛出异常 | cancelled 获胜，迟到异常不得覆盖为 failed/unknown | 自动化通过 |
| FT-015 | 取消与产物登记竞争 | Manager 取消与 stage 完成并发 | 尝试登记产物并完成 stage | 取消获胜时产物与 stage 完成同事务回滚，无 active 迟到产物 | 自动化通过 |
| FT-016 | generation 提交窗口取消 | accepted 未提交、queue_prompt 进行中或声明后重启 | 并发取消 execution | 未提交时不发远端取消；提交前持久化稳定 prompt ID；重启后仍按该 ID 对账；重复 claim 不重复提交 | 自动化通过 |

## 4. 接口测试清单

| 接口 | 场景 | 请求重点 | 响应/副作用重点 | 结果 |
| --- | --- | --- | --- | --- |
| `POST /manager/api/tasks/{id}/cancel` | 活动 stage | 管理会话、合法 ID | 202；本地终态与屏障原子提交；Wake 一次 | 自动化通过 |
| `POST /internal/v1/executions/{id}/cancel` | 重复调用 | 固定 Idempotency-Key | 只作用于同一 execution/job；运行中保持 cancelling | 自动化通过；真实 Comfy 待人工确认 |
| `GET /internal/v1/executions/{id}` | 全部非终态/终态 | 原 execution ID | 只有安全终态解除屏障 | 自动化通过 |
| `GET /internal/v1/health` | execution 404 | 队列计数字段 | 仅两个计数均为 0 时作为空闲证据 | 自动化通过 |

## 5. 数据一致性检查

| 流程 | 涉及表 | 检查点 | 结果 |
| --- | --- | --- | --- |
| 建立屏障 | `video_tasks/task_stages/stage_attempts/node_dispatch_barriers/callback_deliveries` | 同一事务成功或回滚；Node 最多一个屏障 | 待执行 |
| execution 恢复 | `node_dispatch_barriers` | 只补 execution_id，不复活 task/stage | 待执行 |
| 对账失败 | `node_dispatch_barriers` | retry_count、next_retry_at、last_error_code 条件更新 | 待执行 |
| 对账成功 | `node_dispatch_barriers` | 条件删除恰好一行；后续可再次领取 | 待执行 |
| Node 修改/删除 | `model_service_nodes/node_dispatch_barriers` | 有屏障时返回活动占用冲突 | 待执行 |

## 6. 安全和可观测性检查

| 检查项 | 标准 | 验证方式 | 结果 |
| --- | --- | --- | --- |
| 管理鉴权 | 未登录不能中止 | HTTP 单元测试 | 待执行 |
| Node 鉴权 | 继续使用加密保存的单 API Key | 客户端测试 | 待执行 |
| 日志脱敏 | 不记录 Key、prompt、媒体 URL 或原始响应 | 日志捕获 + 人工复核 | 待人工确认 |
| 关联字段 | 记录 task/stage/attempt/execution/node 和稳定错误码 | 日志单元测试 | 待执行 |
| Node 状态 | 屏障期间显示异常而非空闲 | Registry/Manager 快照测试 | 待执行 |

## 7. 本地验证命令

| 类型 | 命令 | 预期 |
| --- | --- | --- |
| 定向测试 | `go test ./internal/store/sqlite ./internal/orchestrator ./internal/upstream/registry ./internal/httpapi/manager` | 通过 |
| 全量测试 | `go test ./...` | 通过 |
| 并发检查 | `go test -race ./internal/store/sqlite ./internal/orchestrator ./internal/upstream/registry` | 主机具备 CGO/编译器时通过 |
| 静态检查 | `go vet ./...` | 无阻塞问题 |
| 构建 | `go build ./cmd/server ./cmd/healthcheck` | 成功 |
| Node 定向测试 | 在 `Minimax-H3` 执行 `pytest -q tests/h3_service/test_execution_api.py tests/h3_service/test_execution_stage_service.py tests/h3_service/test_generation_reconcile.py` | 通过 |

执行证据（2026-08-15）：

- `go test ./...`：通过。
- `go vet ./...`：通过。
- `go build ./cmd/server ./cmd/healthcheck`：通过。
- `walkingwithai/python.exe -m pytest -q tests/h3_service/test_execution_api.py tests/h3_service/test_execution_stage_service.py tests/h3_service/test_generation_reconcile.py`：`63 passed`。
- 扩展执行上述测试并加入 `test_db.py`、`test_stage_executor.py`、`test_comfy_postprocess_adapter.py`：`87 passed`。
- `walkingwithai/python.exe -m py_compile ...`（execution service、generation adapter、server 与相关测试）：通过。
- `tests-unit/jobs_cancel_test/jobs_cancel_test.py`：纯 helper 用例 `11 passed`；HTTP 用例因当前虚拟环境缺少 `pytest-aiohttp`/`pytest-asyncio` fixture 未执行，待补齐测试依赖后确认。
- `go test -race ./internal/store/sqlite ./internal/orchestrator ./internal/upstream/registry`：未执行成功；启用 CGO 后主机缺少 `gcc`，待具备 C 编译器的环境确认。

## 8. 人工确认项

| 确认项 | 原因 | 负责人/角色 | 状态 |
| --- | --- | --- | --- |
| 真实运行任务中止 | 需确认 Comfy/GPU 作业确实停止 | 研发/测试 | 待人工确认 |
| 双 Node 故障演练 | 需真实验证 Node A 隔离时 Node B 吞吐 | 研发/测试 | 待人工确认 |
| 单 Node 状态展示 | 需确认任务保持 queued 且 Node 异常信息可理解 | 产品/测试 | 待人工确认 |
| Proxy/Node 重启组合 | 需真实进程和持久化数据库 | 运维/测试 | 待人工确认 |
| 回滚演练 | 需使用升级前 SQLite 备份和旧二进制 | 运维 | 待人工确认 |

## 9. 问题记录

| 问题ID | 问题 | 严重程度 | 复现步骤 | 处理状态 |
| --- | --- | --- | --- | --- |
| BUG-009-01 | 中止后旧 Node execution 继续运行并阻塞新任务 | High | Manager 中止运行任务后提交新任务 | 本地修复完成，待真实 Node 验证 |
| REVIEW-009-01 | adapter 返回后过早写 cancelled、迟到异常覆盖终态、产物登记竞态、generation 非 job-specific 中断 | Critical | 独立规格与代码质量审查 | 阻塞项已修复并重新执行受影响测试 |
