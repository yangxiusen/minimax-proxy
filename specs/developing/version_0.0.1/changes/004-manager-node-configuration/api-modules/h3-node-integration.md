# H3 Node API 集成接口设计

> 现行认证为 `Authorization: Bearer <32位api_key>`，一个 Key可访问全部受保护 Node 路由。以下 Key ID、scope 和复合 Token 描述已由 `006-node-single-key-conf` 取代，仅保留为历史记录。

## 1. 认证

Proxy 从 SQLite 读取 `api_key_id` 并解密 Secret，以结构化 `Credentials` 传入客户端。客户端请求头固定为 `Authorization: Bearer <key_id>.<secret>`。Key ID 或 Secret 缺失时禁止创建运行时节点。

必需 scopes：`health`、`execute`、`artifact:read`、`artifact:write`、`artifact:delete`。`maintenance` 不属于 Proxy v0.0.1 权限集。

## 2. 执行与幂等

`POST /executions` 的请求体 `operation_id` 必须与 `Idempotency-Key` 相同。Proxy 在收到确定响应前永久关联该 operation 与 stage attempt；传输结果未知时重发完全相同的 operation，不创建新 attempt。

收到 `execution_id` 后写入 attempt，再调用 `GET /executions/{id}`。短暂网络失败保留 execution 关联并延迟重试，不能把阶段恢复为全新 pending。

## 3. 取消

任务出现取消请求且 attempt 已绑定 execution 时，调用：

```http
POST /internal/v1/executions/{execution_id}/cancel
Idempotency-Key: cancel:<attempt_id>
```

重复取消使用同一个幂等键。响应为 202 后继续查询原 execution，直至 `cancelled`、`succeeded` 或 `failed`。取消请求与终态竞争以 Node 返回的真实终态为准。

## 4. 制品

- 生成输入需要本地化时调用 `/artifacts/import`，operation ID 固定关联逻辑制品和目标节点。
- 阶段成功后先读取 `/artifacts/{id}` 并校验 kind、state、size、SHA256 和 media manifest，再注册 Proxy 逻辑制品。
- 下载和跨节点迁移通过 `/artifacts/{id}/content`，仅支持单 Range 并限制传输大小。
- Proxy 清理 Worker 只对已选中的位置调用 `/artifacts/delete`，不调用 Node maintenance cleanup。

## 5. 错误分类

| 条件 | Proxy 行为 |
| --- | --- |
| 401/403 | 节点配置错误，停止当前重试并把节点标为认证异常 |
| 400/404/409/422 且 `retryable=false` | 确定性失败，不创建新 attempt |
| 429 或 `retryable=true` | 保留当前 operation/execution，退避重试 |
| 5xx/连接中断 | 标记 unknown，使用同一关联恢复 |
| 未知响应状态/缺少必需字段 | `node_protocol_error`，停止自动重提交流程 |

`HTTPError` 保存 Node `request_id` 供关联排障，但对外只返回 Proxy 稳定错误；Node details 只参与受控分类。

## 6. 契约验证

Node CI 从 FastAPI OpenAPI 生成归一化契约；Proxy CI 验证本模块列出的 9 条消费路由。测试至少覆盖认证头、幂等键、全部状态枚举、统一错误包、未知可选响应字段兼容以及 12 条发布路由摘要无漂移。
