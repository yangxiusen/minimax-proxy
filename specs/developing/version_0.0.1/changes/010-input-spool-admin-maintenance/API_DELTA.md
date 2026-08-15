# 输入临时文件与 Manager 任务详情接口增量

## 1. 变更概览

| 模块 | 接口 | 方法 | 路径 | 变化 |
| --- | --- | --- | --- | --- |
| V2 视频生成 | 创建任务 | POST | `/v2/video_generation` | 路径和请求响应不变，内部不再把新任务 Base64 正文写入 DB |
| Manager 任务管理 | 任务详情 | GET | `/manager/api/tasks/{task_id}` | 新增，用于查看用户提交请求内容 |
| Manager 任务管理 | 输入文件内容 | GET | `/manager/api/tasks/{task_id}/inputs/{input_id}/content` | 新增，用于在任务详情中查看或下载本地托管的媒体输入文件 |
| Manager 任务管理 | 删除任务 | DELETE | `/manager/api/tasks/{task_id}` | 路径不变，语义改为终态任务物理删除 |

## 2. V2 创建任务内部语义

外部契约保持不变。调用方仍可提交：

```json
{
  "model": "MiniMax-H3",
  "content": [
    { "type": "text", "text": "让画面动起来" },
    {
      "type": "image_url",
      "role": "first_frame",
      "image_url": { "url": "data:image/png;base64,..." }
    }
  ],
  "resolution": "768P",
  "duration": 5
}
```

内部变化：

1. 校验通过后生成 `task_id`。
2. Data URI 被写入数据库同级 `temp-inputs/<task_id>/`。
3. 入库的 `request_json` 中对应 URL 改为 `proxy-input://<task_id>/<input_id>`。
4. `request_hash` 仍基于用户原始规范化请求计算，幂等行为保持一致。
5. 有 `Idempotency-Key` 且请求哈希匹配已有任务时，服务在写入本地临时文件前直接返回已有 `task_id`；并发竞争仍以数据库唯一约束兜底，失败方清理候选目录。

错误响应沿用当前 `bad_request_error` 和 `server_error` 结构。新增稳定错误场景：

| 场景 | HTTP | error.type | message |
| --- | --- | --- | --- |
| 临时输入目录不可写 | 500 | `server_error` | `internal error (1000)` |
| Data URI 解码后为空或超限 | 400 | `bad_request_error` | 沿用图片或音频 Base64 校验提示 |
| 文件格式和声明 MIME 明显跨类型不匹配 | 400 | `bad_request_error` | `媒体文件格式与声明类型不匹配 (2013)` |

## 3. Manager 任务详情

接口名称：查询任务请求详情

URL：
`GET /manager/api/tasks/{task_id}`

Content-Type：
响应 `application/json; charset=utf-8`

认证与权限：
Manager 管理员会话 Cookie。普通 V2 API Key 不可调用。

Path 参数：

| 参数名 | 类型 | 必填 | 描述 | 示例 |
| --- | --- | --- | --- | --- |
| `task_id` | string | 是 | 任务 ID，1-64 字符 | `895567577569093972` |

成功响应：

```json
{
  "id": "895567577569093972",
  "api_key_id": "key_1bfe...",
  "status": "queued",
  "phase": "queued",
  "scenario": "r2va",
  "model": "MiniMax-H3",
  "resolution": "2K",
  "ratio": "adaptive",
  "duration": 5,
  "created_at": 1786730000,
  "updated_at": 1786730020,
  "request": {
    "model": "MiniMax-H3",
    "resolution": "2K",
    "ratio": "adaptive",
    "duration": 5,
    "content": [
      {
        "index": 0,
        "type": "text",
        "text": "用户提交的文案"
      },
      {
        "index": 1,
        "type": "image_url",
        "role": "reference_image",
        "source_kind": "data_uri",
        "media_type": "image/png",
        "extension": ".png",
        "file_name": "input_abcd1234.png",
        "input_id": "input_abcd1234",
        "input_ref": "proxy-input://895567577569093972/input_abcd1234",
        "size_bytes": 123456,
        "sha256": "abcdef123456...",
        "file_state": "available"
      }
    ]
  },
  "config": {
    "profile_id": "profile_1",
    "profile_version": 3,
    "model_mode": "high_quality",
    "steps": 8,
    "cache_mode": "easycache",
    "loras": [],
    "interpolation_enabled": true,
    "restoration_enabled": true
  },
  "legacy_base64_present": false
}
```

错误响应：

```json
{
  "error": {
    "type": "task_not_found",
    "message": "任务不存在"
  }
}
```

接口逻辑：

1. 校验管理员会话和 `task_id`。
2. 查询未物理删除的任务；如果不存在返回 404。
3. 解析 `request_json`。
4. 查询 `task_input_spool_files`，按 `content_index` 合并媒体元数据。
5. 解析 `config_snapshot_json`，抽取后台需要展示的摘要。
6. 若发现 legacy Data URI，只返回 `legacy_base64_present=true` 和隐藏后的媒体摘要，不返回 Base64 正文。

涉及表与字段：

