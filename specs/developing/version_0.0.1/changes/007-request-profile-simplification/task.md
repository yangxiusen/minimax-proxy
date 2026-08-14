# 模型请求配置简化实施任务

> 按顺序实施；每项先写失败测试，再做最小实现。人工联调仅记录在 `TEST_ACCEPTANCE.md`。

## PS-001 v11 数据迁移

- [x] 在 `internal/store/sqlite` 增加 v11、多配置去重、任务引用和外键完整性失败测试。
- [x] 新增 `migrations/011_request_profile_simplification.sql`，在 `migrations/embed.go` 和迁移计划注册。
- [x] 实现每 resolution 保留 `updated_at DESC,id DESC` 第一条、删除测试数据并建立唯一索引。
- [x] 运行 `go test ./internal/store/sqlite -run 'Migration|Profile' -count=1`。

依赖：v9/v10 迁移实现或在当前分支协调最终编号。

## PS-002 领域与 Store 简化

- [x] 修改 `internal/domain/profile.go`，删除版本状态、测试发布和无效参数类型。
- [x] 重写 `internal/store/sqlite/profile_store.go` 测试，覆盖 resolution 唯一 CRUD、乐观锁和无状态删除。
- [x] 实现新 Store；删除 Profile Test 存储路径。
- [x] 运行 `go test ./internal/store/sqlite ./internal/domain`。

依赖：PS-001。

## PS-003 Profile Service 与匹配简化

- [x] 重写 `internal/profile/service_test.go`，覆盖即时 CRUD、完整七比例校验和删除。
- [x] 简化 `service.go`；删除 clone/test/publish、测试 Worker/执行器和 watermark capability 门禁。
- [x] 将任务配置查询改为 `GetByResolution`，移除 scenario/model Profile 键。
- [x] 运行 `go test ./internal/profile`。

依赖：PS-002。

## PS-004 Manager API 与页面

- [x] 重写 `internal/httpapi/manager/profiles_test.go`，覆盖新 CRUD、DELETE 204、严格字段和并发冲突。
- [x] 更新 `profiles.go` 路由，删除 tests/publish/clone/compatible-nodes。
- [x] 更新 `manager.html/manager.js/styles.css`，移除无效控件和版本工作流，增加即时删除和状态反馈。
- [x] 增加嵌入 Web 资源契约测试，断言旧控件/端点不再引用。
- [x] 运行 `go test ./internal/httpapi/manager`。

依赖：PS-003。

## PS-005 V2 匹配、固定参数和水印

- [x] 在 `validation_test.go/handler_test.go` 增加固定 24/follow model 和水印 true/false/省略测试。
- [x] 修改 `validation.go`，水印默认 false且显式 false 合法。
- [x] 修改 `handler.go`，按 resolution 查询，固定 generation 参数，条件追加 watermark stage。
- [x] 运行 `go test ./internal/httpapi/v2`。

依赖：PS-003。

## PS-006 清理装配与文档

- [x] 从 `cmd/server/main.go` 删除 Profile Test Worker 装配和相关后台协程。
- [x] 删除不再使用的测试/发布代码文件，修复接口 mock 和启动测试。
- [x] 同步 README、公开 API 文档和项目规格中的水印与即时配置规则。
- [x] 执行 `gofmt`、`go test ./...`、`go vet ./...`、`go build ./cmd/server ./cmd/healthcheck`。
- [x] 代码审查重点检查迁移数据损失边界、删除事务、任务快照权威和页面旧端点残留。

依赖：PS-004、PS-005。
