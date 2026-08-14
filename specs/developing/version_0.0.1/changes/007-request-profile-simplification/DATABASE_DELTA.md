# 模型请求配置简化数据库增量

## 1. 迁移

| 项目 | 内容 |
| --- | --- |
| 迁移 | `011_request_profile_simplification.sql` |
| 当前可升级版本 | v8；迁移器按已登记版本执行，允许编号跳跃 |
| 策略 | 保留旧列兼容现有任务外键，清理旧测试数据，按 resolution 去重并建立唯一索引 |

本次没有依赖尚未落地的 v9/v10，也没有重建 Profile 表。`model/scenario/profile_version/status` 等旧列仅作为数据库兼容占位，领域和 Manager API 不再暴露或使用这些业务语义。

## 2. 迁移流程

1. 对每个 resolution 按 `updated_at DESC, id DESC` 排序，仅保留第一条。
2. 清空 `profile_test_runs`。
3. 对未保留 Profile 的 `video_tasks.profile_id` 置空，保留任务的 `config_snapshot_json`、`config_hash` 和阶段快照；新任务仅保存快照与哈希，不再写 Profile 外键。
4. 清空 `source_profile_id`，避免删除旧版本时产生自引用阻塞。
5. 增加 `updated_by`，历史数据回填为 `created_by`。
6. 删除旧 active/history 索引，建立 `model_request_profiles(resolution)` 唯一索引。

`row_version` 只用于并发覆盖检测，不是业务配置版本，不展示版本历史。

## 3. 删除事务

删除配置使用同一 immediate transaction：先将历史任务的 `profile_id` 置空，再按 `id + row_version` 删除配置。不读取任务状态，不阻塞运行中任务。任务执行以已冻结的 `task_stages.config_snapshot_json` 为准。

## 4. 回滚

v11 会删除重复 Profile 和 Profile 测试数据，自动 down migration 无法恢复。回滚需恢复数据库备份与旧二进制。
