# 管理节点与 H3 Node API 接口增量

> 鉴权和 Manager 凭据字段以 `006-node-single-key-conf/API_DELTA.md` 为现行契约；本文涉及 Key ID、scope、`key_id.secret` 的内容仅为已废弃历史记录。

## 1. Manager 接口集合

| 接口名称 | 方法 | URL | 修订重点 |
| --- | --- | --- | --- |
| 查询节点 | GET | `/manager/api/nodes` | 返回真实协议和密钥配置状态，不返回 Secret |
| 新增 H3 节点 | POST | `/manager/api/nodes` | Key ID + Secret 必填 |
| 更新/升级节点 | PUT | `/manager/api/nodes/{node_id}` | Legacy 转 H3 要求 `upgrade_protocol=true` 和新 Secret |
| 删除节点 | DELETE | `/manager/api/nodes/{node_id}` | 规则不变：已停用且无活动任务 |
| 测试节点 | POST | `/manager/api/nodes/test` | 验证认证、协议、能力和必需 scopes |

Manager 会话、快照和任务接口继续位于 `/manager/api/*`；公共 `/v2` 路径和响应不因本修订变化。

## 2. Manager 通用契约

- JSON 请求限制 64 KiB，严格拒绝未知、重复和尾随字段。
- 所有节点响应使用 `Cache-Control: no-store`。
- `api_key` 表示 MiniMax-H3 配置中生成摘要前的原始 Secret，不接受已经拼接的 `key_id.secret`。
- `api_key_id` 使用 `^[A-Za-z0-9_-]+$`，长度 1 至 64；`api_key` 长度 32 至 512 且前后无空白。
- 响应只返回 `api_key_configured` 和脱敏指纹，不返回 Secret、密文、Nonce 或完整 Bearer Token。
- 详细字段、示例和流程见 `api-modules/manager-nodes.md`。

### 2.1 稳定错误

| HTTP | `error.type` | 场景 |
| --- | --- | --- |
| 400 | `bad_request_error` | JSON、URL、时长或普通字段无效 |
| 400 | `node_api_key_id_invalid` | Key ID 不符合 Node 规则 |
| 400 | `node_api_key_required` | 创建或 Legacy 升级缺少新 Secret；H3 记录无可复用密钥 |
| 400 | `node_protocol_upgrade_required` | Legacy 节点被普通 PUT 隐式改为 H3 |
| 400 | `node_protocol_downgrade_forbidden` | H3 节点请求降级到 Legacy |
| 401 | `authentication_error` | Manager 未登录或会话失效 |
| 404 | `node_not_found` | 节点不存在或已删除 |
| 409 | `node_id_conflict` | 节点 ID 已使用，包括软删除记录 |
| 409 | `node_version_conflict` | 乐观锁版本冲突 |
| 409 | `node_has_active_task` | 活动任务阻止连接修改或删除 |
| 409 | `node_must_be_disabled` | 删除启用节点 |
| 502 | `node_probe_failed` | 节点不可达、认证、协议、能力或 scope 检查失败 |
| 503 | `master_key_missing` | Proxy 未配置节点密钥主密钥 |
| 500 | `server_error` | 非预期内部错误；不得用于上述确定性输入问题 |

错误响应保持现有 Manager 结构：

```json
{
  "error": {
    "type": "node_api_key_required",
    "message": "升级到 H3 Node API 时必须填写新密钥"
  }
}
```

## 3. MiniMax-H3 Node API 消费集合

| 方法 | 路径 | Scope | Proxy 消费方 |
| --- | --- | --- | --- |
| GET | `/internal/v1/health` | `health` | Registry、Manager 测试 |
| GET | `/internal/v1/capabilities` | `health` | Registry、Manager 测试 |
| POST | `/internal/v1/executions` | `execute` | Orchestrator、Profile Test |
| GET | `/internal/v1/executions/{execution_id}` | `execute` | Orchestrator、Profile Test |
| POST | `/internal/v1/executions/{execution_id}/cancel` | `execute` | Orchestrator、Profile Test |
| POST | `/internal/v1/artifacts/import` | `artifact:write` | Input Materializer/Migrator |
| GET | `/internal/v1/artifacts/{artifact_id}` | `artifact:read` | Orchestrator |
| GET | `/internal/v1/artifacts/{artifact_id}/content` | `artifact:read` | Artifact Service/Migrator |
| POST | `/internal/v1/artifacts/delete` | `artifact:delete` | Cleanup Worker |

请求统一携带：

```http
Authorization: Bearer <key_id>.<secret>
X-Request-Id: <proxy_request_id>
Idempotency-Key: <operation_id>
```

`Idempotency-Key` 仅用于 Node 定义为幂等写入的接口，并必须与请求体 `operation_id` 一致。

### 3.1 Node 统一错误包

认证、业务和 Pydantic 请求校验错误统一为：

