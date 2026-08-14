# 模型服务节点配置接口设计

> 现行凭据契约由 `006-node-single-key-conf/API_DELTA.md` 定义：请求仅使用精确 32 位字母数字 `api_key`，响应不返回 Key 明文，`api_key_id` 为废弃未知字段。本文后续旧示例仅保留为历史记录。

## 1. 节点对象

```json
{
  "id": "minimax-private-1",
  "service_url": "http://127.0.0.1:7860",
  "protocol_version": "h3-node-v1",
  "api_key_id": "proxy-primary",
  "api_key_configured": true,
  "api_key_fingerprint": "sha256:abcd1234",
  "poll_interval": "3s",
  "request_timeout": "30s",
  "enabled": true,
  "version": 2,
  "created_at": 1786420800,
  "updated_at": 1786420900
}
```

| 字段 | 规则 |
| --- | --- |
| `id` | 1-64 位，`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`，创建后不可复用 |
| `service_url` | HTTP/HTTPS 基础 URL，无凭据、查询参数或片段 |
| `protocol_version` | `legacy-gradio-v1` 或 `h3-node-v1`；新建只接受 H3 |
| `api_key_id` | H3 必填，1-64 位，`^[A-Za-z0-9_-]+$` |
| `api_key` | 只在请求出现，32-512 位原始 Secret，响应永不返回 |
| `poll_interval` | `1s` 至 `5m` |
| `request_timeout` | `1s` 至 `5m` |
| `enabled` | 布尔值 |
| `version` | 更新和删除使用的乐观锁版本 |

## 2. 查询节点

`GET /manager/api/nodes`

返回全部未软删除节点，按 ID 排序。Legacy 节点必须返回真实协议、`api_key_configured=false` 和空 Key ID，页面据此进入只读待升级态。

## 3. 新增 H3 节点

`POST /manager/api/nodes`

```json
{
  "id": "minimax-private-2",
  "service_url": "http://127.0.0.1:7861",
  "protocol_version": "h3-node-v1",
  "api_key_id": "proxy-primary",
  "api_key": "a-random-secret-with-at-least-32-characters",
  "poll_interval": "3s",
  "request_timeout": "30s",
  "enabled": true
}
```

创建要求 Key ID 和 Secret 同时存在。成功返回 `201` 与完整节点对象；Secret 加密后再落库，响应不回显。

## 4. 更新或升级节点

`PUT /manager/api/nodes/{node_id}`

编辑已有 H3 节点时，`api_key` 可省略，仅当当前记录拥有完整密文/Nonce/指纹时才复用。更换 Key ID 但不提供新 Secret 是非法组合，返回 `node_api_key_required`。

Legacy 升级请求示例：

```json
{
  "service_url": "http://127.0.0.1:7860",
  "protocol_version": "h3-node-v1",
  "upgrade_protocol": true,
  "api_key_id": "proxy-primary",
  "api_key": "a-random-secret-with-at-least-32-characters",
  "poll_interval": "3s",
  "request_timeout": "30s",
  "enabled": false,
  "version": 1
}
```

升级前节点必须无活动任务。`upgrade_protocol` 只允许 Legacy 到 H3；普通 PUT 不得隐式转换，H3 不允许降级。成功返回 `200`、版本加一并唤醒 Registry。

Legacy 普通编辑仅允许启停，后端以当前记录补齐其历史连接字段，不要求页面发送或修改这些字段。

## 5. 删除节点

`DELETE /manager/api/nodes/{node_id}?version=2`

规则保持不变：节点必须停用且没有活动任务，成功返回 `204` 并软删除。

## 6. 测试节点

`POST /manager/api/nodes/test`

新节点测试携带草稿 Key ID 和 Secret；已有 H3 节点可传 `use_stored_api_key=true`，此时不能同时传 `api_key`。Legacy 节点不能使用已保存密钥测试 H3。

成功响应：

```json
{
  "reachable": true,
  "authenticated": true,
  "protocol_version": "h3-node-v1",
  "checks": [
    {"name": "health", "status": "passed"},
    {"name": "capabilities", "status": "passed"},
    {"name": "required_scopes", "status": "passed"}
  ],
  "capabilities": {"capabilities_revision": "..."}
}
```

失败仍返回 `502 node_probe_failed`，`checks[].error_code` 只能是稳定值，例如 `node_unreachable`、`node_authentication_failed`、`node_protocol_mismatch`、`node_scope_insufficient`。缺失 scope 可以返回名称列表，但不得返回 Secret、完整 Token、私有响应体或节点堆栈。

## 7. 事务与安全

- 创建、更新和删除的校验与 Store 写入无网络调用；探测是独立只读请求。
- 更新先读取当前记录并完成协议/凭据交叉校验，再进入同一事务执行版本、活动任务和写入检查。
- SQLite CHECK 仅是数据完整性后盾；所有可预期输入错误必须在 Handler/领域层映射为 400。
- 结构化日志仅记录 `node_id/action/error_code/key_fingerprint/request_id`，不得记录 URL、Secret、Token、密文或 Node details。
