# MiniMax-H3 Node API 跨项目契约审计

> 本文记录修订前的缺陷证据。最终凭据结论已由 `006-node-single-key-conf` 取代：Node 与 Proxy 使用一个 32 位字母数字 Key，无 Key ID、scope 或复合 Token。

## 1. 审计信息

| 项目 | 内容 |
| --- | --- |
| 审计日期 | 2026-08-13 |
| Node 项目 | `E:/MiniMax-WorkFlow/Minimax-H3` |
| Proxy 项目 | `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy` |
| 协议 | `h3-node-v1` |
| 结论 | Node 路由完整；Proxy 集成存在阻塞级认证、取消和不确定结果恢复缺口 |

## 2. 已验证基线

- Node 与 Proxy 的 `node_v1_contract.json` 均列出 12 条同名路由，方法、路径、scope 和错误字段集合一致。
- 2026-08-13 执行 Node 鉴权、契约、执行、制品和清理定向测试，32 项全部通过。
- 2026-08-13 执行 Proxy `nodeapi`、Manager、Orchestrator、Profile、Cleanup 和 Artifact 定向测试，全部通过。
- 两边现有测试通过仍不足以证明集成正确：Proxy 测试把 `proxy.secret` 作为单一密钥传给客户端，恰好等同于 Node 所需的完整 Token，掩盖了真实数据库把 Key ID 与 Secret 分开存储的事实。

## 3. 路由覆盖矩阵

| 方法与路径 | Scope | Node | Proxy 客户端 | Proxy 生产消费 | 结论 |
| --- | --- | --- | --- | --- | --- |
| `GET /internal/v1/health` | `health` | 已实现 | 已实现 | Registry/连接测试 | 必需 |
| `GET /internal/v1/capabilities` | `health` | 已实现 | 已实现 | Registry/连接测试 | 必需 |
| `POST /internal/v1/executions` | `execute` | 已实现 | 已实现 | 阶段编排/配置测试 | 必需 |
| `GET /internal/v1/executions/{id}` | `execute` | 已实现 | 已实现 | 阶段编排/配置测试 | 必需 |
| `POST /internal/v1/executions/{id}/cancel` | `execute` | 已实现 | 已实现 | 未接入生产编排 | 阻塞缺口 |
| `POST /internal/v1/artifacts/import` | `artifact:write` | 已实现 | 已实现 | 输入跨节点迁移 | 必需 |
| `GET /internal/v1/artifacts/{id}` | `artifact:read` | 已实现 | 已实现 | 产物校验 | 必需 |
| `GET /internal/v1/artifacts/{id}/content` | `artifact:read` | 已实现 | 已实现 | 下载/跨节点迁移 | 必需 |
| `POST /internal/v1/artifacts/delete` | `artifact:delete` | 已实现 | 已实现 | Proxy 清理 Worker | 必需 |
| `POST /internal/v1/maintenance/cleanup/preview` | `maintenance` | 已实现 | 未实现 | 不消费 | 有意不消费 |
| `POST /internal/v1/maintenance/cleanup` | `maintenance` | 已实现 | 未实现 | 不消费 | 有意不消费 |
| `GET /internal/v1/maintenance/cleanup/{id}` | `maintenance` | 已实现 | 未实现 | 不消费 | 有意不消费 |

Proxy 实际需要 9 条路由；维护清理 3 条由 Node 自治使用。Proxy 已有基于任务、逻辑制品和物理位置的预览/删除任务，若再调用 Node 维护清理会绕过 Proxy 的候选集和审计记录，因此本版本不新增对应客户端方法。

## 4. 问题清单

| ID | 严重度 | 问题 | 证据 | 设计处置 |
| --- | --- | --- | --- | --- |
| H3-A01 | Critical | Proxy 出站只发送 Secret，Node 要求 `key_id.secret` | `nodeapi.Client` 只有 `apiKey`；Node `Authenticator._parse_bearer` 按首个点分割 | 客户端改用结构化凭据，集中组装 Token |
| H3-A02 | High | 编辑旧节点会静默切换协议且复用空密钥，SQLite CHECK 失败后返回 500 | 页面仅有 H3 选项；更新路径复制旧节点空密文；v7 约束要求 H3 密钥完整 | 旧协议只读 + 显式升级；Store 前稳定 400 校验 |
| H3-A03 | High | H3 执行取消接口未接入 Orchestrator | 客户端已有 `CancelExecution`，生产接口未暴露该方法 | 增加取消状态机和重启恢复 |
| H3-A04 | High | 提交响应未知或轮询短暂失败会结束 attempt，下一次生成新 operation，可能重复生成 | `CreateExecution/GetExecution` 错误进入 `FailStage` | 未知态保留 attempt、operation 和 execution，原地恢复 |
| H3-A05 | High | FastAPI/Pydantic 422 不使用统一错误包，Proxy 会把确定性参数错误按暂时故障重试 | App 只注册 `ServiceError` handler | Node 注册请求校验异常 handler；Proxy 按状态和 retryable 分类 |
| H3-A06 | Medium | Proxy Key ID 允许点号且上限 128，与 Node 1-64 且禁止点号不一致 | `validAPIName` 与 `APIKeyConfig` 规则不同 | 对节点 Key ID 使用专用校验器 |
| H3-A07 | Medium | 连接测试只验证 health scope，不能证明执行和制品 scope 完整 | health/capabilities 都仅需 `health` | capabilities 返回当前授权 scope，Proxy 校验必需集合 |
| H3-A08 | Medium | Proxy 丢弃 HTTP 错误中的 `retryable/request_id/details` | `HTTPError` 只保留状态、code、message | 保留 retryable 和 request_id；details 不写日志 |
| H3-A09 | Medium | 手写路由 fixture 只检查路径和错误字段，不覆盖请求/响应 schema | 两仓各维护一份静态 JSON | 从 FastAPI OpenAPI 归一化生成契约基线并做跨仓校验 |
| H3-A10 | Medium | 配置测试超时不会取消远端执行 | NodeTestExecutor 退出前未调用 cancel | 超时/主动取消时 best-effort 取消并轮询收口 |
| H3-A11 | Medium | Proxy 暴露 CFG，但 Node generation schema 和工作流不接受/使用 CFG | Profile 有 `cfg`，冻结请求无此字段；Node 使用 BasicGuider | 移除 H3 Profile CFG 编辑与新配置字段 |

## 5. 接口完整性结论

MiniMax-H3 的 `h3-node-v1` 发布路由在数量和功能面上完整，执行、取消、制品传输和清理均有接口。当前“不完整”发生在跨项目消费层：认证格式错误、取消未接线、错误和不确定结果语义没有端到端闭环。完成 `task.md` 的 MN-013 至 MN-021 后，Proxy 所需的 9 条路由才可判定为集成完整。

静态 12 路由 fixture 继续作为快速烟测，但正式契约源改为 Node 生成并提交的归一化 OpenAPI 快照；Proxy 在 CI 中验证所消费的路径、方法、认证、必需字段、枚举与统一错误包。新增可选响应字段应保持向前兼容，Proxy 不因无关扩展字段失败。

## 6. 人工确认

- 使用真实 Node 配置中的 Key ID 和原始 Secret，通过 Manager 新增节点并测试连接。
- 验证凭据含全部必需 scope；本版本不要求 `maintenance`。
- 在真实长任务运行中发起取消，确认 Node 与 Proxy 均进入 `cancelled` 且没有遗留 GPU 作业。
- 人为断开 Proxy 与 Node 的网络，分别覆盖提交响应未知和轮询短暂中断，确认没有重复 execution。