| 表 | 字段 | 用途 |
| --- | --- | --- |
| `video_tasks` | `task_id/api_key_id/status/request_json/config_snapshot_json/profile_id/profile_version` | 详情主体 |
| `task_input_spool_files` | `content_index/role/media_type/extension/size_bytes/sha256` | 输入文件元数据；接口只返回文件名或 `proxy-input://` 引用，不返回相对路径或绝对路径 |
| `task_stages` | `stage_type/config_snapshot_json` | 必要时补充阶段配置 |

数据变更：
无。

是否事务：
否。只读查询，单次详情允许读取当前已提交快照。

缓存与消息：
暂无。

人工联调准备：

- 请求详情弹窗需验证长文案换行、媒体多行、缺失文件提示、历史 Base64 隐藏提示。
- 媒体输入为本地托管文件时，弹窗中的“查看”应新标签打开受保护内容接口，“下载”应触发附件下载。
- 后台接口日志不得输出请求正文。

## 3.1 Manager 输入文件内容

接口名称：查看或下载任务输入文件

URL：
`GET /manager/api/tasks/{task_id}/inputs/{input_id}/content`

认证与权限：
Manager 管理员会话 Cookie。普通 V2 API Key 不可调用。

Query 参数：

| 参数名 | 类型 | 必填 | 描述 | 示例 |
| --- | --- | --- | --- | --- |
| `download` | string | 否 | 等于 `1` 时返回附件下载；缺省时返回 inline 预览 | `1` |

成功响应：

- `HTTP 200`
- `Content-Type` 使用托管文件元数据中的媒体类型，例如 `image/png`、`audio/mpeg`。
- 缺省 `Content-Disposition: inline; filename="..."`。
- `download=1` 时 `Content-Disposition: attachment; filename="..."`。
- 响应体为原始媒体文件字节，不返回本地绝对路径。

错误响应：

| 场景 | HTTP | error.type | message |
| --- | --- | --- | --- |
| 未登录或会话过期 | 401 | `authentication_error` | `未登录或会话已过期` |
| `task_id/input_id` 无效 | 400 | `bad_request_error` | `输入文件标识无效` |
| 元数据或本地文件不存在 | 404 | `task_not_found` | `任务不存在` 或 `输入文件不存在` |
| 输入文件服务未配置或路径异常 | 500 | `server_error` | `服务内部错误` |

安全约束：

- 只通过 `task_id + input_id` 定位 `task_input_spool_files` 元数据。
- 真实文件路径由服务端根据 `temp-inputs` 根目录和 DB 相对路径拼接。
- 服务端校验相对路径不得越界，不向浏览器返回相对路径或绝对路径。

## 4. Manager 删除任务

接口名称：物理删除终态任务

URL：
`DELETE /manager/api/tasks/{task_id}`

认证与权限：
Manager 管理员会话 Cookie。

成功响应：
`HTTP 204 No Content`

行为语义：

- 仅允许 `succeeded/failed/cancelled` 终态任务。
- 删除成功后，该任务在任务列表、详情、V2 查询和幂等记录中均不可见。
- 本地 `temp-inputs/<task_id>/` 被物理删除；默认根目录从 `database.path` 推导，例如 `/data/minimax.db` 对应 `/data/temp-inputs/<task_id>/`。
- 该任务相关 SQLite 行被物理删除，不再使用 `deleted_at` 逻辑删除。
- 远端 Node 产物必须删除成功或确认不存在后才清除 DB 线索。
- 若任务仍存在 `node_dispatch_barriers`，说明 Node 取消对账未完成，删除必须失败且不得移除屏障。

错误映射：

| 场景 | HTTP | error.type | message |
| --- | --- | --- | --- |
| 任务不存在 | 404 | `task_not_found` | `任务不存在` |
| 任务非终态 | 409 | `task_not_operable` | `任务当前状态不可操作` |
| 取消对账未完成 | 409 | `cancel_reconcile_pending` | `任务仍在中止对账中，请稍后重试` |
| 远端产物删除失败 | 503 | `task_delete_unavailable` | `远端文件删除失败，请稍后重试` |
| 本地临时目录删除失败 | 500 | `server_error` | `服务内部错误` |

涉及表与字段：

| 表 | 字段 | 用途 |
| --- | --- | --- |
| `video_tasks` | `task_id/status/version` | 状态校验和物理删除 |
| `node_dispatch_barriers` | `task_id` | 禁止删除仍在取消对账的任务 |
| `task_input_spool_files` | `task_id/relative_path` | 删除本地输入文件 |
| `task_artifacts` | `id/task_id` | 找到逻辑产物 |
| `artifact_locations` | `node_id/node_artifact_id/state` | 删除远端文件 |
| `artifact_deletion_jobs/items` | `artifact_id/location_id` | 清理旧删除作业记录 |
| `task_stages/stage_attempts` | `task_id/stage_id` | 清理执行记录 |
| `idempotency_keys` | `task_id` | 删除幂等映射 |
| `profile_test_runs` | `artifact_id` | 若引用任务 artifact，则阻止物理删除 |

是否事务：
是。远端 Node 删除在事务外执行，SQLite 物理删除在一个 `BEGIN IMMEDIATE` 中完成。

缓存与消息：

- 删除成功后唤醒任务列表刷新。
- 不创建新的 artifact deletion job，避免任务 DB 行已删除但删除作业仍引用已删除数据。
- 远端删除使用稳定 operation ID `purge-<task_id>-<location_id>`，保证接口重试和并发删除时 Node 侧幂等。
