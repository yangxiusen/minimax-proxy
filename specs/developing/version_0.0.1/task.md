# v0.0.1 开发任务清单

实施顺序按依赖排列。每项完成时同时提交测试，不将 `docs/` 引入运行时依赖。

当前执行状态：本地开发与自动化验证已完成，待真实私有服务人工联调。

## 1. 工程骨架与配置

- [x] 创建 `go.mod`、`cmd/server/main.go`、`cmd/healthcheck/main.go` 和 `internal/config/`。
- [x] 实现 UTF-8 YAML、`${ENV}` 展开、默认值与严格启动校验。
- [x] 添加 `config.example.yaml`，覆盖 API Key、SQLite、队列、上游和生成 profile。
- [x] 测试缺失环境变量、重复 ID/Key、非法 URL、缺失尺寸映射和不可写数据目录。

验证：`go test ./internal/config/... && go build ./cmd/server ./cmd/healthcheck`。

## 2. 领域模型与 SQLite

- [x] 在 `internal/domain/` 定义任务、内部状态、V2 状态和错误码。
- [x] 创建 `migrations/001_init.sql` 及 `internal/store/sqlite/`。
- [x] 实现迁移、WAL、busy timeout、任务、幂等和清理仓储。
- [x] 实现 `BEGIN IMMEDIATE` 创建、领取、取消/删除和保护位重算事务。
- [x] 测试部分唯一索引、FIFO、额度、并发竞争、7 天可见性和重启持久化。

验证：`go test -race ./internal/store/sqlite/...`。

## 3. V2 协议层

- [x] 在 `internal/httpapi/v2/` 定义独立 DTO、严格 JSON 解码和官方错误结构。
- [x] 实现 Bearer 鉴权、请求 ID、Body 上限、Content-Type 和安全日志中间件。
- [x] 实现场景识别及 model/content/role/resolution/duration/ratio 校验。
- [x] 明确拒绝 callback、水印、`mm_file://`、Data URI 和未知字段。
- [x] 使用 `httptest` 覆盖三种场景、边界值、跨 Key 隔离和错误响应。

验证：`go test ./internal/httpapi/...`。

## 4. Gradio 客户端与参数映射

- [x] 在 `internal/upstream/gradio/` 实现带超时和响应上限的两阶段客户端。
- [x] 实现 SSE 解析、状态归一化、Gallery 提取与 URL 指纹。
- [x] 将 V2 请求和 profile 映射为固定 32 位参数，禁止猜测缺失配置。
- [x] 实现内部 host 到 `public_base_url` 的结构化 URL 替换。
- [x] 以假上游测试 complete/error、协议终止事件、响应限制和 URL 映射。

验证：`go test -race ./internal/upstream/gradio/...`。

## 5. 调度、Worker 与恢复

- [x] 在 `internal/scheduler/` 实现非阻塞唤醒、周期兜底和健康实例选择。
- [x] 在 `internal/worker/` 为每个上游运行一个串行 Worker。
- [x] 实现基线持久化、提交、轮询、唯一差集绑定和失败分类。
- [x] 实现提交前失败重排、未知提交结果 reconciling、启动恢复和优雅停机。
- [x] 测试单上游单活、双上游并行、FIFO、健康摘除、恢复和不重复提交。

验证：`go test -race ./internal/scheduler/... ./internal/worker/...`。

## 6. 四个官方接口

- [x] 实现 `POST /v2/video_generation`，事务入队并支持 `Idempotency-Key`。
- [x] 实现 `GET /v2/query/video_generation/{task_id}`。
- [x] 实现 `GET /v2/query/video_generation` 的分页与过滤。
- [x] 实现 `DELETE /v2/video_generation/{task_id}` 的保护区取消和逻辑删除。
- [x] 不注册 `/health`、Context-IR、regeneration 或文件接口。

验证：按 `TEST_ACCEPTANCE.md` 的 API-01 至 API-08 执行 HTTP 集成测试。

## 7. 清理、可观测性与安全

- [x] 在 `internal/cleaner/` 实现 7 天逻辑删除和幂等记录过期清理。
- [x] 使用 `log/slog` 输出 JSON；代理提交、SSE、状态变更、恢复和失败必须记录中文阶段说明。
- [x] 添加敏感字段脱敏测试，确保 Key、prompt、媒体 URL、内网地址不进入日志。
- [x] 为 HTTP、后台协程和 SQLite 实现有界优雅停机。

验证：`go test -race ./internal/cleaner/... ./internal/...`。

## 8. Docker 与交付

- [x] 创建多阶段 `Dockerfile`，以非 root 用户运行并挂载 `/data`。
- [x] 配置 `cmd/healthcheck` 仅 TCP 连接本地监听端口，不增加 HTTP 路由。
- [x] 更新根 `README.md`：配置、环境变量、Docker 启动、轮询示例和兼容差异。
- [x] 确认源码、配置和中文注释均为 UTF-8 无 BOM。

验证：

```powershell
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/server ./cmd/healthcheck
docker build -t minimax-h3-tc:v0.0.1 .
```

## 本地验证记录

- 已执行：`go test ./...`、`go vet ./...`、`go build ./cmd/server ./cmd/healthcheck`。
- 已执行：领取与取消并发竞争 100 轮。
- 未执行：`go test -race ./...`，当前 Windows 环境缺少 CGO 所需 C 编译器。
- 已尝试：Docker 镜像构建；当前 Docker Desktop Linux 引擎未运行，未能完成构建。

## 人工后续事项

按 `TEST_ACCEPTANCE.md` 使用真实私有服务确认 Gallery/SSE 样本、生成模式与尺寸、多个物理实例、公开下载地址和目标 Docker Volume。性能、生产发布和长期稳定性由目标环境单独确认。
