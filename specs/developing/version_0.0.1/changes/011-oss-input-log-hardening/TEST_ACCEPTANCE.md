# 测试与验收

## 1. 测试基本信息

| 项目 | 内容 |
|------|------|
| 目标版本 | `version_0.0.1` |
| 变更编号 | `011-oss-input-log-hardening` |
| 测试日期 | 2026-09-06 |
| 测试负责人 | Codex / 人工复核 |

## 2. 测试范围

- 变更模块：`internal/httpapi/v2`、`internal/inputobject`、`cmd/server`、Docker 部署文件。
- 回归模块：SQLite 任务创建、对象存储输入元数据、服务启动日志。
- 不在范围：真实 MiniMax 官方提交、真实 UCloud 上传联调。

## 3. 核心用例

| 用例ID | 场景 | 前置条件 | 操作步骤 | 预期结果 | 实际结果 | 状态 |
|--------|------|----------|----------|----------|----------|------|
| TC-001 | 相同 OSS 输入重复提交 | 对象存储输入直传启用 | 两次提交相同 Base64 图片请求 | 两次均入库，第二次复用第一次的输入对象且不重复上传 | 待执行 | Pending |
| TC-002 | 创建接口内部异常日志 | 注入 store 失败 | 调用创建接口 | 500 响应，同时日志含脱敏 error_reason | 待执行 | Pending |
| TC-003 | 文件日志输出 | 设置日志目录 | 启动 server 日志初始化 | stdout 与 server.log 均收到 JSON 日志 | 待执行 | Pending |

## 4. 回归验证

- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ] `go build ./cmd/server ./cmd/healthcheck`
- [ ] 人工确认 Docker 挂载 `./logs:/var/log/minimax-proxy` 后可查看 `server.log`
- [ ] 人工确认重复请求在真实 OSS 侧只产生一次输入对象写入

## 5. AI 边界

- 自动化测试和本地构建可由 AI 完成。
- 真实容器运行、宿主机挂载目录权限、生产日志采集由人工确认。
