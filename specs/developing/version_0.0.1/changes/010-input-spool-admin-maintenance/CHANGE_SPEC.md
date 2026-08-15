# 输入临时文件、任务详情查看与自动迁移变更说明

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 目标版本 | `version_0.0.1` |
| 变更编号 | `010-input-spool-admin-maintenance` |
| 变更类型 | 架构优化 / 管理后台增强 / 数据库迁移 |
| 优先级 | High |
| 提出日期 | 2026-08-15 |
| 负责人 | 待人工填写 |
| Preflight 执行情况 | 已读取 `specs/README.md`、`PROJECT_OVERVIEW.md`、`ARCHITECTURE.md`、版本 PRD、003/008/009 变更和现有 Proxy 代码 |

## 2. 变更背景

- 当前 V2 创建任务会把完整 `request_json` 写入 `video_tasks.request_json`，图片和音频 Data URI 会把 Base64 正文一并落入 SQLite。
- 线上数据库已出现 70 MiB 级体积，其中主要空间来自 `request_json` 中的 Base64 数据。
- 当前执行阶段才把 Data URI 解码为 OS 临时文件并导入 Node；Proxy 重启前仍依赖数据库中的 Base64 来恢复排队任务。
- 管理后台任务列表缺少“查看请求内容”能力，排查用户提交的文案、图片、音频、分辨率和配置时需要直接查数据库。
- 当前后台删除任务是逻辑删除并创建远端产物删除作业；本次明确要求后台删除任务时，临时文件和数据库记录都物理删除。
- 当前 Store 已具备启动自动迁移框架，但后续新增表和字段必须纳入迁移计划，部署正式环境时不能再依赖手工复制已升级数据库。

## 3. 目标结果

- V2 创建任务时把 Data URI 解码为 Proxy 托管临时文件，按真实文件格式保存为 `jpg/png/webp/wav/mp3` 等后缀，数据库只保存轻量引用和元数据，不再保存 Base64 正文。
- 排队任务在 Proxy 重启后仍可继续执行；执行阶段从持久化临时目录读取文件，并继续使用现有 Node `ImportArtifact` multipart 导入流程。
- 管理后台任务操作区新增“查看”按钮，弹窗展示用户请求内容，包括文案、媒体输入、角色、来源、媒体类型、文件大小、分辨率、比例、时长和冻结配置摘要。
- 后台删除终态任务时，物理删除本地输入临时文件和 SQLite 中该任务的关联数据；远端 Node 产物删除成功或确认不存在后再清除数据库线索。
- 若 `cancelled` 任务仍存在 `node_dispatch_barriers`，说明对应 Node 仍在取消对账中，后台删除必须返回冲突并保留屏障，等屏障解除后才能物理删除。
- 数据库升级到新版本时由服务启动自动执行迁移，正式环境只需要部署新二进制和保留原数据库文件，不再手动复制本地升级后的数据库。

## 4. 影响范围

| 影响项 | 是否影响 | 说明 |
| --- | --- | --- |
| 产品流程 | Y | 新增后台查看任务请求，删除任务变为物理删除 |
| 业务逻辑 | Y | 创建任务前置 Data URI 落盘，执行阶段从托管临时文件读取 |
| 数据库 | Y | 新增输入临时文件元数据表，迁移版本升级到 v15 |
| 接口契约 | Y | 新增 Manager 任务详情接口；V2 入参不变 |
| 页面或交互 | Y | 任务列表操作列新增“查看”，删除提示明确不可恢复 |
| 原型 | Y | 更新 Manager 弹窗与操作按钮说明 |
| 配置部署 | Y | 本地输入临时目录默认从 `database.path` 推导，不新增必填配置；数据库自动迁移 |
| 回归测试 | Y | 覆盖 Base64 不入库、重启恢复、物理删除、自动迁移和后台查看 |

## 5. 文档生成决策

| 文档 | 是否生成 | 原因 |
| --- | --- | --- |
| `CHANGE_SPEC.md` | Y | 固定生成 |
| `TECH_SOLUTION.md` | Y | 涉及创建、执行、删除、后台和启动迁移链路 |
| `DATABASE_DELTA.md` | Y | 新增表、迁移版本和物理删除策略 |
| `API_DELTA.md` | Y | 新增 Manager 任务详情接口并调整删除语义 |
| `PROTOTYPE_DELTA.md` | Y | 管理后台新增查看按钮和弹窗 |
| `TEST_ACCEPTANCE.md` | Y | 固定生成 |
| `task.md` | Y | 固定生成 |
| `PRD_DELTA.md` | N | 当前需求已由本 change 明确，产品边界较小 |
| `API_INTERFACE.md` | N | 局部接口增量足以表达 |

## 6. 变更详情

### 6.1 变更前

- `POST /v2/video_generation` 校验请求后直接 marshal `CreateRequest` 并写入 `video_tasks.request_json`。
- `InputMaterializer` 在执行阶段读取 `request_json`，遇到 `data:` 才解码到 `os.CreateTemp("", "minimax-h3-input-*")`，导入 Node 后立即删除。
- 排队任务恢复依赖数据库里仍保留完整 Data URI。
- Manager 任务列表只展示 ID、客户、状态、实例、方式、规格、耗时、创建时间和操作。
- Manager 删除终态任务时调用 `AdminDelete`，本地任务进入 `deleted_at` 逻辑删除，远端产物通过删除作业异步处理。

