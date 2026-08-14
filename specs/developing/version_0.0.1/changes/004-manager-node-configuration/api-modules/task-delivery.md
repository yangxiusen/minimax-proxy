# 任务交付与视频播放接口设计

## 模块说明

- 模块名称：任务交付与视频播放
- 业务目标：保证查询结果可直接访问，并让管理员播放已完成视频
- 核心表：`video_tasks`、`task_artifacts`、`artifact_locations`
- 依赖组件：Artifact Service、Node API Client、HMAC 签名器
- 权限边界：V2 查询使用外部 Bearer Key；Manager 使用管理会话；文件内容使用短期签名或已有 Bearer 所有权鉴权

## 接口列表

| 接口名称 | 方法 | URL | 简要说明 |
| --- | --- | --- | --- |
| 查询单个生成任务 | GET | `/v2/query/video_generation/{task_id}` | 返回成功任务的绝对签名视频 URL |
| 查询生成任务列表 | GET | `/v2/query/video_generation` | 分页返回绝对签名视频 URL |
| 查询管理任务列表 | GET | `/manager/api/tasks` | 返回用于播放的 `video_url` |
| 获取视频内容 | GET | `/v2/files/{artifact_id}/content` | 校验签名/归属并代理 Node 视频流 |

## 1. 查询单个生成任务

接口名称：查询单个生成任务

URL：`GET /v2/query/video_generation/{task_id}`

Content-Type：无请求体；响应 `application/json`

认证与权限：`Authorization: Bearer <external_api_key>`；任务必须属于当前 Key。

Path 参数：

| 参数名 | 类型 | 必填 | 描述 | 示例 |
| --- | --- | --- | --- | --- |
| `task_id` | string | 是 | 1-64 位任务 ID | `272076440301631191` |

成功响应：`200 OK`

```json
{
  "task": {
    "id": "272076440301631191",
    "model": "MiniMax-H3",
    "status": "succeeded",
    "created_at": 1786617789,
    "updated_at": 1786617879,
    "content": {
      "url": "http://127.0.0.1:18081/v2/files/art_d6263610cd6a8a48cc6d306465aa7190/content?expires=1786618995&signature=signature-value"
    },
    "resolution": "480P",
    "duration": 5,
    "usage": {
      "total_seconds": 5,
      "input_seconds": 0,
      "output_seconds": 5,
      "input_image_count": 1
    },
    "ratio": "3:4",
    "task_type": "generation",
    "modality": "video"
  }
}
```

接口逻辑：

1. 按 Bearer Key 所有者读取未删除、未过期任务。
2. 非成功任务保持现有响应，不返回 `content`。
3. 成功任务有 `result_artifact_id` 时，使用 `artifact_id + api_key_id + GET + expires` 签名。
4. 签名器以 `server.public_base_url + /v2/files` 为 URL 前缀，返回绝对地址。
5. 仅有历史 `result_public_url` 时返回其原始、已校验绝对地址。

涉及表与字段：

| 表 | 字段 | 用途 |
| --- | --- | --- |
| `video_tasks` | `task_id/api_key_id/status/expires_at/deleted_at` | 所有权与状态查询 |
| `video_tasks` | `result_artifact_id/result_public_url` | 新签名地址或历史兼容地址 |

数据变更：无。

是否事务：否，只读查询和无状态 HMAC 签名。

错误响应：沿用现有 V2 `authentication_error`、`bad_request_error`、`server_error`；错误不得包含 Node URL 或签名原文。

## 2. 查询生成任务列表

接口名称：查询生成任务列表

URL：`GET /v2/query/video_generation`

Content-Type：无请求体；响应 `application/json`

认证与权限：`Authorization: Bearer <external_api_key>`；只返回当前 Key 的任务。

Query 参数：沿用 `page_num`、`page_size`、`filter.status`、`filter.task_ids`、`filter.model`、`filter.task_type`。

成功响应：`200 OK`

```json
{
  "items": [
    {
      "id": "272076440301631191",
      "model": "MiniMax-H3",
      "status": "succeeded",
      "content": {
        "url": "https://proxy.example.com/v2/files/art_x/content?expires=1786618995&signature=signature-value"
      },
      "resolution": "480P",
      "duration": 5,
      "usage": {"total_seconds": 5, "input_seconds": 0, "output_seconds": 5, "input_image_count": 1},
      "ratio": "3:4",
      "task_type": "generation",
      "modality": "video"
    }
  ],
  "total": 1
}
```

