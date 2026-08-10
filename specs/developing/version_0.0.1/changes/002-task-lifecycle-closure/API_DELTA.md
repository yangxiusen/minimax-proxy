# 一、接口列表集合

## 1 管理任务生命周期

模块说明：管理员查看任务可操作能力，中止活动任务，删除终态任务并访问公开视频结果。

| 接口名称 | 方法 | URL | 简要说明 |
| --- | --- | --- | --- |
| 管理任务列表 | GET | `/monitor/api/tasks` | 扩展阶段、重试、能力和公开视频字段 |
| 中止任务 | POST | `/monitor/api/tasks/{task_id}/cancel` | 原子提出中止请求 |
| 删除任务 | DELETE | `/monitor/api/tasks/{task_id}` | 逻辑删除已中止、成功或失败任务 |

# 二、模块功能说明

## 1 管理任务生命周期

1. 页面根据后端 `can_cancel/can_delete` 能力字段显示操作。
2. 中止排队任务立即终止；活动任务进入 `cancelling`，由 Worker 完成上游和本地收口。
3. 删除只接受 `cancelled/succeeded/failed`，并清理对应幂等关系。
4. 成功任务只返回 `result_public_url` 对应的 `video_url`，不返回内部 URL。

核心实体：`video_tasks`、`idempotency_keys`。

依赖关系：管理会话、SQLite Store、Worker、私有 Jobs API。

权限边界：必须具有有效的现有管理会话；不接受 Bearer 客户 Key替代管理登录。

# 三、接口文档索引

| 模块 | 文档路径 | 接口数量 | 说明 |
| --- | --- | ---: | --- |
| 管理任务生命周期 | `api-modules/admin-task-lifecycle.md` | 3 | 查询、中止、删除和视频入口 |
