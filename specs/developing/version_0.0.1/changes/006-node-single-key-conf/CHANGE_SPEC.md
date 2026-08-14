# Node 单 Key 与 conf.yml 凭据变更说明

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 目标版本 | `version_0.0.1` |
| 变更编号 | `006-node-single-key-conf` |
| 变更类型 | 安全优化 / 配置调整 / 接口调整 / 页面优化 |
| 优先级 | High |
| 提出日期 | 2026-08-13 |
| 负责人 | 待人工填写 |
| Preflight 执行情况 | 已执行：读取 `specs/README.md`、`PROJECT_OVERVIEW.md`、`ARCHITECTURE.md` |

## 2. 变更背景

- 当前 Node 使用 Key ID、Secret、scope 与复合 Bearer Token，WebUI 凭据还依赖环境变量；Proxy 节点表单和接口也要求 Key ID。
- 多份凭据来源和复合 Token 增加部署、录入和排障成本，并曾导致节点保存、升级和连接测试口径不一致。
- 本变更将 Node 凭据收敛到项目根目录的 `conf.yml`，并将 Proxy 节点配置收敛为单个 Key。
- 本变更覆盖 `E:/MiniMax-WorkFlow/Minimax-H3` 与 `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy` 两个仓库。
- 已通过前序需求澄清明确版本、角色、端、接口、数据库兼容、回滚风险和人工验收边界。

## 3. 目标结果

- Node 启动时按 `start.py` 所在目录定位 `conf.yml`。
- 文件不存在时一次性生成：32 位字母数字 API Key、固定用户名 `admin`、8 位且同时包含字母和数字的 WebUI 密码。
- 文件存在时仅严格读取；字段、类型或规则不合法则启动失败，绝不修复或覆盖。
- Node 所有受保护接口仅接受 `Authorization: Bearer <32位Key>`，不再使用 Key ID、scope 或复合 Token。
- Proxy 管理端节点配置只展示和提交一个 API Key，精确校验为 32 位字母数字。
- Proxy 保留 SQLite 旧 `api_key_id` 非空列作为兼容占位，不新增迁移；业务层不再读取、写入或返回其语义值。

## 4. 影响范围

| 影响项 | 是否影响 | 说明 |
| --- | --- | --- |
| 产品流程 | Y | 首次启动获取凭据和管理员录入节点流程变化 |
| 业务逻辑 | Y | Node 鉴权和 Proxy 节点密钥复用规则变化 |
| 数据库 | Y | 不改结构，旧列降级为兼容占位语义 |
| 接口契约 | Y | Manager DTO 与 Node Bearer 鉴权收敛为单 Key |
| 页面或交互 | Y | 节点表单移除 Key ID，API Key 限制为 32 位 |
| 原型 | Y | 记录局部字段与状态变化；不属于重大界面改版 |
| 配置部署 | Y | 新增 Node `conf.yml` 和 `conf.example.yml`，清理旧凭据环境变量 |
| 回归测试 | Y | Node 全路由、Proxy 节点管理、存储兼容及调用链 |

## 5. 文档生成决策

| 文档 | 是否生成 | 原因 |
| --- | --- | --- |
| `CHANGE_SPEC.md` | Y | 固定生成 |
| `task.md` | Y | 固定生成 |
| `TEST_ACCEPTANCE.md` | Y | 固定生成 |
| `PRD_DELTA.md` | Y | 首次启动、管理员配置和鉴权规则变化 |
| `PROTOTYPE_DELTA.md` | Y | 管理端节点表单字段和校验交互变化 |
| `TECH_SOLUTION.md` | Y | 跨仓库、安全配置、并发首次启动和兼容边界复杂 |
| `API_DELTA.md` | Y | Manager 字段及 Node 鉴权契约变化 |
| `API_INTERFACE.md` | N | 局部契约变化，增量文档足以表达 |
| `DATABASE_DELTA.md` | Y | 无 DDL，但旧列业务语义和写入策略变化 |
| `DATABASE_DESIGN.md` | N | 数据模型结构不变 |

