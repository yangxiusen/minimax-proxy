# 测试验收准备

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 版本/变更 | `010-input-spool-admin-maintenance` |
| 测试范围 | V2 创建任务、输入临时文件、Manager 任务详情、物理删除、数据库自动迁移 |
| 输入文档 | `CHANGE_SPEC.md`、`TECH_SOLUTION.md`、`DATABASE_DELTA.md`、`API_DELTA.md`、`PROTOTYPE_DELTA.md`、`task.md` |
| 状态 | Draft |

## 2. 测试范围

| 模块 | 功能点 | 测试类型 | 是否人工确认 |
| --- | --- | --- | --- |
| V2 创建 | Data URI 入库前落盘 | 功能/安全 | 否 |
| Orchestrator | `proxy-input://` 导入 Node | 功能/兼容 | 否 |
| Manager | 查看请求详情 | 功能/页面 | 是 |
| Manager | 任务物理删除 | 功能/一致性 | 否 |
| SQLite | v14 自动升级到 v15 | 迁移/兼容 | 否 |
| 运维 | 正式环境备份和升级 | 人工确认 | 是 |

## 3. 功能测试清单

| 用例ID | 场景 | 前置条件 | 操作步骤 | 预期结果 | 结果 |
| --- | --- | --- | --- | --- | --- |
| FT-001 | 图片 Data URI 创建任务 | 准备 PNG/JPEG Base64 | 调用 V2 创建任务 | DB `request_json` 不含 Base64，本地保存原格式文件 | 待执行 |
| FT-002 | 音频 Data URI 创建任务 | 准备 WAV/MP3 Base64 | 调用 V2 创建任务 | 保存 `.wav` 或 `.mp3`，size/sha256 正确 | 待执行 |
| FT-003 | DB 创建失败清理文件 | 注入 Store 创建失败 | 调用 V2 创建任务 | 本次 task 临时目录被删除 | 待执行 |
| FT-004 | Proxy 重启后继续排队任务 | 创建含 Data URI 的 queued 任务 | 重启 Store/Orchestrator 后执行 | 任务能读取 spool 文件并导入 Node | 待执行 |
| FT-005 | Node 导入失败后重试 | fake Node 首次导入失败 | 触发 stage 重试 | 本地输入文件保留，重试可再次读取 | 待执行 |
| FT-006 | Manager 查看详情 | 存在含文本、图片、音频的任务 | 点击“查看” | 展示文案、媒体元数据和设置，不显示 Base64 | 待人工确认 |
| FT-007 | 删除终态任务 | 任务已 succeeded/failed/cancelled | 点击删除并确认 | 远端删除成功后 DB 行和本地目录物理删除 | 待执行 |
| FT-008 | 删除时 Node 不可达 | 成功任务有远端产物且 fake Node 失败 | 点击删除 | 返回错误，DB 和本地目录保留 | 待执行 |
| FT-009 | 历史 Base64 任务兼容 | 旧 DB 中任务仍含 Data URI | 执行或查看任务 | 执行 fallback 成功；详情隐藏 Base64 正文 | 待执行 |
| FT-010 | v14 自动迁移 | 准备 user_version=14 的 DB | 启动 Store | 自动创建表并写 v15 | 待执行 |
| FT-011 | cancelled 但屏障未解除时删除 | cancelled 任务存在 `node_dispatch_barriers` | 点击删除 | 返回 409，屏障、任务和临时目录均保留 | 待执行 |
| FT-012 | 幂等重复请求不重复落盘 | 已有相同 Idempotency-Key 和 request_hash | 再次提交同一 Base64 请求 | 直接返回原 task_id，不新增临时目录 | 待执行 |
| FT-013 | 外键顺序正确 | 成功任务有 stage、attempt、artifact、callback | 物理删除 | `PRAGMA foreign_key_check` 无违规 | 待执行 |
| FT-014 | 启动孤儿清理不误删任务文件 | 同时存在合法任务目录、`.part` 文件和无 DB 关联旧目录 | 启动孤儿清理 | 只删除孤儿/候选文件，合法任务目录保留 | 待执行 |

## 4. 接口测试清单

