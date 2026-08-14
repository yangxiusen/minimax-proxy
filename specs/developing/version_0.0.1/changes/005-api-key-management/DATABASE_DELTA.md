# 对外 API Key 数据库增量设计

## 1. 变更概览

| 项目 | 内容 |
| --- | --- |
| 影响对象 | 新表、唯一索引、一次性数据导入 |
| 变更类型 | 新增 / 迁移 |
| 风险等级 | High |
| 迁移版本 | `010_external_api_keys.sql`、`012_external_api_key_plaintext.sql` |

## 2. `external_api_keys`

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | TEXT | PK, NOT NULL | 不可变内部 ID；旧记录沿用 YAML `id` |
| `name` | TEXT | NOT NULL | 管理员显示名称，保存去除首尾空格后的值 |
| `key_digest` | BLOB | NOT NULL, length=32 | 完整 Bearer Key 的 SHA-256 摘要 |
| `key_prefix` | TEXT | NOT NULL | 安全展示前缀，例如 `mmx_ab12` |
| `key_suffix` | TEXT | NOT NULL | 安全展示尾部，例如 `89ef` |
| `key_plaintext` | TEXT | v12 新增，可空 | 完整 Bearer Key 明文；历史摘要记录无法恢复时为空 |
| `enabled` | INTEGER | NOT NULL, 0/1 | 是否允许鉴权 |
| `version` | INTEGER | NOT NULL, >=1 | 乐观锁版本 |
| `created_at` | INTEGER | NOT NULL | UTC Unix 毫秒 |
| `updated_at` | INTEGER | NOT NULL | UTC Unix 毫秒 |

索引与约束：

```sql
CREATE TABLE external_api_keys (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL CHECK(length(trim(name)) BETWEEN 1 AND 128),
    key_digest BLOB NOT NULL CHECK(length(key_digest) = 32),
    key_prefix TEXT NOT NULL CHECK(length(key_prefix) BETWEEN 4 AND 16),
    key_suffix TEXT NOT NULL CHECK(length(key_suffix) BETWEEN 4 AND 16),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK(version >= 1),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX uq_external_api_keys_name_ci
    ON external_api_keys(lower(name));
CREATE UNIQUE INDEX uq_external_api_keys_digest
    ON external_api_keys(key_digest);
CREATE INDEX idx_external_api_keys_enabled
    ON external_api_keys(enabled,id);
```

不为 `video_tasks.api_key_id` 增加外键。历史任务必须在 Key 被停用或兼容导入前后保持可读，且旧 ID 的格式不应受新建 ID 规则限制。

## 3. `api_key_config_bootstrap`

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `source` | TEXT | PK, 固定 `yaml_api_keys` | 导入来源 |
| `imported_count` | INTEGER | NOT NULL, >=0 | 实际导入数量 |
| `completed_at` | INTEGER | NOT NULL | 完成时间，UTC Unix 毫秒 |

```sql
CREATE TABLE api_key_config_bootstrap (
    source TEXT PRIMARY KEY CHECK(source = 'yaml_api_keys'),
    imported_count INTEGER NOT NULL CHECK(imported_count >= 0),
    completed_at INTEGER NOT NULL
);
```

## 4. 一次性导入算法

1. 打开数据库并完成 v10 DDL。
2. 查询 `source='yaml_api_keys'`；存在则完全跳过 YAML Key 解析。
3. 未完成时读取旧 YAML Key，校验 ID/名称非空且不超过 128 字符、Key 非空、名称大小写不重复、摘要不重复。
4. 在 `BEGIN IMMEDIATE` 事务中再次检查完成标记。
5. 要求 `external_api_keys` 为空；若已有记录，则写入导入 0 条标记并以数据库为准，不混合两个来源。
6. 对每个旧 Key 计算 SHA-256，`id` 和 `name` 均沿用旧 `id`，保存安全前后缀和原启停状态。
7. 全部插入成功后写完成标记并提交。空 YAML 同样写入导入 0 条标记。
8. 任一冲突或写入错误整体回滚，不写完成标记，服务拒绝启动。

导入阶段可以接收不符合新建内部 ID 格式的旧 ID，但不能接收空值或超长值。Key 明文写入 `key_plaintext`，但不写日志。

## 5. CRUD 事务规则

- 创建：事务内插入，数据库唯一索引是名称和摘要冲突的最终防线。
- 更新：`WHERE id=? AND version=?`；成功后 `version=version+1`。
- 删除：同一 `BEGIN IMMEDIATE` 事务内检查 `video_tasks.api_key_id` 和 `idempotency_keys.api_key_id`，任一计数非零则返回 `key_in_use`，否则物理删除。
- 管理列表返回 Key 明文以支持复制；鉴权仍仅使用 `id/key_digest/enabled`。

## 6. 兼容与回滚

- v10 只新增表和索引，不修改历史任务表；旧二进制可忽略。
- 迁移文件前向执行，不提供线上反向 DDL。
- 应用回滚时恢复含旧 `api_keys` 的配置，旧二进制继续使用 YAML。
- 回滚窗口结束前备份 SQLite 和旧配置；清理配置前必须完成人工导入验证。

## 7. 验证项

- [ ] 新库和 v9 数据库均升级到 v10。
- [ ] 名称大小写冲突、摘要冲突和坏数据使导入整体回滚。
- [ ] 空 YAML 写入 0 条完成标记并允许启动。
- [ ] 已完成导入后无效 YAML Key 不再影响启动。
- [ ] 删除引用检查与删除在同一事务中，无 TOCTOU 竞态。
- [ ] SQLite 中能读取创建返回的完整 Key，管理列表可复制；数据库文件和备份按敏感凭据保护。
