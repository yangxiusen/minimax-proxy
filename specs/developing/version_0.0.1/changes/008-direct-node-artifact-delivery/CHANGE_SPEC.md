# 节点直出视频签名链接变更说明

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 目标版本 | `version_0.0.1` |
| 变更编号 | `008-direct-node-artifact-delivery` |
| 变更类型 | 架构优化 / 接口调整 / 配置简化 |
| 优先级 | High |
| 提出日期 | 2026-08-14 |
| 负责人 | 待人工填写 |
| Preflight 执行情况 | 已执行：读取 `specs/README.md`、`PROJECT_OVERVIEW.md`、`ARCHITECTURE.md` |

## 2. 变更背景

- 当前成功任务返回 Proxy 的 `/v2/files/{artifact_id}/content` 签名地址，Proxy 再使用 Node API Key 从节点读取并向调用方流式转发视频。
- 每次播放或下载都会占用 Proxy 的出口带宽和长连接；节点与 Proxy 分机部署时还会产生节点到 Proxy 的额外传输。
- 节点配置已经在 SQLite 中保存根 `service_url` 和加密 Node API Key。经需求确认，`service_url` 同时可被 Proxy 与外部调用方访问，并允许在现有 `7860` 服务新增公开签名下载路由。
- Node 当前 `/internal/v1/artifacts/{artifact_id}/content` 必须携带 Node API Key，不能直接返回给外部用户，也不能把 Node API Key 暴露给调用方。

## 3. 目标结果

- Proxy 为成功任务生成指向对应节点 `service_url` 的 48 小时 HMAC 签名视频地址。
- 调用方直接从节点下载或播放视频，视频正文不再经过 Proxy。
- 签名地址在 48 小时内可重复访问并支持单 Range 请求；任何持有完整链接的人均可访问。
- Proxy V2 查询与 Manager 任务列表的字段结构保持不变，仅 URL 的主机和路径语义改变。
- `server.public_base_url` 与 `MINIMAX_PUBLIC_BASE_URL` 不再是 Proxy 启动配置。

## 4. 影响范围

| 影响项 | 是否影响 | 说明 |
| --- | --- | --- |
| 产品流程 | Y | 视频交付从 Proxy 中转改为 Node 直出 |
| 业务逻辑 | Y | Proxy 定位 Node artifact 并签名；Node 验签后输出文件 |
| 数据库 | N | 复用现有节点、逻辑产物和位置记录，无迁移 |
| 接口契约 | Y | 新增 Node 公开下载路由，现有结果 URL 语义变化 |
| 页面或交互 | N | Manager 播放按钮和交互不变 |
| 原型 | N | 无页面结构或交互变化 |
| 配置部署 | Y | 删除 Proxy 公共根地址配置；要求节点 `service_url` 外部可达 |
| 回归测试 | Y | 覆盖签名、过期、篡改、Range、任务查询和旧链接兼容 |

## 5. 文档生成决策

| 文档 | 是否生成 | 原因 |
| --- | --- | --- |
| `CHANGE_SPEC.md` | Y | 固定生成 |
| `task.md` | Y | 固定生成 |
| `TEST_ACCEPTANCE.md` | Y | 固定生成 |
| `PRD_DELTA.md` | Y | 视频交付规则和安全边界变化 |
| `PROTOTYPE_DELTA.md` | N | 页面与交互不变 |
| `TECH_SOLUTION.md` | Y | 跨 Go/Python 项目共享签名契约，涉及兼容与安全风险 |
| `API_DELTA.md` | Y | 新增 Node 路由，现有响应 URL 语义变化 |
| `API_INTERFACE.md` | N | 局部接口增量足以表达 |
| `DATABASE_DELTA.md` | N | 无表结构、索引或数据修复 |
| `DATABASE_DESIGN.md` | N | 数据模型不变 |

## 6. 变更详情

### 6.1 变更前

- Proxy 使用 `server.public_base_url` 构造自身 `/v2/files` 的绝对签名 URL。
- Proxy 校验外部签名或 Bearer 所有权，再携带 Node API Key 请求节点内部文件接口并转发正文。
- 节点没有面向外部的临时签名文件路由。

### 6.2 变更后

- Proxy 根据逻辑 artifact 的活动位置取得 `node_artifact_id`、节点 `service_url` 和解密后的 Node API Key。
- Proxy 与 Node 使用固定、带版本的 HMAC-SHA256 契约生成和验证 URL-safe、无填充签名；签名绑定 `GET`、节点 artifact ID 和过期时间。
- 新 URL 格式为 `{service_url}/public/v1/artifacts/{node_artifact_id}/content?expires=...&signature=...`。
- Node 公共路由不接受 Node Bearer，只有签名有效且 artifact 仍为活动状态时返回文件；沿用现有 Content-Type、ETag、单 Range 和完整文件输出行为。
- 每次查询成功任务都会生成从当前时间起 48 小时有效的新链接。Node API Key 轮换会立即使旧链接失效。
- Proxy 保留既有 `/v2/files` 路由用于已签发链接的短期兼容，但新查询和 Manager 播放地址不再指向它。
- Proxy 删除 `server.public_base_url` 的必填解析和 Docker 环境变量要求。

### 6.3 不在本次范围

- 不新增对象存储、CDN、缓存或独立文件服务。
- 不新增节点公网地址字段；直接复用数据库中的根 `service_url`。
- 不支持一次性链接、永久公开链接、多 Range 或目录访问。
- 不修改任务表、artifact 表、节点表或 Manager 页面。
- 不允许外部用户访问任何 `/internal/v1/*` 路由，也不返回 Node API Key。

## 7. 兼容性与风险

- V2 和 Manager 响应字段兼容，但调用方必须能访问节点 `service_url`；仅内网地址、容器名和 `host.docker.internal` 不满足部署要求。
- 完整签名 URL 在 48 小时内属于 bearer credential，可被转发；日志不得记录 query string 或完整 URL。
- Node 与 Proxy 时钟偏差可能导致提前过期，验签允许最多 5 分钟未来窗口余量，但生产仍需同步系统时间。
- Node API Key 轮换会使已签发链接失效；重新查询任务可使用新 Key 生成新链接。
- 回滚时恢复 Proxy URL 签发和 `MINIMAX_PUBLIC_BASE_URL`；保留 `/v2/files` 路由可降低回滚期间的链接中断。

## 8. 验收标准

- Given 成功任务拥有活动 Node artifact，When 调用 V2 单项、列表或 Manager 任务查询，Then 返回节点 `service_url` 下有效 48 小时的签名 URL。
- Given 有效签名 URL，When 完整下载、重复访问或发送合法单 Range，Then Node 分别返回 `200` 或 `206`，Proxy 不传输视频正文。
- Given 签名、artifact ID 或过期时间被篡改，或链接已经过期，When 请求 Node 公共路由，Then 返回 `403` 且不泄露文件和 Node Key。
- Given artifact 不存在、已删除或非活动，When 使用形式正确的签名请求，Then 返回 `404`。
- Given Proxy 配置未包含 `server.public_base_url` 且容器未设置 `MINIMAX_PUBLIC_BASE_URL`，When 启动，Then 配置加载成功。
- Given 升级前已签发且尚未过期的 Proxy `/v2/files` URL，When 请求该 URL，Then 兼容路由仍按原签名规则处理。