| 接口 | 场景 | 请求重点 | 响应重点 | 结果 |
| --- | --- | --- | --- | --- |
| `POST /v2/video_generation` | Data URI 成功 | 图片/音频 Base64 | 返回 task_id，DB 无 Base64 | 待执行 |
| `GET /manager/api/tasks/{task_id}` | 详情成功 | Manager Cookie | 返回 request/config/spool 元数据 | 待执行 |
| `GET /manager/api/tasks/{task_id}` | 未登录 | 无 Cookie | 401 | 待执行 |
| `DELETE /manager/api/tasks/{task_id}` | 删除成功 | 终态任务 | 204 | 待执行 |
| `DELETE /manager/api/tasks/{task_id}` | 非终态 | queued/running | 409 | 待执行 |
| `DELETE /manager/api/tasks/{task_id}` | 远端失败 | fake Node 5xx | 503 且数据保留 | 待执行 |
| `DELETE /manager/api/tasks/{task_id}` | 取消屏障存在 | cancelled + barrier | 409 且数据保留 | 待执行 |

## 5. 数据一致性检查

| 流程 | 涉及表 | 检查点 | 结果 |
| --- | --- | --- | --- |
| 创建任务 | `video_tasks/task_input_spool_files/task_stages` | 任务和输入元数据同事务可见 | 待执行 |
| 幂等创建 | `idempotency_keys/video_tasks/task_input_spool_files` | 重复请求返回原任务，不遗留新目录 | 待执行 |
| 物理删除 | 所有任务关联表 | 删除后按 task_id 查不到关联行 | 待执行 |
| 物理删除外键 | `video_tasks/task_stages/stage_attempts/task_artifacts/artifact_locations/callback_deliveries/node_dispatch_barriers` | 删除完成后 `PRAGMA foreign_key_check` 为 0 行 | 待执行 |
| 自动迁移 | `schema_migrations` | v15 只执行一次，重复启动无副作用 | 待执行 |
| 孤儿清理 | `video_tasks/task_input_spool_files` + `temp-inputs` | 无 DB 关联旧目录可删除，有 DB 关联目录必须保留 | 待执行 |

## 6. 安全检查

| 检查项 | 标准 | 验证方式 | 结果 |
| --- | --- | --- | --- |
| 鉴权 | 未登录不能访问 Manager 详情和删除 | 单元测试 | 待执行 |
| 输入校验 | 非法 Base64、空文件、MIME 不匹配被拒绝 | 单元测试 | 待执行 |
| 路径安全 | `proxy-input://` 不能越权访问数据库同级 `temp-inputs` 之外的路径 | 单元测试 | 待执行 |
| 敏感数据 | 日志和 Manager 详情不输出 Base64、Key、绝对路径 | 单元测试/人工确认 | 待人工确认 |

## 7. 性能与稳定性检查

| 检查项 | 目标 | 验证方式 | 结果 |
| --- | --- | --- | --- |
| SQLite 体积 | 新任务 request_json 随媒体大小不线性增长 | 本地 DB size 对比 | 待执行 |
| 大请求 | 64 MiB 请求体上限保持有效 | 单元测试 | 待执行 |
| 长时间运行 | 排队任务跨重启不丢输入 | 本地重启测试 | 待执行 |

## 8. 人工联调准备

| 页面/端 | 接口 | 检查点 | 问题记录位置 |
| --- | --- | --- | --- |
| Manager | `GET /manager/api/tasks/{task_id}` | 弹窗字段、长文案、媒体列表、历史任务提示 | 变更 issue 或测试记录 |
| Manager | `DELETE /manager/api/tasks/{task_id}` | 删除不可恢复提示、远端失败提示、中止对账未完成提示 | 变更 issue 或测试记录 |
| 正式环境 | 启动自动迁移 | 备份 SQLite 和数据库同级 `temp-inputs`、启动日志、v15 检查 | 发布记录 |

## 9. 本地验证命令

| 类型 | 命令 | 预期 |
| --- | --- | --- |
| 单元测试 | `go test ./internal/httpapi/v2 ./internal/orchestrator ./internal/store/sqlite ./internal/httpapi/manager` | 通过 |
| 全量测试 | `go test ./...` | 通过 |
| 静态检查 | `go vet ./...` | 无阻塞问题 |
| 构建 | `go build ./cmd/server ./cmd/healthcheck` | 成功 |

## 10. 人工确认项

| 确认项 | 原因 | 负责人/角色 | 状态 |
| --- | --- | --- | --- |
| Manager 查看内容满足排查需要 | 需要业务/运营判断 | 产品/业务/研发 | 待人工确认 |
| 真实 Node 删除产物行为 | 需要真实节点服务 | 测试/研发 | 待人工确认 |
| 正式环境备份策略包含数据库同级 `temp-inputs` | 关系到排队任务恢复 | 运维/研发 | 待人工确认 |
| 上线确认 | 需要发布流程 | 负责人 | 待人工确认 |

## 11. 问题记录

| 问题ID | 问题 | 严重程度 | 复现步骤 | 处理状态 |
| --- | --- | --- | --- | --- |
| 待记录 | 暂无 | - | - | Pending |
