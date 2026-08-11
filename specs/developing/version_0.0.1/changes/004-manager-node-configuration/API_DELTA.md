# 一、接口列表集合

## 1 管理后台基础接口迁移

原有管理会话、监控快照和任务管理接口只变更路径前缀，请求和响应契约保持不变。

| 接口名称 | 方法 | 新 URL | 原 URL | 变化 |
| --- | --- | --- | --- | --- |
| 管理员登录 | POST | `/manager/api/session` | `/monitor/api/session` | Cookie 改为 `manager_session`，Path 改为 `/manager` |
| 管理员退出 | DELETE | `/manager/api/session` | `/monitor/api/session` | 路径迁移 |
| 管理快照 | GET | `/manager/api/snapshot` | `/monitor/api/snapshot` | 节点项新增 `enabled`、`applying` |
| 管理任务列表 | GET | `/manager/api/tasks` | `/monitor/api/tasks` | 路径迁移 |
| 中止任务 | POST | `/manager/api/tasks/{task_id}/cancel` | `/monitor/api/tasks/{task_id}/cancel` | 路径迁移 |
| 删除任务 | DELETE | `/manager/api/tasks/{task_id}` | `/monitor/api/tasks/{task_id}` | 路径迁移 |

旧 `/monitor/api/*` 不提供接口别名。页面入口 `GET /monitor`、`GET /monitor/`、`GET /monitor/login` 使用 308 跳转到对应 `/manager` 页面。

## 2 模型服务节点配置

模块说明：管理员持久化并运行时应用模型服务节点配置。

| 接口名称 | 方法 | URL | 简要说明 |
| --- | --- | --- | --- |
| 查询节点配置 | GET | `/manager/api/nodes` | 返回全部未删除节点配置 |
| 新增节点 | POST | `/manager/api/nodes` | 创建节点期望配置 |
| 更新节点 | PUT | `/manager/api/nodes/{node_id}` | 使用版本号全量更新节点 |
| 删除节点 | DELETE | `/manager/api/nodes/{node_id}` | 软删除已停用且无活动任务的节点 |
| 测试草稿连接 | POST | `/manager/api/nodes/test` | 检查当前表单中的 Gradio 与 Jobs 服务 |

# 二、模块功能说明

## 1 管理后台基础接口迁移

页面和 Cookie 作用域迁移到 `/manager`，防止继续把已具备写能力的页面误称为只读监控。升级后旧会话不复用，管理员重新登录。公共 `/v2` 接口不受影响。

## 2 模型服务节点配置

1. 节点配置写入 `model_service_nodes` 后唤醒运行时 Registry。
2. Registry 异步协调配置，节点只有启用且健康新鲜时才可调度。
3. 更新使用 `version` 乐观锁；活动任务存在时只允许停用，不允许改变连接参数。
4. 删除要求节点已停用且无活动任务，并使用软删除保留历史 ID。
5. 连接测试使用草稿配置且不写数据库，分别检查 Gradio 健康入口和 Jobs `/api/jobs`。

核心实体：`model_service_nodes`、`node_config_bootstrap`、`video_tasks`。

依赖关系：管理会话、SQLite Store、Node Registry、Gradio/Jobs 客户端、Monitor Cache。

权限边界：所有接口必须具有有效管理会话；不接受公共 API Key 替代登录。

# 三、通用契约

- JSON 请求只接受 `Content-Type: application/json`，限制为 32 KiB，拒绝未知或重复字段及尾随内容。
- 所有响应设置 `Cache-Control: no-store`。
- 时间字段使用 Unix 秒；轮询和超时字段在管理 API 中使用 Go duration 字符串，如 `3s`、`30s`。
- URL 规范化后去掉尾部 `/`，但保留根路径；不得包含凭据、查询参数或片段。
- 错误响应沿用现有格式：

```json
{
  "error": {
    "type": "node_version_conflict",
    "message": "配置已被更新，请刷新后重试"
  }
}
```

| HTTP 状态 | `type` | 使用场景 |
| --- | --- | --- |
| 400 | `bad_request_error` | JSON、字段、URL、时长或路径无效 |
| 401 | `authentication_error` | 未登录或会话过期 |
| 404 | `node_not_found` | 节点不存在或已删除 |
| 409 | `node_id_conflict` | 节点 ID 已使用，包括软删除记录 |
| 409 | `node_version_conflict` | 更新版本不匹配 |
| 409 | `node_has_active_task` | 活动任务阻止修改或删除 |
| 409 | `node_must_be_disabled` | 删除启用节点 |
| 502 | `node_probe_failed` | 草稿配置连接测试未全部通过 |
| 500 | `server_error` | 未预期内部错误 |

# 四、接口文档索引

| 模块 | 文档路径 | 接口数量 | 说明 |
| --- | --- | ---: | --- |
| 模型服务节点配置 | `api-modules/manager-nodes.md` | 5 | 节点查询、创建、更新、删除和连接测试 |
