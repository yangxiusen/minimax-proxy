# API 接口

## 分类目标

定义对外 MiniMax H3 V2 兼容接口及内部 MiniMax-H3 Gradio 适配契约。

## `v0.0.1` 接口范围

- `POST /v2/video_generation`：创建任务。
- `GET /v2/query/video_generation/{task_id}`：查询单个任务和结果地址。
- `GET /v2/query/video_generation`：分页查询当前 API Key 最近 7 天任务。
- `DELETE /v2/video_generation/{task_id}`：取消普通排队任务或删除成功/失败记录。

## 关键约定

对外使用官方 V2 JSON、Bearer API Key 和 OpenAI 风格错误结构。标准状态为 `queued/running/succeeded/failed/cancelled`；成功结果在 `task.content.url` 返回。不提供 H3-Context-IR、视频再生成或回调。队首保护区内任务对外仍为 `queued`，但取消请求会被拒绝。

详细字段、响应和状态规则见 [`API_INTERFACE.md`](../../developing/version_0.0.1/API_INTERFACE.md)。
