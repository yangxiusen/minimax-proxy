# 运行中任务中止一致性开发任务列表

> **执行要求：** 后续 `/s-develop` 必须按 `test-driven-development`、`systematic-debugging` 和 `requesting-code-review` 执行。适合使用 `subagent-driven-development` 逐任务实施，或使用 `executing-plans` 顺序执行。

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 版本/变更 | `009-running-task-cancellation-consistency` |
| 任务范围 | SQLite、Manager 中止、Stage Orchestrator、Node Registry、监控与文档 |
| 输入文档 | `CHANGE_SPEC.md`、`PRD_DELTA.md`、`TECH_SOLUTION.md`、`DATABASE_DELTA.md`、`API_DELTA.md`、`TEST_ACCEPTANCE.md` |

## 2. 任务总览

| 任务ID | 任务名称 | 优先级 | 状态 | 依赖任务 | AI可完成 | 说明 |
| --- | --- | --- | --- | --- | --- | --- |
| DSG-009 | 设计确认 | High | Completed | - | 是 | 已确认屏障模型，并补充 materialize 后请求快照约束 |
| DB-009 | 增加 Node 调度屏障迁移与 Store | High | Completed | DSG-009 | 是 | 新增 v14 表、CRUD、活动占用和迁移测试 |
| ST-009 | 原子化管理员中止与提交竞态 | High | Completed | DB-009 | 是 | 屏障、本地终态和 callback 同事务；强化 attempt 前置条件 |
| NODE-009 | 修复 Node 取消幂等收口 | High | Completed | ST-009 | 是 | 按 job 中断；运行中保持 cancelling 并持续对账；迟到结果/异常收口 cancelled |
| ORCH-009 | 实现 Proxy 取消对账 | High | Completed | ST-009,NODE-009 | 是 | operation 恢复、幂等取消、终态/404+空闲判定 |
| REG-009 | 接入 Node 级隔离和多 Node 调度 | High | Completed | ORCH-009 | 是 | 屏障优先于 ClaimStage，监控显示真实异常 |
| TEST-009 | 全量验证与代码审查 | High | Completed | REG-009 | 是 | 独立审查阻塞项已修复；自动化通过；race 缺 gcc 已记录 |
| DOC-009 | 同步设计与状态 | Medium | Completed | TEST-009 | 是 | 已更新设计、版本索引和本地测试证据 |

## 3. 实施步骤

### 3.1 DB-009：屏障迁移和 Store

**文件：**

- 新增：`migrations/014_node_dispatch_barriers.sql`
- 修改：`migrations/embed.go`
- 修改：`internal/store/sqlite/store.go`（注册 v14 迁移）
- 新增：`internal/store/sqlite/dispatch_barrier.go`
- 新增：`internal/store/sqlite/dispatch_barrier_test.go`
- 新增：`internal/store/sqlite/migration_v14_test.go`
- 修改：`internal/store/sqlite/model_node.go`
- 修改：`internal/store/sqlite/model_node_test.go`

- [ ] 先写 `TestOpenMigratesVersionThirteenToNodeDispatchBarriers`，断言 `user_version=14`、表字段、外键和索引存在；运行 `go test ./internal/store/sqlite -run TestOpenMigratesVersionThirteenToNodeDispatchBarriers`，预期在迁移实现前失败。
- [ ] 写屏障 Store 测试，覆盖每 Node 唯一、按 row_version 补 execution、失败退避、条件删除和重开数据库后仍存在。
- [ ] 迁移为 `stage_attempts` 增加 `request_snapshot_json`，并验证新 attempt 在首次远端提交前持久化精确请求。
- [ ] 实现以下最小接口并使测试通过：

