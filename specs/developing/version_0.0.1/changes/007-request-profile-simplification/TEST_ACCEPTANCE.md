# 模型请求配置简化测试与验收准备

## 1. 自动化用例

| ID | 场景 | 预期 |
| --- | --- | --- |
| PS-001 | v8 -> v11 有多版本/场景数据 | 每 resolution 只保留 updated_at/id 最新一条，外键完整 |
| PS-002 | 同 resolution 重复创建 | 409，不产生第二条 |
| PS-003 | 更新配置 | row_version 增加，下一新任务读取新配置 |
| PS-004 | 并发更新 | 一个成功，旧 row_version 返回 409 |
| PS-005 | 删除有历史/运行任务引用的配置 | task.profile_id 置空，配置删除，阶段快照不变 |
| PS-006 | 删除后创建任务 | 返回无匹配配置 |
| PS-007 | 三种任务场景 | 共用同 resolution Profile，场景和 ratio 映射正确 |
| PS-008 | 新 Profile payload | 旧字段按严格 JSON 规则返回 400 |
| PS-009 | watermark 省略/false | 创建成功，无 watermark stage |
| PS-010 | watermark true | 恰有一个 watermark stage |
| PS-011 | generation 参数 | fps=24，模型跟随模式，无 cfg |
| PS-012 | Manager Web | 无版本/场景/无效参数/测试发布控件，具备删除操作 |

## 2. 本地验证命令

| 类型 | 命令 | 预期 |
| --- | --- | --- |
| 定向测试 | `go test ./internal/store/sqlite ./internal/profile ./internal/httpapi/manager ./internal/httpapi/v2` | 通过 |
| 全量测试 | `go test ./...` | 通过 |
| 静态检查 | `go vet ./...` | 无阻塞问题 |
| 构建 | `go build ./cmd/server ./cmd/healthcheck` | 成功 |

## 3. 人工确认项

| 确认项 | 原因 | 状态 |
| --- | --- | --- |
| 保存/删除即时生效 | 本地服务真实浏览器操作 | 已确认：保存后列表立即出现，删除后立即回到空态 |
| 三场景真实生成 | 需 GPU 节点 | 待人工确认 |
| 有/无水印画面 | 需真实媒体肉眼检查 | 待人工确认 |
| v8 数据库升级 | 自动化构造多版本、任务引用和测试数据 | 已通过；生产备份副本演练仍由部署阶段执行 |
| 管理页面桌面/移动验收 | Playwright 真实浏览器，1280px 与 390x844 | 已通过；移动比例区无横向溢出 |

## 4. 实施验证记录

- `go test ./... -count=1`：通过。
- `go vet ./...`：通过。
- `go build ./cmd/server ./cmd/healthcheck`：通过。
- `git diff --check`：通过，仅有工作区既有 CRLF 转换提示。
- 独立代码审查发现的 Profile 删除竞态和 `custom model_mode` 问题均已修复并回归。