### 6.2 变更后

- 创建任务时新增 `InputSpooler`：
  - 对 `image_url.url` 和 `audio_url.url` 中的 Data URI 做严格 Base64 解码。
  - 根据文件魔数和 MIME 识别真实格式，写入数据库文件同级目录下的 `temp-inputs/<task_id>/<input_id>.<ext>`；例如 `database.path=/data/minimax.db` 时保存到 `/data/temp-inputs`。
  - 写入时先生成 `.part`，校验 size/sha256 后原子 rename。
  - 将待入库请求中的 Data URI 改写为 `proxy-input://<task_id>/<input_id>`。
  - 在 `task_input_spool_files` 表保存媒体类型、角色、大小、sha256、相对路径、原始声明 MIME、检测 MIME 和 content 索引。
- 执行阶段 `InputMaterializer` 支持 `proxy-input://`：
  - 根据表记录和相对路径打开文件。
  - 再次校验路径位于输入临时目录下，校验 size/sha256。
  - 沿用既有 `ImportArtifact` multipart 导入 Node。
  - 导入成功后仍登记 `task_artifacts` 和 `artifact_locations`，但不删除 Proxy 输入临时文件；文件随任务保留，直到管理员物理删除该任务。
- Manager 新增任务详情接口和弹窗：
  - 请求快照来自脱 Base64 后的 `request_json`。
  - 媒体内容展示为引用和元数据，不返回 Base64 正文。
  - 文案、角色、URL 输入、Data URI 文件元数据和配置快照摘要均可查看。
- Manager 删除终态任务改为物理删除：
  - 删除前检查任务不存在 Node 调度屏障；存在屏障时返回 409，不删除任何 DB 行或文件。
  - 先读取该任务关联的远端 artifact location，并调用 Node 删除接口。
  - 远端删除全部成功或确认不存在后，开启事务物理删除该任务相关 DB 行。
  - 事务提交后删除数据库同级 `temp-inputs/<task_id>/`。
  - 如果远端 Node 不可达或删除失败，返回冲突或服务不可用，不提前删除 DB 线索。
- 数据库自动迁移：
  - 新增 `migrations/015_input_spool_admin_maintenance.sql`。
  - `migrations/embed.go` 嵌入该文件。
  - `internal/store/sqlite/store.go` 的迁移计划增加 v15。
  - 服务启动 `sqlite.Open` 时自动执行缺失迁移并更新 `schema_migrations` 与 `PRAGMA user_version`。

### 6.3 不在本次范围

- 不把临时文件改成对象存储、CDN 或公开 HTTP URL。
- 不改变外部 V2 创建任务、查询任务、取消任务的路径和字段。
- 不把 Base64 图片或音频再返回给 Manager 浏览器。
- 不为普通外部 API Key 增加查看其他用户请求的能力。
- 不迁移历史任务中的 Base64 正文到临时文件；历史任务仍按兼容路径执行和查看。
- 不自动强制删除活动任务；后台物理删除仅允许终态任务。
- 不新增“按保留期自动删除合法任务”的后台任务；本次只允许管理员显式删除任务，启动清理仅处理无 DB 关联的孤儿临时目录。

## 7. 兼容性与风险

- 旧任务的 `request_json` 仍可能包含 Data URI，执行阶段必须保留 legacy fallback，直到历史任务过期或被删除。
- 数据库备份不再包含新任务的输入二进制；备份排队和运行中任务时必须同时备份数据库同级 `temp-inputs/`。
- 如果正式环境升级后只复制数据库而不复制数据库同级 `temp-inputs/`，尚未执行完成的 Data URI 任务会缺少输入文件并失败。
- 物理删除一旦完成，任务详情、幂等记录、输入文件和产物位置记录均不可从数据库恢复；删除按钮必须明确提示不可恢复。
- 远端 Node 删除失败时不物理删除 DB，避免失去追踪能力；管理员可在 Node 恢复后重试删除。
- `cancelled` 状态不等于 Node 已可调度；存在 `node_dispatch_barriers` 的任务必须禁止物理删除，否则会丢失取消对账上下文并可能重现旧任务占用 Node 的问题。

## 8. 验收标准

- Given 请求包含图片或音频 Data URI，When 创建任务成功，Then `video_tasks.request_json` 不包含 `;base64,`，`task_input_spool_files` 存在元数据，本地临时文件字节与原始 Base64 解码结果一致。
- Given Proxy 重启且任务仍在排队，When 调度任务，Then 能从数据库文件同级 `temp-inputs` 目录读取输入并导入 Node 继续执行。
- Given 任务列表存在任意任务，When 管理员点击“查看”，Then 弹窗展示文案、媒体输入、角色、分辨率、比例、时长和配置摘要，且不返回 Base64 正文。
- Given 管理员删除终态任务且远端产物删除成功或已不存在，When 删除接口返回 204，Then 本地临时目录和 SQLite 中该任务相关记录均被物理删除。
- Given 管理员删除终态任务但远端 Node 删除失败，When 删除接口返回错误，Then 数据库记录和临时文件仍保留，可重试。
- Given cancelled 任务仍有关联 `node_dispatch_barriers`，When 管理员点击删除，Then 返回 409 且屏障、任务记录和本地临时文件均保留。
- Given 正式环境数据库停留在 v14，When 启动新版本 Proxy，Then 自动执行 v15 迁移，`schema_migrations` 和 `PRAGMA user_version` 更新成功。