```go
type NodeDispatchBarrier struct {
    NodeID, TaskID, StageID, AttemptID string
    OperationID, ExecutionID, CancelOperationID string
    LastErrorCode string
    RetryCount int
    NextRetryAt, CreatedAt, UpdatedAt int64
    RowVersion int64
}

func (s *Store) GetNodeDispatchBarrier(ctx context.Context, nodeID string) (NodeDispatchBarrier, error)
func (s *Store) BindBarrierExecution(ctx context.Context, nodeID string, rowVersion int64, executionID string) error
func (s *Store) DeferNodeDispatchBarrier(ctx context.Context, nodeID string, rowVersion int64, errorCode string, retryAt time.Time) error
func (s *Store) ResolveNodeDispatchBarrier(ctx context.Context, nodeID string, rowVersion int64) error
func (s *Store) HasNodeDispatchBarrier(ctx context.Context, nodeID string) (bool, error)
```

- [ ] 修改节点活动占用检查，使屏障存在时拒绝危险更新和删除；运行相关 Store 测试，预期通过。

### 3.2 ST-009：中止事务和竞态

**文件：**

- 修改：`internal/store/sqlite/store.go`
- 修改：`internal/store/sqlite/store_test.go`
- 修改：`internal/store/sqlite/stage_store.go`
- 修改：`internal/store/sqlite/stage_store_test.go`

- [ ] 写 `TestAdminCancelActiveStageCreatesBarrierAndFinishesTaskAtomically`：运行 attempt 中止后 task/stages/attempt/callback/屏障必须同时提交。
- [ ] 写 `TestCancelAndCreateStageAttemptHaveSingleWinner`：循环并发执行取消与 attempt 创建；每轮只能是“无 attempt/无屏障”或“有 attempt/有屏障”。
- [ ] 写 `TestCancelAndCompleteStageHaveSingleTerminalWinner`，禁止 `cancelled` 与 `succeeded` 相互覆盖。
- [ ] 先运行上述测试并确认旧实现失败，再修改 `RequestAdminCancel`、`CreateStageAttempt` 和完成路径的条件 SQL。
- [ ] 删除启动时把无归属 `cancelling` 任务直接收敛为 cancelled 的补丁路径；新逻辑由屏障显式恢复，不推断远端安全。
- [ ] 运行 `go test ./internal/store/sqlite`，预期通过。

### 3.3 NODE-009：Node 取消幂等收口

**工作区：** `E:/MiniMax-WorkFlow/Minimax-H3`

**文件：**

- 修改：`h3_service/services/execution_service.py`
- 修改：`tests/h3_service/test_execution_api.py`
- 修改：`tests/h3_service/test_execution_stage_service.py`

- [ ] 写 API 回归测试：第一次 adapter cancel 返回 false、第二次使用相同 operation 返回 true；断言第二次会重新作用于同一 job，最终 execution 和 operation 响应均为 `cancelled`。
- [ ] 写 service 回归测试：execution 已为 `cancelling` 时后台返回迟到产物；断言不注册 artifact、临时输出被清理、输入锁释放且 execution 为 `cancelled`。
- [ ] 修改 `ExecutionService.cancel`：历史 operation 响应仍为 `cancelling` 时继续执行同一资源的安全取消检查；终态响应继续直接幂等返回。
- [ ] 修改 generation 取消：只中断目标 job；运行中中断不报告终态，后续对账确认退出；瞬时对账失败保持 `cancelling`。
- [ ] 修改迟到完成路径：条件成功更新未命中后读取真实状态，`cancelling` 分支执行取消终态收口，不把它归类为普通失败。
- [ ] 运行 `pytest -q tests/h3_service/test_execution_api.py tests/h3_service/test_execution_stage_service.py tests/h3_service/test_generation_reconcile.py`，预期通过。

### 3.4 ORCH-009：取消对账

**文件：**

- 修改：`internal/orchestrator/orchestrator.go`
- 修改：`internal/orchestrator/orchestrator_test.go`
- 按需修改：`internal/upstream/nodeapi/client.go`
- 按需修改：`internal/upstream/nodeapi/client_test.go`

