# `version_0.0.1` 变更索引

## 目录说明

本目录记录 `v0.0.1` 需求确认后的迭代、缺陷和兼容修复。目录名必须使用 `{编号}-{功能名称}`，新增时同时检查开发中与归档目录的最大编号。

## 状态枚举

| 状态 | 说明 |
| --- | --- |
| Pending | 已创建，未开始 |
| In Progress | 开发中 |
| Completed | 开发闭环完成 |
| Blocked | 被阻塞 |
| Cancelled | 已取消 |

## 变更清单

| 编号 | 变更标题 | 类型 | 状态 | 负责人 | 关联文档 | 路径 | 备注 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 001-monitor-console | 只读监控控制台与节点状态缓存 | 迭代 | In Progress | 待人工填写 | CHANGE_SPEC.md、PRD_DELTA.md、PROTOTYPE_DELTA.md、TECH_SOLUTION.md、API_DELTA.md、task.md、TEST_ACCEPTANCE.md | `specs/developing/version_0.0.1/changes/001-monitor-console/` | 实现与本地验收完成，待 Docker 和真实环境人工联调 |
| 002-task-lifecycle-closure | 任务恢复、中止、删除与视频访问闭环 | 缺陷修复/迭代 | In Progress | 待人工填写 | CHANGE_SPEC.md、PRD_DELTA.md、TECH_SOLUTION.md、DATABASE_DELTA.md、API_DELTA.md、PROTOTYPE_DELTA.md、task.md、TEST_ACCEPTANCE.md | `specs/developing/version_0.0.1/changes/002-task-lifecycle-closure/` | 设计完成，待开发与真实模型故障演练 |

## 维护规则

- 新建 change 时立即追加索引，状态变化时同步更新。
- 不删除历史记录；取消或废弃使用状态标记。
- 负责人未知时写 `待人工填写`。
- 每个 change 至少维护 `CHANGE_SPEC.md`、`task.md` 和 `TEST_ACCEPTANCE.md`。
