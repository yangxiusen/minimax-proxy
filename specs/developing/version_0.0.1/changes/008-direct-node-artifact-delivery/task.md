# 节点直出视频变更任务

## 任务状态

| 状态 | 说明 |
| --- | --- |
| Pending | 未开始 |
| In Progress | 进行中 |
| Completed | 已完成 |
| Blocked | 阻塞 |

## 核心任务

| 任务ID | 任务名称 | 优先级 | 状态 | 依赖任务 | 说明 |
| --- | --- | --- | --- | --- | --- |
| DA-001 | Preflight 与范围确认 | High | Completed | - | 已确认版本、两个仓库、48 小时 TTL、持链访问和 `service_url` 外部可达假设 |
| DA-002 | 变更文档与接口契约 | High | Completed | DA-001 | 固化 HMAC、Range、配置删除、兼容与人工验收边界 |
| DA-003 | Node 签名工具 TDD | High | Completed | DA-002 | 固定向量、过期、未来超限、篡改和非法参数测试已先失败后通过 |
| DA-004 | Node 公共下载路由 TDD | High | Completed | DA-003 | 公开完整/Range、篡改和删除状态测试已先失败后通过 |
| DA-005 | Proxy 直接 URL Signer TDD | High | Completed | DA-002 | 活动位置、Node Key、48 小时 URL 和跨语言固定向量测试已通过 |
| DA-006 | 切换 V2 与 Manager URL | High | Completed | DA-005 | Signer 接口已增加 context，响应 DTO 保持不变，定向测试通过 |
| DA-007 | 移除 Proxy 公共根地址配置 | High | Completed | DA-006 | 已清理 Go 配置、YAML、Docker env 示例和 README；缺少旧变量可启动 |
| DA-008 | 保留旧 Proxy 文件路由回归 | High | Completed | DA-006 | 旧路由、owner Bearer、签名和 Range 回归随 Proxy 全量测试通过 |
| DA-009 | 跨语言签名契约验证 | High | Completed | DA-003, DA-005 | Go/Python 固定向量逐字节一致 |
| DA-010 | 两仓自测与构建 | High | Completed | DA-004, DA-007, DA-008, DA-009 | Node 293 tests；Proxy test/vet/build 全部通过；应用日志未记录签名和 Key |
| DA-011 | 代码审查与问题修复 | High | Completed | DA-010 | 已审查安全、兼容、日志和跨语言一致性，无未关闭 Critical/Important 问题 |
| DA-012 | 稳定文档同步 | Medium | Completed | DA-011 | 已更新 README、安全、架构、接口和部署运维说明，标明流量路径变化 |

## 无需执行的条件任务

| 任务 | 结论 | 原因 |
| --- | --- | --- |
| 数据库迁移 | 不需要 | 完全复用现有 artifact location 和 node 记录 |
| 页面/原型调整 | 不需要 | Manager 仍消费 `video_url`，交互不变 |
| 新增公网地址字段 | 不需要 | 用户确认复用根 `service_url` |

## 完成标准

- [x] Node 与 Proxy 实现 `API_DELTA.md` 的固定签名契约。
- [x] 新任务查询仅返回 Node 公共签名 URL，正文不经过 Proxy。
- [x] 48 小时内完整下载、重复访问和单 Range 自动化测试通过。
- [x] 篡改、过期、未来超限、缺失 artifact 和 Key 轮换测试通过。
- [x] Proxy 不配置 `server.public_base_url`/`MINIMAX_PUBLIC_BASE_URL` 可启动。
- [x] 旧 `/v2/files` 自动化回归通过。
- [x] 两仓测试、静态检查和构建通过。
- [x] 代码审查的 Critical/Important 问题关闭。
- [x] 文档同步完成。
- [ ] `TEST_ACCEPTANCE.md` 的真实网络、流量和发布项由人工确认。

## 下游 Superpowers 触发点

- `/s-develop`：对齐 `test-driven-development`；遇到测试失败使用 `systematic-debugging`；两个项目实现完成后使用 `requesting-code-review`。
- `/s-archive`：对齐 `verification-before-completion` 和 `finishing-a-development-branch`，不得以自动化测试代替真实网络与生产流量验收。

## AI 边界

- AI 可以实现两个仓库的代码、运行本地自动化验证、审查和同步文档。
- 外部网络可达性、防火墙/反向代理、真实视频流量、48 小时真实等待验证和生产发布批准必须由人工确认。