- [ ] 用 fake NodeClient 写表驱动测试：已绑定 execution、未绑定 execution、CancelExecution 5xx、非终态轮询、各终态、404+队列忙、404+队列空。
- [ ] 断言所有恢复提交复用 barrier 的 `operation_id`，所有取消复用 `cancel_operation_id`，且不调用 `CreateStageAttempt/ClaimStage`。
- [ ] 扩展 Orchestrator Store/NodeClient 的窄接口，并在 `ProcessOne` 开头优先读取屏障。
- [ ] 实现 `reconcileCancellationBarrier`：单次调用只推进有限步骤，暂时失败持久化退避并返回 `nil`，避免 scheduler 退出。
- [ ] 对 `succeeded/failed/cancelled` 解除屏障；对 404 调用 Health，仅当两个队列计数明确为 0 时解除。
- [ ] 运行 `go test ./internal/orchestrator ./internal/upstream/nodeapi`，预期通过。

### 3.5 REG-009：Node 级隔离与调度

**文件：**

- 修改：`internal/upstream/registry/runtime.go`
- 修改：`internal/upstream/registry/runtime_test.go`
- 修改：`internal/httpapi/manager/handler_test.go`

- [ ] 写双 Node 测试：Node A 有屏障且取消失败，Node B 仍领取后续 stage；Node A 的 CreateExecution 只能用于恢复屏障，不能领取新 stage。
- [ ] 写单 Node 测试：屏障期间后续 task 保持 `queued`，Node 快照不是 idle。
- [ ] 为 Node API scheduler 增加 `Active` 回调，屏障存在时仍驱动 Processor 对账但跳过普通 ClaimStage。
- [ ] 初始化和 probe 从 Store 恢复 `SchedulingBlocked`，屏障期间使用稳定错误码 `node_cancel_reconciling`；解除后再恢复健康调度。
- [ ] 运行 `go test ./internal/upstream/registry ./internal/httpapi/manager`，预期通过。

### 3.6 TEST-009：验证和审查

- [ ] 格式化改动 Go 文件：`gofmt -w <本变更修改的.go文件>`。
- [ ] 运行 `go test ./internal/store/sqlite ./internal/orchestrator ./internal/upstream/nodeapi ./internal/upstream/registry ./internal/httpapi/manager`，预期通过。
- [ ] 运行 `go test ./...`、`go vet ./...`、`go build ./cmd/server ./cmd/healthcheck`，预期全部通过。
- [ ] 在 `E:/MiniMax-WorkFlow/Minimax-H3` 运行 Node 定向测试，预期通过。
- [ ] 主机具备 CGO 和 C 编译器时运行 `go test -race ./internal/store/sqlite ./internal/orchestrator ./internal/upstream/registry`；不具备时在 `TEST_ACCEPTANCE.md` 记录为待专用环境确认。
- [ ] 进行规格符合性审查，再进行代码质量审查；修复所有阻塞问题后重新运行受影响测试。

### 3.7 DOC-009：文档同步

- [ ] 将自动化命令和结果写回 `TEST_ACCEPTANCE.md`，不得把真实 Node、多环境、性能或发布验收标记为完成。
- [ ] 根据最终迁移和接口实现同步 `DATABASE_DELTA.md`、`API_DELTA.md` 和 `TECH_SOLUTION.md`。
- [ ] 更新 `changes/README.md` 的状态和备注；开发完成但未人工联调时保持 `In Progress`。

## 4. 完成标准

- [ ] 本地任务中止、Node 屏障和 callback 事务一致。
- [ ] 取消失败或重启不会丢失屏障，也不会向同一 Node 提交新任务。
- [ ] 其他健康 Node 能继续执行后续任务。
- [ ] 单 Node 无法取消时，后续任务保持真实排队。
- [ ] 定向测试、全量测试、静态检查和构建通过。
- [ ] 代码审查完成并修复阻塞问题。
- [ ] 文档已同步；真实 Node 联调仍标记为待人工确认。

## 5. 下游触发点

- `/s-develop`：使用 `test-driven-development` 先写失败回归测试，按 `systematic-debugging` 验证根因，完成后使用 `requesting-code-review`。
- `/s-archive`：人工验收完成后使用 `verification-before-completion` 和 `finishing-a-development-branch` 检查归档条件。
