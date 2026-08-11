# 模型服务节点配置接口设计

## 模块说明

- 业务目标：由管理员在运行中维护模型服务节点，并以 SQLite 作为唯一配置真相源。
- 核心表：`model_service_nodes`、`video_tasks`。
- 依赖组件：管理会话、Node Registry、Gradio/Jobs 客户端。
- 权限边界：全部接口要求有效 `manager_session`；响应禁止缓存。

## 数据对象

节点配置对象：

```json
{
  "id": "minimax-private-1",
  "base_url": "http://10.7.85.43:8200",
  "jobs_base_url": "http://10.7.85.43:8188",
  "public_base_url": "https://video.example.com",
  "health_path": "/",
  "submit_api_name": "submit_minimax_from_slots",
  "check_api_name": "check_and_get_video",
  "poll_interval": "3s",
  "request_timeout": "30s",
  "enabled": true,
  "version": 1,
  "created_at": 1786420800,
  "updated_at": 1786420800
}
```

字段规则：

| 字段 | 类型 | 必填 | 规则 |
| --- | --- | --- | --- |
| `id` | string | 创建是 | 1 至 64 位，正则 `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` |
| `base_url` | string | 是 | 完整 HTTP/HTTPS URL，无凭据、查询参数、片段 |
| `jobs_base_url` | string | 是 | 同上 |
| `public_base_url` | string | 是 | 同上 |
| `health_path` | string | 是 | 1 至 256 字符，以 `/` 开头，不含查询或片段 |
| `submit_api_name` | string | 是 | 1 至 128 字符，不含空白或 `/` |
| `check_api_name` | string | 是 | 1 至 128 字符，不含空白或 `/` |
| `poll_interval` | duration | 是 | `1s` 至 `5m` |
| `request_timeout` | duration | 是 | `1s` 至 `5m` |
| `enabled` | boolean | 是 | 创建默认由前端显式传 `true` |
| `version` | integer | 更新是 | 大于等于 1 |

## 1 查询节点配置

URL：`GET /manager/api/nodes`

认证与权限：有效管理会话。

成功响应：`200 OK`

```json
{
  "items": [
    {
      "id": "minimax-private-1",
      "base_url": "http://10.7.85.43:8200",
      "jobs_base_url": "http://10.7.85.43:8188",
      "public_base_url": "https://video.example.com",
      "health_path": "/",
      "submit_api_name": "submit_minimax_from_slots",
      "check_api_name": "check_and_get_video",
      "poll_interval": "3s",
      "request_timeout": "30s",
      "enabled": true,
      "version": 1,
      "created_at": 1786420800,
      "updated_at": 1786420800
    }
  ]
}
```

接口逻辑：按 `id` 升序返回全部 `deleted_at IS NULL` 节点。节点数量小且属于配置集合，不分页。

涉及表与字段：读取 `model_service_nodes` 全部非删除字段。

是否事务：否，只读单查询。

## 2 新增节点

URL：`POST /manager/api/nodes`

Content-Type：`application/json`

认证与权限：有效管理会话。

请求体：除 `version/created_at/updated_at` 外的完整节点配置。

```json
{
  "id": "minimax-private-2",
  "base_url": "http://10.7.85.44:8200",
  "jobs_base_url": "http://10.7.85.44:8188",
  "public_base_url": "https://video-2.example.com",
  "health_path": "/",
  "submit_api_name": "submit_minimax_from_slots",
  "check_api_name": "check_and_get_video",
  "poll_interval": "3s",
  "request_timeout": "30s",
  "enabled": true
}
```

成功响应：`201 Created`，Body 为完整节点配置对象，并设置 `Location: /manager/api/nodes/minimax-private-2`。

接口逻辑：

1. 严格解析和规范化字段。
2. 在事务内确认 ID 从未存在并插入 `version=1`。
3. 提交后唤醒 Registry。
4. 返回持久化配置；不等待网络健康检查。

数据变更：`model_service_nodes` insert。

是否事务：是，仅包含唯一性检查和插入；无网络调用。

## 3 更新节点

URL：`PUT /manager/api/nodes/{node_id}`

Content-Type：`application/json`

认证与权限：有效管理会话。

Path 参数：`node_id` 必须符合节点 ID 规则并与数据库记录匹配。

请求体：完整可编辑配置，不包含 `id`，必须包含客户端读取到的 `version`。

```json
{
  "base_url": "http://10.7.85.43:8200",
  "jobs_base_url": "http://10.7.85.43:8188",
  "public_base_url": "https://video.example.com",
  "health_path": "/",
  "submit_api_name": "submit_minimax_from_slots",
  "check_api_name": "check_and_get_video",
  "poll_interval": "3s",
  "request_timeout": "30s",
  "enabled": false,
  "version": 1
}
```

成功响应：`200 OK`，Body 为 `version=2` 的完整节点配置对象。

接口逻辑：

1. 在同一事务读取节点和该节点活动任务。
2. 若有活动任务，只允许其他字段不变且由启用改为停用。
3. 使用预期版本条件全量更新，增加 `version` 和 `updated_at`。
4. 提交后唤醒 Registry；不等待节点健康。

数据变更：`model_service_nodes` update。

是否事务：是，节点/任务状态检查和条件更新同一事务；无网络调用。

## 4 删除节点

URL：`DELETE /manager/api/nodes/{node_id}?version=2`

认证与权限：有效管理会话。

Query 参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `version` | integer | 是 | 当前配置版本，防止误删已更新节点 |

成功响应：`204 No Content`。

接口逻辑：

1. 在同一事务读取节点和活动任务。
2. 节点启用时返回 `node_must_be_disabled`。
3. 存在活动任务时返回 `node_has_active_task`。
4. 按版本条件写 `deleted_at/updated_at` 并增加版本。
5. 提交后唤醒 Registry，停止槽和采集并清理缓存。

数据变更：`model_service_nodes` soft delete。

是否事务：是，所有状态检查和条件更新同一事务。

## 5 测试草稿连接

URL：`POST /manager/api/nodes/test`

Content-Type：`application/json`

认证与权限：有效管理会话。

请求体：与新增节点相同；`id` 仅用于日志关联，可使用尚未保存的新 ID。`public_base_url` 只做格式校验，不主动请求。

成功响应：`200 OK`

```json
{
  "gradio": {"ok": true},
  "jobs": {"ok": true}
}
```

部分或全部失败响应：`502 Bad Gateway`

```json
{
  "error": {
    "type": "node_probe_failed",
    "message": "模型服务连接测试未全部通过"
  },
  "checks": {
    "gradio": {"ok": true},
    "jobs": {"ok": false, "error_code": "upstream_jobs_unhealthy"}
  }
}
```

接口逻辑：

1. 使用与保存相同的字段校验和 URL 规范化。
2. 使用 `request_timeout` 创建总超时上下文，并行执行 Gradio `health_path` 和 Jobs `/api/jobs` 只读检查。
3. 限制响应体，丢弃私有响应内容，只返回稳定检查结果。
4. 不写数据库、不唤醒 Registry、不改变监控缓存。

是否事务：否。

安全说明：该接口属于管理员显式配置私有节点的必要网络访问能力。日志仅包含输入 `id` 和稳定错误码，不记录三个 URL 或上游响应。

## 人工联调准备

- [ ] 新增、更新、停用、启用和删除字段映射按本文核对。
- [ ] Gradio 健康路径与 Jobs `/api/jobs` 在真实节点验证。
- [ ] 409 版本冲突和活动任务提示在弹窗展示。
- [ ] 保存成功后应用中、未知、健康和异常状态转换核对。