## 6. 变更详情

### 6.1 变更前

- Node 凭据可由环境变量承载，API Token 包含 Key ID 和 Secret，并按 scope 授权。
- WebUI 用户名和密码哈希与 API 凭据分开配置。
- Proxy 节点创建、编辑和测试包含 `api_key_id` 与 `api_key`。
- SQLite `model_service_nodes.api_key_id` 被应用层视为有效业务字段。

### 6.2 变更后

- Node 凭据唯一来源是根目录 `conf.yml`；旧凭据环境变量即使存在也必须忽略。
- `api.key` 必须匹配 `^[A-Za-z0-9]{32}$`。
- `webui.username` 必须为 `admin`；`webui.password` 必须匹配 8 位字母数字且至少包含一个字母和一个数字。
- 首次生成使用密码学安全随机源和原子创建，生成文件不得被日志输出。
- Node 应用配置不通过环境变量可覆盖的通用 Settings 模型承载凭据；加载结果作为独立凭据对象显式注入应用和 WebUI 鉴权。
- Proxy 创建、升级和连接测试要求提供一个 Key；编辑时 Key 留空表示复用已保存密文。
- `api_key_id` 从 Manager 请求、响应、领域模型和业务比较中移除；旧数据库列写空字符串并在读取时忽略。
- 本变更取代 `004-manager-node-configuration` 中有关 Key ID、scope、`key_id.secret` 的活动规则。

### 6.3 不在本次范围

- 不修改 Proxy 面向外部客户的 API Key 管理和任务归属标识。
- 不提供 Node 多 Key、Key 轮换、scope、WebUI 改密页面或在线编辑 `conf.yml`。
- 不删除 SQLite 旧列，不新增数据库迁移。
- 不重做管理后台导航或整体视觉体系。

## 7. 兼容性与风险

- 旧 Node 复合 Token 不再兼容；升级后 Proxy 管理员必须录入 `conf.yml` 中的新单 Key。
- 已保存 H3 节点继续复用加密 Key；历史 Key ID 被忽略。Legacy 节点升级仅需一个 Key。
- 既有非法 `conf.yml` 会阻止启动，这是防止静默替换生产凭据的预期行为。
- 首次并发启动存在重复生成风险，必须以原子独占创建解决，并让失败进程重新读取已创建文件。
- 回滚需同时恢复 Node 与 Proxy 的旧鉴权契约；回滚前保留 `conf.yml`，不得自动恢复旧环境变量为有效来源。
- `conf.yml` 为明文敏感文件，必须加入 `.gitignore`，示例文件只使用无效示例值。

## 8. 验收标准

- Given Node 根目录无 `conf.yml`，When 首次启动，Then 仅生成一份符合长度和字符规则的配置并可用于 API 与 WebUI 登录。
- Given 合法 `conf.yml` 已存在，When 重启，Then 文件内容、摘要和修改时间保持不变。
- Given 配置缺字段、类型错误或凭据不合法，When 启动，Then 进程明确失败且原文件不变。
- Given 旧凭据环境变量存在，When Node 启动，Then 实际鉴权仍只采用 `conf.yml`。
- Given 调用任一受保护 Node 路由，When 使用单 Key Bearer，Then 鉴权成功；使用复合 Token、Key ID 或错误 Key 时返回 401。
- Given 管理员创建或升级 H3 节点，When 提交 32 位字母数字 Key，Then 保存和连接测试成功；请求包含 `api_key_id` 时返回 400。
- Given 管理员编辑已有 H3 节点且 Key 留空，When 保存或测试，Then 复用已保存密文且不泄露明文。
- Given 旧 v7 数据库含非空 `api_key_id`，When 启动和读取节点，Then 无需迁移且该值不进入业务响应。
