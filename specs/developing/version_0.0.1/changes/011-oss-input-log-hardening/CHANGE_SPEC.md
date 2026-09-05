# OSS 输入重复提交与异常日志增强

## 1. 基本信息

| 项目 | 内容 |
|------|------|
| 目标版本 | `version_0.0.1` |
| 变更编号 | `011-oss-input-log-hardening` |
| 变更类型 | 缺陷修复 / 运维增强 |
| 优先级 | High |
| 提出日期 | 2026-09-06 |
| 负责人 | Codex |
| Preflight 状态 | 已执行 `specs/README.md`、`PROJECT_OVERVIEW.md`、`ARCHITECTURE.md` |

## 2. 背景

- 当前行为：开启对象存储输入直传后，相同用户用相同提示词和相同 Base64 素材重复提交时，对象 key 与 `task_input_spool_files.relative_path` 会复用；旧表结构要求 `relative_path` 全局唯一，导致 SQLite 约束冲突，创建接口返回 `500 internal error (1000)`，且任务未入库。
- 触发原因：相同素材本来应该复用同一个 OSS 对象，但本地输入元数据表没有支持“多个 task 引用同一对象”的结构；如果改为每次生成新 key，则能避开冲突但会重复上传同一素材。
- 运维诉求：所有服务端异常需要在日志中保留脱敏原因，并将容器内日志同步输出到可挂载目录。

## 3. 目标结果

- 相同用户、相同提示词、相同素材可重复创建任务，并复用第一次上传的 OSS 输入对象，不再重复上传。
- 创建接口内部异常日志包含脱敏后的 `error_reason`，便于定位 SQLite 约束、配置、随机数、签名等失败。
- 容器默认将 JSON 日志同时输出到 stdout 和 `/var/log/minimax-proxy/server.log`，支持宿主机挂载。

## 4. 影响范围

| 影响项 | 是否影响 | 说明 |
|------|---------|------|
| 产品流程 | Y | 修复重复提交失败 |
| 业务逻辑 | Y | OSS 输入对象 key 增加任务维度 |
| 数据库 | Y | 新增 v22 迁移，允许多个任务引用同一 `relative_path` |
| API 契约 | N | 不改请求/响应字段 |
| UI 与交互 | N | 无页面变化 |
| 原型 | N | 无 |
| 配置部署 | Y | Docker 增加日志目录 volume |
| 回归测试 | Y | 覆盖重复提交、日志文件输出 |

## 5. 文档生成决策

| 文档 | 是否生成 | 原因 |
|------|----------|------|
| `CHANGE_SPEC.md` | Y | 固定生成 |
| `task.md` | Y | 固定生成 |
| `TEST_ACCEPTANCE.md` | Y | 固定生成 |
| `PRD_DELTA.md` | N | 不改变对外业务规则 |
| `PROTOTYPE_DELTA.md` | N | 无页面变化 |
| `TECH_SOLUTION.md` | N | 方案可在变更说明内表达 |
| `API_DELTA.md` | N | 不改变 API 契约 |
| `DATABASE_DELTA.md` | N | 数据库变更较小，已在本变更说明中记录 |

## 6. 变更详情

### 6.1 变更前

- OSS 输入对象 key 稳定，相同请求会复用相同 `relative_path`。
- `task_input_spool_files.relative_path` 全局唯一，阻止多个任务引用同一个已上传对象。
- V2 创建接口内部错误日志只记录错误类型，不记录脱敏错误原因。
- 容器仅依赖 stdout 查看日志。

### 6.2 变更后

- V2 创建接口在上传前先按 owner 与 request hash 查询已存在的对象输入；命中时直接复用旧任务的 rewritten request JSON、`relative_path` 与 `object_url`，并为新任务生成新的输入元数据 ID。
- 新增 v22 迁移重建 `task_input_spool_files`，保留 `UNIQUE(task_id, content_index)`，移除 `UNIQUE(relative_path)`，并增加 `relative_path` 普通索引。
- V2 创建接口和管理接口内部异常日志增加 `error_reason`，使用 `logsafe.Error` 脱敏 URL、Data URI、私有地址。
- server 启动时创建文件日志目录并用 `io.MultiWriter` 同时写 stdout 与日志文件。
- Dockerfile 创建 `/var/log/minimax-proxy` 并声明 volume；compose 映射到 `./logs`。

### 6.3 不在本次范围

- 不调整 API 响应结构。
- 不迁移或删除历史 OSS 对象。
- 不放开数据库唯一约束。

## 7. 兼容性与风险

- 现有 API 消费方不受影响。
- 同一 OSS 对象可被多个任务引用，物理对象清理不能简单按单个任务删除；本次变更仅处理输入对象引用复用，不新增输入 OSS 对象删除流程。
- 回滚策略：回滚代码与镜像；历史新增对象不影响读取。

## 8. 验收标准

- Given 开启对象存储输入直传且请求包含 Base64 图片，When 两次提交相同请求，Then 两次都返回任务 ID，第二次不调用上传准备流程，数据库中两条输入素材元数据引用相同 `relative_path` 与 `object_url`。
- Given 创建接口发生内部异常，When 返回 500，Then 日志记录 request_id、error_code、error_type 和脱敏 error_reason。
- Given 容器启动，When `/var/log/minimax-proxy` 被挂载，Then `server.log` 中能看到同 stdout 一致的 JSON 日志。