接口逻辑、字段、事务和错误与单任务查询一致；列表中的每个成功 artifact 独立签名，使用同一服务时钟和 TTL 策略。

## 3. 查询管理任务列表

接口名称：查询管理任务列表

URL：`GET /manager/api/tasks`

Content-Type：无请求体；响应 `application/json`

认证与权限：有效 `manager_session` Cookie。

Query 参数：沿用 `page_num/page_size/status/upstream_id/search`。

成功响应：`200 OK`

```json
{
  "items": [
    {
      "id": "272076440301631191",
      "api_key_id": "key_cff8c4d50df3943440172d63f26ecbf5",
      "status": "succeeded",
      "upstream_id": "minimax-private-1",
      "scenario": "r2va",
      "resolution": "480P",
      "duration_seconds": 76,
      "created_at": 1786617789,
      "phase": "succeeded",
      "retry_count": 0,
      "can_cancel": false,
      "can_delete": true,
      "video_url": "http://127.0.0.1:18081/v2/files/art_d6263610cd6a8a48cc6d306465aa7190/content?expires=1786618995&signature=signature-value"
    }
  ],
  "total": 1,
  "page_num": 1,
  "page_size": 10
}
```

接口逻辑：

1. Store 摘要读取 `result_artifact_id`，但响应不返回 artifact ID。
2. Handler 对成功 artifact 使用任务 `api_key_id` 生成 owner-bound 签名。
3. 无可用视频时 `video_url` 为 `null`，页面不渲染播放按钮。
4. Manager 会话只保护任务列表；视频正文仍由 URL 签名独立授权，不能把 Manager Cookie 当作 Node 凭据。

涉及表与字段：`video_tasks` 的任务摘要字段、`result_artifact_id`、`result_public_url`。

数据变更：无。是否事务：否。

人工联调准备：

- [ ] 成功、失败、运行和历史成功任务的按钮显隐已核对。
- [ ] 页面弹窗播放、关闭资源释放和过期 URL 错误态已核对。

## 4. 获取视频内容

接口名称：获取视频内容

URL：`GET /v2/files/{artifact_id}/content`

Content-Type：按 artifact 返回，视频通常为 `video/mp4`。

认证与权限：二选一。

- 查询参数包含有效 `expires/signature`；签名绑定 GET、逻辑 artifact 和 owner。
- 或携带有效外部 Bearer Key，且 Key 是 artifact 所属任务 owner。

请求头：

| 字段名 | 必填 | 描述 | 示例 |
| --- | --- | --- | --- |
| `Range` | 否 | 单一字节范围，供浏览器拖动播放 | `bytes=0-1048575` |

成功响应：`200 OK` 或 `206 Partial Content`，透传受控的 `Content-Type/Length/Range/ETag/Accept-Ranges`。

接口逻辑：

1. 校验签名有效期或 Bearer owner。
2. 从 `task_artifacts/artifact_locations/model_service_nodes` 定位活动主位置。
3. 解密 Node Key，在内部调用 `GET /internal/v1/artifacts/{node_artifact_id}/content`。
4. 校验状态、长度、ETag 和 Range 后流式返回，响应中不暴露 Node 地址。

数据变更：无。是否事务：否。

缓存与消息：不缓存正文、不发送 MQ；响应继续使用现有受控缓存策略。

## 5. 配置契约

```yaml
server:
  address: ":18081"
  public_base_url: "http://127.0.0.1:18081"
```

| 字段 | 必填 | 校验 | 用途 |
| --- | --- | --- | --- |
| `server.address` | 是 | 现有监听地址规则 | 服务监听，不用于构造对外 URL |
| `server.public_base_url` | 是 | HTTP/HTTPS、host 必填、无凭据/query/fragment、仅根路径 | V2 和 Manager 签名 URL 前缀 |

反向代理部署应配置调用方可达的 HTTPS 根地址。服务不信任 `Host`、`Forwarded` 或 `X-Forwarded-*` 自动覆盖该值。
