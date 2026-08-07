# 监控控制台接口增量

## 1. 鉴权与通用规则

- 管理接口统一位于 `/monitor/api/`，使用 HttpOnly、SameSite=Strict 会话 Cookie。
- 未登录返回 401；无权限写操作不存在。响应为 UTF-8 JSON，禁止缓存敏感响应。
- 登录失败使用统一提示，不区分账号或密码错误；记录脱敏的安全日志。

## 2. 接口列表

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/monitor/api/session` | 账号密码登录 |
| DELETE | `/monitor/api/session` | 退出并销毁会话 |
| GET | `/monitor/api/snapshot` | 全局指标和全部节点快照 |
| GET | `/monitor/api/tasks` | 管理员范围的任务列表 |

## 3. 会话接口

请求：`{"username":"admin","password":"***"}`。成功返回 204 并设置 Cookie；失败返回 401：

```json
{"error":{"type":"authentication_error","message":"账号或密码错误"}}
```

## 4. 节点快照

```json
{
  "updated_at": 1786073770,
  "stale_after_seconds": 15,
  "summary": {"healthy": 2, "unhealthy": 1, "unknown": 0, "running": 1},
  "upstreams": [{
    "id": "gpu-1", "address": "127.0.0.1:7860",
    "health": "healthy", "runtime": "running", "private_queue": 0,
    "cpu_percent": 8, "memory_percent": 89,
    "gpu_percent": 42, "vram_percent": 64,
    "checked_at": 1786073770, "last_healthy_at": 1786073770,
    "current_task": {"id":"073478442647032693","status":"running","started_at":1786073636},
    "latest_finished_task": {"id":"0734784418","api_key_id":"customer-1","status":"succeeded","duration_seconds":291,"finished_at":1786070301},
    "last_error": null
  }]
}
```

资源或队列字段无法解析时为 `null`。`stale_after_seconds` 由采集周期计算，供页面判断缓存是否过期。`last_error` 只包含稳定错误码和脱敏摘要，不包含上游原始日志。

## 5. 管理任务列表

查询参数：`page_num` 默认 1；`page_size` 仅允许 10/20；`status`、`upstream_id` 可选；`search` 精确/前缀匹配任务 ID 或客户标识。响应：

```json
{"items":[{"id":"073478442647032693","api_key_id":"customer-1","status":"running","upstream_id":"gpu-1","scenario":"t2va","resolution":"768P","created_at":1786072877}],"total":128,"page_num":1,"page_size":10}
```

## 6. 创建任务新增错误分支

`POST /v2/video_generation` 在所有节点为 `unhealthy` 或 `unknown` 时返回：

```json
{"type":"error","error":{"type":"resource_unavailable_error","message":"资源不足，请稍后重试","http_code":"503"},"request_id":"..."}
```

该分支向后兼容原有成功响应；健康但忙碌的节点仍允许排队。
