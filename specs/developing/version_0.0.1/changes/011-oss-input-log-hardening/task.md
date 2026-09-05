# 变更任务列表

## 核心任务

| 任务ID | 任务名称 | 优先级 | 状态 | 依赖 | 说明 |
|--------|---------|--------|------|------|------|
| CH-001 | Preflight 与范围确认 | High | Completed | - | 已读取规格导航和项目架构说明 |
| CH-002 | 回归测试先行 | High | In Progress | CH-001 | 为 OSS 重复提交和文件日志输出编写失败测试 |
| CH-003 | OSS 输入复用修复 | High | Pending | CH-002 | 相同请求命中已有对象输入时复用，不重复上传 |
| CH-003A | 输入元数据引用迁移 | High | Pending | CH-002 | 新增 v22 迁移，允许多个任务引用同一 relative_path |
| CH-004 | 异常日志增强 | High | Pending | CH-002 | 内部异常记录脱敏 error_reason |
| CH-005 | 容器日志目录 | Medium | Pending | CH-002 | 增加文件日志、Docker volume 与 compose 挂载 |
| CH-006 | 自测与回归 | High | Pending | CH-003, CH-003A, CH-004, CH-005 | 执行 gofmt、go test、go vet、go build |
| CH-007 | 文档同步 | Medium | Pending | CH-006 | 更新变更索引和交付说明 |

## 完成标准

- [ ] 重复相同 OSS 输入请求可成功创建多个任务，并且第二次不重复上传素材。
- [ ] 内部异常日志包含脱敏错误原因。
- [ ] Docker 容器支持将 `/var/log/minimax-proxy` 挂载到宿主机。
- [ ] 本地测试与构建通过。

## 下游触发点

- 开发执行对齐 `test-driven-development` 和 `systematic-debugging`。
- 完成前对齐 `verification-before-completion`。
