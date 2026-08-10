# 音频 Base64 输入开发任务

## 任务总览

| 任务ID | 任务名称 | 优先级 | 状态 | 依赖 |
| --- | --- | --- | --- | --- |
| AB-001 | 完成变更规格、接口增量和技术方案 | High | Completed | - |
| AB-002 | 为音频 Data URI 校验补充失败测试 | High | Completed | AB-001 |
| AB-003 | 实现 MIME、Base64 与 15 MiB 校验 | High | Completed | AB-002 |
| AB-004 | 为 Gradio 上传与参数准备补充失败测试 | High | Completed | AB-003 |
| AB-005 | 实现上传和首次提交/重试参数准备 | High | Completed | AB-004 |
| AB-006 | 同步主接口文档并运行全量验证 | High | Completed | AB-005 |
| AB-007 | 代码审查并修复阻塞问题 | High | Completed | AB-006 |

## 完成标准

- [x] 音频 URL 和指定音频 Data URI 均可进入任务链路。
- [x] 音频 Data URI 通过 Gradio 上传协议变成私有缓存路径并写入音频槽位。
- [x] 视频非 URL 的提示和图片双输入能力不回归。
- [x] 自动化测试、静态检查和构建通过。
- [x] 真实私有服务联调项保留为人工确认，不伪造完成状态。

## 本地验证证据

- `go test ./... -count=1`：通过。
- `go vet ./...`：通过。
- `go build ./cmd/server ./cmd/healthcheck`：通过。
- 真实 WAV/MP3 生成、私有服务重启重试和生产反向代理大小限制：待人工确认。
