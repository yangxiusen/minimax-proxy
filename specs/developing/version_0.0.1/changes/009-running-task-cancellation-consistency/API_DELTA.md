# 运行中任务中止接口行为增量

## 1. 变更概览

本变更不修改 Manager 与 H3 Node 对外路径、字段、鉴权或错误包，只补充两者之间的状态语义。Node 内部调用的 ComfyUI 单任务取消响应增加 `classification`，供 adapter 判断是否已经安全出队。

| 接口 | 方法 | 路径 | 变更类型 |
| --- | --- | --- | --- |
| 管理员中止任务 | POST | `/manager/api/tasks/{task_id}/cancel` | 行为语义收敛 |
| 取消 Node execution | POST | `/internal/v1/executions/{execution_id}/cancel` | Proxy 消费语义收敛 |
| 查询 Node execution | GET | `/internal/v1/executions/{execution_id}` | 屏障解除判定 |
| Node 健康 | GET | `/internal/v1/health` | execution 404 时的队列空闲判定 |

## 2. Manager 管理员中止

认证：既有 Manager 管理员会话。

成功响应保持不变：

```json
{
  "action": "cancel_requested",
  "task_id": "123456789"
}
```

HTTP `202 Accepted` 的精确定义：

- 本地取消事务已提交，任务不再参与 FIFO。
- 如存在远端 execution 风险，对应 Node 调度屏障已经与本地终态原子建立。
- 202 不承诺 Node 已完成物理中断；Node 是否可再次调度由屏障对账决定。

错误映射、Path 校验和 `Wake` 副作用保持不变。写操作继续使用 SQLite 事务。

## 3. H3 Node 取消消费规则

请求：

```http
POST /internal/v1/executions/{execution_id}/cancel
Authorization: Bearer <node-api-key>
Idempotency-Key: stage-cancel-<attempt_id>
```

Proxy 规则：

1. 每次重试使用同一 `Idempotency-Key`，不得创建新的取消 operation。
2. 202 响应可能为 `cancelling` 或终态；非终态必须继续查询原 execution。
3. 请求超时或 5xx 表示结果未知，保留屏障并重试，不能视为取消成功。
4. 401/403 保留屏障并把 Node 标记为认证异常。
5. 404 需结合健康接口的 `queue_running=0` 且 `queue_pending=0` 才能解除屏障。

Node 实现规则：

1. 同一取消 operation 的历史响应为 `cancelling` 时，允许幂等地重新检查并再次执行针对同一 job 的安全中断，不创建新的 operation。
2. 重新检查发现 execution 已终态时，更新该 operation 的缓存响应并返回真实终态。
3. execution 已标记 `cancelling` 后产生迟到结果时，不注册结果产物，清理临时输出并把 execution 收口为 `cancelled`。
4. generation adapter 必须按 `job_id` 调用 ComfyUI 单任务取消；`classification=pending` 表示已出队，可立即完成取消，`classification=running` 只表示已发中断，execution 保持 `cancelling` 并继续对账。
5. `cancelling` 对账遇到连接或协议异常时保持原状态；只有底层作业终止、失败、迟到成功，或队列与历史均确认 job 消失时才收口 `cancelled`。
6. Node 在提交 generation 前预分配并持久化 UUID，调用 ComfyUI `/prompt` 时通过 `prompt_id` 原样传入；返回 ID 不一致视为 adapter 契约错误并补偿取消。
4. 上述规则只修复实现语义，不改变 HTTP 202、请求头或响应字段。

## 4. execution 查询终态映射

| Node 结果 | Proxy 屏障行为 | 本地任务行为 |
| --- | --- | --- |
| `accepted/queued/running/validating/cancelling/unknown` | 保留并继续对账 | 保持 `cancelled` |
| `cancelled` | 解除 | 保持 `cancelled` |
| `failed` | 解除并记录终态竞争日志 | 保持 `cancelled` |
| `succeeded` | 解除并丢弃迟到产物 | 保持 `cancelled` |
| `execution_not_found` + Node 队列为空 | 解除 | 保持 `cancelled` |
| `execution_not_found` + Node 队列非空/未知 | 保留 | 保持 `cancelled` |

## 5. 兼容性和人工联调准备

- V2 客户端、Manager 前端和 Node 路由均无需改字段。
- Proxy 与 Node 必须使用支持现有取消接口及运行队列计数的 `h3-node-v1` 版本。
- 人工联调需覆盖运行中、Node 内排队、提交响应丢失、取消响应丢失、Proxy 重启和 execution 404 六类场景。
