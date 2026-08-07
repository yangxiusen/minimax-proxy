# 一、接口列表集合

## 1. 视频生成

模块说明：接收 MiniMax H3 V2 多模态请求，校验并创建本地异步任务。

| 接口名称 | 方法 | URL | 简要说明 |
| --- | --- | --- | --- |
| 创建视频生成任务 | POST | `/v2/video_generation` | 支持 t2va、i2va、r2va，返回 task_id |

## 2. 任务生命周期

模块说明：按 API Key 查询最近 7 天任务，取消普通排队任务或删除成功/失败记录。

| 接口名称 | 方法 | URL | 简要说明 |
| --- | --- | --- | --- |
| 查询单个任务 | GET | `/v2/query/video_generation/{task_id}` | 成功任务在 `task.content.url` 返回视频地址 |
| 查询任务列表 | GET | `/v2/query/video_generation` | 官方分页与过滤参数 |
| 取消或删除任务 | DELETE | `/v2/video_generation/{task_id}` | 按状态取消或逻辑删除 |

# 二、模块功能说明

## 1. 视频生成

1. 严格解析官方 V2 `model/content/resolution/duration/ratio`。
2. 识别文生、图生或多模态参考场景并生成 Gradio 32 位参数。
3. 在 SQLite 事务中执行幂等、额度检查、入队和保护位重算。

核心实体：`video_tasks`、`idempotency_keys`。

权限边界：Bearer API Key；任务写入当前 Key 的 `api_key_id`。

## 2. 任务生命周期

1. 单查与列表只返回当前 Key 最近 7 天、未逻辑删除的任务。
2. 内部细状态统一映射为 V2 小写状态。
3. 仅 `queued_open` 可取消；`succeeded/failed` 可逻辑删除。

核心实体：`video_tasks`。

权限边界：任务 ID 与 API Key 所有权同时过滤，不泄露跨 Key 资源存在性。

# 三、接口文档索引

| 模块 | 文档路径 | 接口数量 | 说明 |
| --- | --- | ---: | --- |
| 视频生成 | [`api-modules/video-generation.md`](api-modules/video-generation.md) | 1 | V2 请求、校验、幂等、入队 |
| 任务生命周期 | [`api-modules/task-lifecycle.md`](api-modules/task-lifecycle.md) | 3 | 单查、列表、取消/删除 |

官方 V2 资料位于 `docs/MiniMax-H3-V2/`，仅作为设计依据，运行时代码不得读取或依赖该目录。
