# 管理任务生命周期接口设计

## 模块说明

- 业务目标：为监控台提供可靠的任务中止、终态删除和视频访问。
- 核心表：`video_tasks`、`idempotency_keys`。
- 依赖组件：现有管理会话、Worker 和私有 Jobs API。
- 权限边界：有效管理会话。

## 接口列表

| 接口名称 | 方法 | URL |
| --- | --- | --- |
| 管理任务列表 | GET | `/monitor/api/tasks` |
| 中止任务 | POST | `/monitor/api/tasks/{task_id}/cancel` |
| 删除任务 | DELETE | `/monitor/api/tasks/{task_id}` |

## 1. 管理任务列表

认证：现有 `monitor_session` Cookie。Query 参数保持 `page_num/page_size/status/upstream_id/search` 不变。

成功响应新增字段：

```json
{
  "items": [{
    "id": "371369477743401442",
    "status": "running",
    "phase": "recovering",
    "retry_count": 0,
    "can_cancel": true,
    "can_delete": false,
    "video_url": null
  }],
  "total": 18,
  "page_num": 1,
  "page_size": 10
}
```

校验与逻辑：`phase` 由内部状态和 `retry_count` 映射；`can_cancel` 仅活动状态为真；`can_delete` 仅 `cancelled/succeeded/failed` 为真；`video_url` 仅成功且存在 `result_public_url` 时返回。读取 `video_tasks`，无事务、无缓存写入。

## 2. 中止任务

URL：`POST /monitor/api/tasks/{task_id}/cancel`

Content-Type：无需请求体。认证：现有管理会话。

成功响应：HTTP 202 Accepted。

```json
{"action":"cancel_requested","task_id":"371369477743401442"}
```

错误响应：

```json
{"error":{"type":"task_not_found","message":"任务不存在"}}
```

```json
{"error":{"type":"task_not_operable","message":"当前状态不能中止"}}
```

接口逻辑：

1. 校验任务 ID并读取未删除任务。
2. `queued_open/queued_locked` 原子更新为 `cancelled` 并重算队列。
3. `dispatching/running/reconciling` 原子更新为 `cancelling`。
4. 唤醒对应 Worker；网络中止由 Worker 在事务外执行。
5. 记录不含敏感数据的中文结构化日志。

数据变更：更新 `video_tasks.status/cancel_requested_at/finished_at/version`。需要事务，原因是状态校验、队列重算和写入必须原子完成。无 MQ；副作用为 Worker 唤醒和监控刷新。

## 3. 删除任务

URL：`DELETE /monitor/api/tasks/{task_id}`

认证：现有管理会话。成功响应：HTTP 204 No Content。

只允许 `cancelled/succeeded/failed`。其他状态返回 HTTP 409 `task_not_operable`；不存在返回 404。事务内写 `video_tasks.deleted_at` 并删除该任务的 `idempotency_keys`，不删除上游视频或历史。

## 4. 人工联调准备

- [ ] 确认中止确认框文案和 202 后的“中止中”状态刷新。
- [ ] 使用真实私有服务验证运行中取消和服务离线时本地收口。
- [ ] 验证成功任务播放只访问公网 URL，且新标签页不持有 opener。
