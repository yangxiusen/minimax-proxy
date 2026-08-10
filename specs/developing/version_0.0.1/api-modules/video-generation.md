# 视频生成接口设计

## 模块说明

- 业务目标：按 MiniMax H3 V2 参数创建本地异步视频任务。
- 核心表：`video_tasks`、`idempotency_keys`。
- 依赖组件：配置、SQLite、调度唤醒器；创建响应不等待上游。
- 权限边界：Bearer API Key；任务归属当前 Key。

## 接口列表

| 接口名称 | 方法 | URL | 简要说明 |
| --- | --- | --- | --- |
| 创建视频生成任务 | POST | `/v2/video_generation` | 入队并返回 task_id |

## 1. 创建视频生成任务

### 基本信息

- URL：`POST /v2/video_generation`
- Content-Type：`application/json`
- 认证：`Authorization: Bearer <API_KEY>`
- 请求体上限：64 MB；首版仍只支持 HTTP/HTTPS 媒体 URL。

请求头：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `Authorization` | 是 | Bearer API Key |
| `Content-Type` | 是 | 固定 `application/json` |
| `Idempotency-Key` | 否 | 1-128 个可打印 ASCII 字符，24 小时内幂等 |
| `X-Request-Id` | 否 | 合法时沿用，否则服务端生成 |

Body 参数：

| 字段 | 类型 | 必填 | 规则 |
| --- | --- | --- | --- |
| `model` | string | 是 | 固定 `MiniMax-H3` |
| `content` | array | 是 | 1-16 项，恰好一个非空 text |
| `resolution` | string | 是 | `480P`、`768P`、`2K` |
| `duration` | integer | 是 | 4-15 |
| `ratio` | string | 场景相关 | `adaptive/21:9/16:9/4:3/1:1/3:4/9:16` |
| `callback_url` | string | 否 | 首版不支持；出现即 400 |
| `aigc_watermark` | boolean | 否 | 默认 false；true 时 400 |

`content` 元素：

| 字段 | 类型 | 条件 | 说明 |
| --- | --- | --- | --- |
| `type` | string | 必填 | `text/image_url/video_url/audio_url` |
| `text` | string | type=text | 1-7000 字符 |
| `image_url.url` | string | type=image_url | HTTP/HTTPS；其他官方来源首版拒绝 |
| `video_url.url` | string | type=video_url | HTTP/HTTPS |
| `audio_url.url` | string | type=audio_url | HTTP/HTTPS |
| `role` | string | 媒体条件必填 | `first_frame/last_frame/reference_image/reference_video/reference_audio` |

场景校验：

| 场景 | content | ratio |
| --- | --- | --- |
| t2va | 仅 text | 必填且不能 adaptive |
| i2va | text + 最多各 1 个 first/last frame | 强制按 adaptive 处理 |
| r2va | text + 最多 9 图/3 视频/3 音频参考 | 可省略，默认 adaptive |

- first/last 与任一 reference role 互斥。
- 单张无 role 图片按 first_frame 处理；其他媒体缺 role 返回 400。
- 类型与 role 必须匹配：video 只能 reference_video，audio 只能 reference_audio。
- 首版结构校验文件扩展名和 URL，不主动下载探测大小、编码、分辨率或时长；真实限制由上游联调验证。
- `mm_file://`、Data URI 返回 `unsupported_media_source`，不尝试隐式转换。

成功响应：

```http
HTTP/1.1 200 OK
X-Request-Id: 01J...
```

```json
{
  "task_id": "424010985738629"
}
```

参数错误：

```http
HTTP/1.1 400 Bad Request
```

```json
{
  "type": "error",
  "error": {
    "type": "bad_request_error",
    "message": "invalid params, content must include a non-empty text item (2013)",
    "http_code": "400"
  },
  "request_id": "01J..."
}
```

队列额度错误：

```http
HTTP/1.1 429 Too Many Requests
```

```json
{
  "type": "error",
  "error": {
    "type": "rate_limit_error",
    "message": "unfinished task limit exceeded (1002)",
    "http_code": "429"
  },
  "request_id": "01J..."
}
```

幂等冲突：

```http
HTTP/1.1 409 Conflict
```

```json
{
  "type": "error",
  "error": {
    "type": "idempotency_error",
    "message": "idempotency key was used with a different request (2024)",
    "http_code": "409"
  },
  "request_id": "01J..."
}
```

接口逻辑：

1. 中间件完成请求 ID、Body 上限、Content-Type 和 API Key 校验。
2. 严格 JSON 解码；拒绝未知顶层和 content 字段，避免拼写错误被忽略。
3. 执行场景、互斥、数量、URL、resolution/duration/ratio 及 profile 校验。
4. 将规范化 JSON 计算 SHA-256；幂等键只保存摘要。
5. `BEGIN IMMEDIATE`：检查幂等、每 Key 10/全局 100 未结束上限、插入任务、写幂等记录、重算保护位。
6. 提交后发送非阻塞调度唤醒；唤醒丢失由周期扫描兜底。
7. 返回 task_id；不调用上游，不等待视频生成。

涉及表：

| 表 | 字段 | 用途 |
| --- | --- | --- |
| `video_tasks` | `task_id/api_key_id/request_json/request_hash/status/queue_seq` | 创建任务与 FIFO |
| `video_tasks` | `model/scenario/resolution/duration/ratio_requested/expires_at` | V2 查询字段与生命周期 |
| `idempotency_keys` | `api_key_id/key_hash/request_hash/task_id/expires_at` | 防重复提交 |

数据变更：插入任务；可选插入幂等记录；批量更新所有排队任务的 `status/cancel_locked/version`。

事务：必须。幂等、额度、任务插入和保护位是同一个业务原子操作。

缓存/MQ：无。调度唤醒是进程内提示，不承载状态。

审计日志：记录 `request_id/task_id/api_key_id/scenario`、数量统计和结果；不记录 Key、prompt 或媒体 URL。

人工联调准备：

- [ ] 使用官方 V2 三类示例验证 JSON 兼容。
- [ ] 使用真实 profile 验证 32 位参数顺序。
- [ ] 验证私有实例能访问媒体 HTTP/HTTPS URL。