```json
{
  "error": {
    "code": "request_validation_failed",
    "message": "请求参数无效",
    "retryable": false,
    "request_id": "req_xxx",
    "details": {"fields": ["parameters.duration_seconds"]}
  }
}
```

Proxy 保留 `code/retryable/request_id` 用于分类与关联；`details` 不进入用户响应或普通日志。未返回统一包的 4xx 默认不可重试，429 和 5xx按受限兜底规则处理。

### 3.2 Capabilities 授权扩展

Node 的 `GET /internal/v1/capabilities` 增加：

```json
{
  "protocol_version": "h3-node-v1",
  "authorization": {
    "key_id": "proxy-primary",
    "scopes": ["artifact:delete", "artifact:read", "artifact:write", "execute", "health"]
  }
}
```

Manager 测试必须确认所需五类 scope 全部存在；`maintenance` 不属于 Proxy 必需权限。

## 4. 有意不消费的 Node 接口

`POST /maintenance/cleanup/preview`、`POST /maintenance/cleanup` 和 `GET /maintenance/cleanup/{operation_id}` 保留在 Node 发布契约中，但 Proxy v0.0.1 不调用。Proxy 使用自己的 `artifact_deletion_jobs/items` 确定候选集，再调用 `/artifacts/delete` 删除明确位置，避免绕过任务归属与审计。

## 5. 文档索引

| 模块 | 文档 | 说明 |
| --- | --- | --- |
| Manager 节点配置 | `api-modules/manager-nodes.md` | CRUD、显式升级、连接测试 |
| Node 跨项目契约 | `api-modules/h3-node-integration.md` | 认证、执行、取消、错误和幂等 |
| 审计结果 | `NODE_API_CONTRACT_AUDIT.md` | 12 路由覆盖与问题清单 |

## 6. Node 健康运行数据扩展

`GET /internal/v1/health` 增加向前兼容的可选字段：

```json
{
  "runtime": {
    "queue_running": 0,
    "queue_pending": 0,
    "memory_total_bytes": 100555894784,
    "memory_free_bytes": 10751811584,
    "vram_total_bytes": 17170956288,
    "vram_free_bytes": 10241762248,
    "cpu_percent": null,
    "gpu_percent": null
  }
}
```

- 队列来自 ComfyUI `/queue`；容量来自 `/system_stats`。
- CPU/GPU 百分比只有真实采集到时才返回，不能用内存/显存占用率代替。
- Proxy 对缺失 `runtime` 的旧 Node 保持兼容；字段缺失时 Manager 显示未知。
- H3 `service_url` 必须指向 Node API 根地址，路径为空或 `/`；`/ui` 是页面路径，作为 API 根地址时返回 400。

## 7. 任务结果与播放接口修订

详细契约见 `api-modules/task-delivery.md`。

| 接口名称 | 方法 | URL | 修订重点 |
| --- | --- | --- | --- |
| 查询单个生成任务 | GET | `/v2/query/video_generation/{task_id}` | 成功任务 `content.url` 改为绝对 Proxy 签名 URL |
| 查询生成任务列表 | GET | `/v2/query/video_generation` | 每个成功任务返回相同格式的绝对 URL |
| Manager 任务列表 | GET | `/manager/api/tasks` | 新 H3 artifact 任务返回 `video_url`，供播放按钮使用 |
| 获取视频内容 | GET | `/v2/files/{artifact_id}/content` | 路径与鉴权不变，浏览器播放继续支持 Range |

### 7.1 URL 来源

成功响应示例：

```json
{
  "task": {
    "id": "272076440301631191",
    "status": "succeeded",
    "content": {
      "url": "http://127.0.0.1:18081/v2/files/art_d6263610cd6a8a48cc6d306465aa7190/content?expires=1786618995&signature=..."
    }
  }
}
```

`http://127.0.0.1:18081` 来自可信配置 `server.public_base_url`。`http://127.0.0.1:7860` 是 Node API 内部地址；该服务没有 `/v2/files` 路由，且访问内部 artifact 需要 Node Key，因此禁止将相对路径拼到 7860。

### 7.2 兼容与错误

- 字段名、查询路径、Bearer 鉴权、签名参数和文件访问错误结构不变。
- 缺少或非法 `server.public_base_url` 属于启动配置错误，不允许服务带病启动。
- 单次签名失败时查询返回现有 500 `server_error`，日志只记录 request/task/artifact ID 和稳定错误类型，不记录 URL 签名或节点地址。
- Manager `video_url` 仅对 `succeeded` 且可签发/兼容访问的任务非空。

## 8. 文档索引补充

| 模块 | 文档 | 说明 |
| --- | --- | --- |
| 任务交付与播放 | `api-modules/task-delivery.md` | FIFO 相关读取边界、绝对结果 URL、Manager 播放和文件内容接口 |
