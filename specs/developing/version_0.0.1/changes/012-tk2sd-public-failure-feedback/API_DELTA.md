# V2 任务失败响应变化

- 接口：`GET /v2/query/video_generation/{task_id}` 与 `GET /v2/query/video_generation`。
- 字段结构不变。tk2sd 任务状态为 `failed` 且存在安全上游反馈时，`error.message` 返回反馈消息，例如 `video audit rejected (reason=video audit rejected)`。
- `error.code` 仍为 Proxy 的稳定错误码；没有反馈、反馈被脱敏或不适合公开时，`error.message` 保留原通用文案。
- 官方协议的错误码本地化不变；本变更对已有已持久化反馈的失败任务生效，不需要数据库迁移。
