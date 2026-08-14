# 运行中任务中止一致性 PRD 增量

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 目标版本 | `version_0.0.1` |
| 变更编号 | `009-running-task-cancellation-consistency` |
| 文档类型 | `PRD_DELTA.md` |

## 2. 变更背景

Manager 允许管理员中止运行中的任务，但本地任务结束不代表 Node execution 已停止。提前释放 Node 会让旧任务继续占用 GPU，后续任务在 Node 内排队，Proxy 与 Node 展示的运行状态发生分歧。

## 3. 本次目标

- 中止任务不再占据全局 FIFO，也不会长期显示“中止中”。
- 未确认停止的旧 execution 只隔离其所在 Node，不影响其他健康 Node 执行后续任务。
- 单 Node 无法取消时如实保持后续任务排队，不制造“已运行”的假状态。

## 4. 变更范围

### 4.1 本次包含

- Manager 对运行中 H3 Node 任务的管理员中止流程。
- Proxy 对 Node execution 取消、查询、重启恢复和调度隔离规则。
- 多 Node 继续调度及单 Node 容量边界。

### 4.2 本次不包含

- 普通 API 调用方取消运行中任务的权限变化。
- Manager 页面结构、操作入口或新状态筛选。
- 人工强制清空 Node 队列或远程重启 Node。

## 5. 需求增量说明

### 5.1 变更前

- 管理员中止本地任务后，Node 可能仍运行旧 execution。
- 本地释放执行槽后可向同一 Node 提交新任务，造成后续任务在 Node 排队。

### 5.2 变更后

- 管理员中止后，本地任务立即进入 `cancelled` 并退出全局 FIFO。
- 存在远端 execution 风险时，Proxy 为对应 Node 建立持久化调度屏障。
- 屏障期间该 Node 只执行取消对账，不领取新任务；其他健康 Node 继续按既有 FIFO 领取可执行任务。
- Node 确认 execution 终态或 execution 不存在且队列为空后，自动解除屏障。

### 5.3 角色与场景

| 角色 | 场景 | 目标行为 |
| --- | --- | --- |
| 管理员 | 中止运行中任务 | 操作被接受，任务不再恢复或重新提交 |
| API 调用方 | 等待中止任务之后的任务 | 有其他健康 Node 时继续执行；无可用 Node 时保持真实排队 |
| 部署运维人员 | Node 取消失败或离线 | Manager 显示 Node 异常，恢复后自动对账，不需修改任务数据 |

## 6. 验收标准

- Given 两个健康 Node，When Node A 的运行任务中止但取消暂时失败，Then Node B 继续执行后续任务。
- Given Node A 的取消屏障存在，When Proxy 重启，Then Node A 仍不可领取新任务并继续对账。
- Given 取消确认完成，When Node A 再次参与调度，Then 不再恢复被中止的旧任务。
- Given 仅有 Node A 且取消失败，When 后续任务入队，Then 状态保持 `queued`，Manager 明确显示 Node 异常。

## 7. 关联文档

- `CHANGE_SPEC.md`
- `TECH_SOLUTION.md`
- `DATABASE_DELTA.md`
- `API_DELTA.md`
- `TEST_ACCEPTANCE.md`
- `task.md`
