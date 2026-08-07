# 任务生命周期接口设计

## 模块说明

- 业务目标：查询 V2 任务、列出最近 7 天记录，并按状态取消或删除。
- 核心表：`video_tasks`、必要时 `idempotency_keys`。
- 权限边界：所有 SQL 同时过滤 `task_id/api_key_id/deleted_at`。
- 状态映射：内部 queued 两态均返回 `queued`；dispatching/running/reconciling 返回 `running`。

## 接口列表

| 接口名称 | 方法 | URL |
| --- | --- | --- |
| 查询单个任务 | GET | `/v2/query/video_generation/{task_id}` |
| 查询任务列表 | GET | `/v2/query/video_generation` |
| 取消或删除任务 | DELETE | `/v2/video_generation/{task_id}` |

## 1. 查询单个任务

- Content-Type：无请求体；响应 `application/json`。
- 认证：Bearer API Key。

Path 参数：

| 字段 | 类型 | 必填 | 规则 |
| --- | --- | --- | --- |
| `task_id` | string | 是 | 1-64 字符 |

成功响应，生成中：

```json
{
  "task": {
    "id": "424010985738629",
    "model": "MiniMax-H3",
    "status": "running",
    "created_at": 1785125529,
    "updated_at": 1785125700,
    "resolution": "2K",
    "duration": 5,
    "usage": {
      "total_seconds": 0,
      "input_seconds": 0,
      "output_seconds": 0,
      "input_image_count": 0
    },
    "ratio": "16:9",
    "task_type": "generation",
    "modality": "video"
  }
}
```

成功响应，已完成：

```json
{
  "task": {
    "id": "424010985738629",
    "model": "MiniMax-H3",
    "status": "succeeded",
    "created_at": 1785125529,
    "updated_at": 1785125946,
    "content": {
      "url": "https://video-1.example.com/gradio_api/file=/outputs/a.mp4"
    },
    "resolution": "2K",
    "duration": 5,
    "usage": {
      "total_seconds": 5,
      "input_seconds": 0,
      "output_seconds": 5,
      "input_image_count": 0
    },
    "ratio": "16:9",
    "task_type": "generation",
    "modality": "video"
  }
}
```

失败任务增加：

```json
{
  "error": {
    "code": "result_ambiguous",
    "message": "unable to identify a unique generated video"
  }
}
```

此对象位于 `task.error`，不替换完整任务响应。

不存在、跨 Key、超过 7 天或已删除统一返回：

```http
HTTP/1.1 400 Bad Request
```

```json
{
  "type": "error",
  "error": {
    "type": "bad_request_error",
    "message": "invalid task_id (2001)",
    "http_code": "400"
  },
  "request_id": "01J..."
}
```

接口逻辑：单 SQL 按 `task_id + api_key_id + deleted_at IS NULL + created_at >= now-retention` 查询，映射内部状态，按终态有选择地输出 `content/error`。`result_internal_url` 永不进入响应。

涉及字段：`video_tasks` 除 `request_json/request_hash/gallery_before_json/result_internal_url` 外的公开映射字段。

数据变更/事务/缓存：无。

## 2. 查询任务列表

Query 参数：

| 字段 | 类型 | 必填 | 默认 | 规则 |
| --- | --- | --- | --- | --- |
| `page_num` | integer | 否 | 1 | >=1 |
| `page_size` | integer | 否 | 20 | 1-100 |
| `filter.status` | string | 否 | - | V2 五种状态 |
| `filter.task_ids` | string[] | 否 | - | 重复 query 参数，最多 100 个 |
| `filter.model` | string | 否 | - | 仅 `MiniMax-H3` 有结果 |
| `filter.task_type` | string | 否 | - | 仅 `generation` 有结果；其他官方值返回空列表 |

示例：

```http
GET /v2/query/video_generation?page_num=1&page_size=20&filter.status=queued&filter.task_ids=id1&filter.task_ids=id2
```

成功响应：

```json
{
  "items": [
    {
      "id": "424635601932587",
      "model": "MiniMax-H3",
      "status": "queued",
      "created_at": 1785225940,
      "updated_at": 1785226100,
      "resolution": "2K",
      "duration": 8,
      "usage": {
        "total_seconds": 0,
        "input_seconds": 0,
        "output_seconds": 0,
        "input_image_count": 0
      },
      "ratio": "9:16",
      "task_type": "generation",
      "modality": "video"
    }
  ],
  "total": 1
}
```

接口逻辑：

1. 严格解析带点号参数；未知 query 参数返回 400。
2. 强制 `api_key_id`、7 天窗口和 `deleted_at IS NULL`。
3. 外部 status 展开为内部状态集合，例如 running 对应三种内部状态。
4. 先 COUNT，再按 `created_at DESC, queue_seq DESC` 使用 LIMIT/OFFSET 查询。
5. 空结果返回 `items: []` 和 `total: 0`。

数据变更/事务/缓存：无；两条 SQL 不要求快照严格一致，列表是观察性接口。

## 3. 取消或删除任务

- URL：`DELETE /v2/video_generation/{task_id}`。
- 认证：Bearer API Key。
- Content-Type：无请求体。

取消成功：

```json
{
  "task_id": "424010985738629",
  "action": "cancelled",
  "status": "cancelled"
}
```

删除成功：

```json
{
  "task_id": "424010985738629",
  "action": "deleted",
  "status": "deleted"
}
```

不可操作：

```http
HTTP/1.1 400 Bad Request
```

```json
{
  "type": "error",
  "error": {
    "type": "bad_request_error",
    "message": "task cannot be cancelled or deleted in its current state (2021)",
    "http_code": "400"
  },
  "request_id": "01J..."
}
```

状态规则：

| 内部状态 | 操作 |
| --- | --- |
| `queued_open` | 取消，返回 cancelled |
| `queued_locked` | 拒绝，防白嫖保护 |
| `dispatching/running/reconciling` | 拒绝 |
| `succeeded/failed` | 逻辑删除，返回 deleted |
| `cancelled`、已删除 | 拒绝 |

接口逻辑：

1. `BEGIN IMMEDIATE`，按 task_id + api_key_id 查询；找不到统一 invalid task_id。
2. 按状态执行条件 UPDATE；受影响行数必须为 1。
3. 取消时重算保护位；删除时设置 `deleted_at/updated_at/version`。
4. 删除后关联幂等键仍可保留到自身 TTL，但重放时发现任务不可见应创建新任务并替换过期映射，具体由仓储原子处理。
5. 提交后发送调度唤醒。

事务：必须，防止 Worker 领取与取消同时成功。

审计日志：成功与拒绝均记录 task_id、api_key_id、旧状态、action/error_code；不记录请求内容。

人工联调准备：

- [ ] 验证 V2 五种状态字段与官方示例一致。
- [ ] 验证队首保护任务对外仍为 queued，但 DELETE 被拒绝。
- [ ] 验证跨 Key、过期和删除任务不会泄露存在性。

