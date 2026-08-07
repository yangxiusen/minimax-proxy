# 监控控制台变更任务

| 任务ID | 任务名称 | 优先级 | 状态 | 依赖 |
| --- | --- | --- | --- | --- |
| CH-001 | Preflight、影响分析与文档确认 | High | Completed | - |
| CH-002 | 增加 `admin` 配置、默认值、校验及配置测试 | High | Completed | CH-001 |
| CH-003 | 以测试驱动实现线程安全节点快照缓存和防御性上游状态解析 | High | Completed | CH-002 |
| CH-004 | 将 Worker 观察值接入缓存，并实现空闲节点 5 秒采集器 | High | Completed | CH-003 |
| CH-005 | 增加跨客户管理任务查询 Store 方法及过滤测试 | High | Completed | CH-003 |
| CH-006 | 实现管理会话、鉴权中间件、登录限速和接口测试 | High | Completed | CH-002 |
| CH-007 | 实现快照、任务列表管理 API 和嵌入式只读页面 | High | Completed | CH-004、CH-005、CH-006 |
| CH-008 | 创建接口接入节点可用性判断和 503 契约测试 | High | Completed | CH-003 |
| CH-009 | 补充加载、空、异常、过期缓存及移动端样式 | Medium | Completed | CH-007 |
| CH-010 | 运行 `go test ./...`、`go vet ./...` 和 Docker 构建 | High | Blocked | CH-008、CH-009 |
| CH-011 | 执行代码审查并修复发现 | High | Completed | CH-010 |
| CH-012 | 同步配置示例、README、接口及运维文档 | Medium | Completed | CH-011 |

## 完成标准

- [x] 自动化测试和静态检查通过
- [x] 页面与接口符合已确认原型和 `API_DELTA.md`
- [x] 关键代理、登录、探测和 503 分支均有中文结构化日志
- [x] 不泄露真实 Key、密码、内部 URL和上游原始日志
- [ ] 用户按 `TEST_ACCEPTANCE.md` 完成人工验收并回复“已完成”

## 下游触发点

- `/s-develop`：按 `test-driven-development` 实现，异常使用 `systematic-debugging`，完成后执行 `requesting-code-review`。
- `/s-archive`：按 `verification-before-completion` 核验证据，再执行 `finishing-a-development-branch`。

人工联调、真实环境验收、性能验收和发布确认不作为 AI 可完成任务，统一记录于 `TEST_ACCEPTANCE.md`。

CH-010 阻塞说明：2026-08-07 已通过 Go 测试、静态检查和 Windows 二进制构建；Docker Desktop Linux 引擎未启动，镜像构建待环境恢复后执行。
