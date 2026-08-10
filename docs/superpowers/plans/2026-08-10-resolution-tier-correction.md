# Resolution Tier Correction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将错误命名的旧 `768P` 档修正为 `480P`，新增真正的 `768P` 档，并让旧配置和旧 SQLite 数据库无停机语义迁移到 `480P/768P/2K`。

**Architecture:** 配置层识别完整匹配的旧 480 尺寸并在内存中拆分为 `480P` 与新 `768P`；API 层仅依赖 profile key 做允许值校验。SQLite 增加事务型 v2 迁移，扩展 CHECK 约束并将全部历史旧 `768P` 任务及其请求 JSON 重标为 `480P`。

**Tech Stack:** Go 1.26、`gopkg.in/yaml.v3`、`database/sql`、`modernc.org/sqlite`、嵌入式 SQL、Docker Compose。

---

### Task 1: 配置档位与旧配置兼容

**Files:**
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/config.go`
- Modify: `config.example.yaml`
- Modify: `config.docker.yaml`

- [ ] **Step 1: 写配置迁移失败测试**

扩展 `TestLoadMigratesLegacyGenerationDimensions`：输入只含旧 `768P` 和 `2K`，断言加载成功后同时存在旧尺寸的 `480P`、新尺寸 `1344x768` 的 `768P` 与 `2K`。新增测试断言显式配置缺少 `480P` 时返回 `generation_profiles 缺少 480P`。

- [ ] **Step 2: 运行测试并确认红灯**

Run: `go test ./internal/config -run 'TestLoad(MigratesLegacyGenerationDimensions|RejectsIncompleteGenerationProfile)$' -count=1`

Expected: FAIL，旧配置加载结果没有 `480P` 或校验未要求 `480P`。

- [ ] **Step 3: 实现配置迁移与校验**

在 `config.go` 增加 `requiredResolutions = []string{"480P", "768P", "2K"}` 和返回全新 map 的 `dimensions768P()`。仅当不存在 `480P` 且旧 `768P` 的七个尺寸完整匹配旧映射时执行：

```go
profiles["480P"] = legacyProfile
legacyProfile.Dimensions = dimensions768P()
profiles["768P"] = legacyProfile
```

保留现有 `1104 -> 1120`、`1080 -> 1088` 兼容后再识别旧 profile。校验循环改用 `requiredResolutions`。

- [ ] **Step 4: 更新显式配置并验证绿灯**

在两个 YAML 中将旧 block 命名为 `480P`，新增 `768P` block，尺寸依次为 `1344x768`、`1792x768`、`1344x768`、`1024x768`、`768x768`、`768x1024`、`768x1344`。

Run: `gofmt -w internal/config/config.go internal/config/config_test.go; go test ./internal/config -count=1`

Expected: PASS。

### Task 2: API 接受新档位语义

**Files:**
- Modify: `internal/httpapi/v2/validation_test.go`
- Modify: `internal/httpapi/v2/validation.go`
- Modify: `internal/httpapi/v2/handler_test.go`

- [ ] **Step 1: 写请求校验失败测试**

将测试 profile helper 扩展到三个档位，增加表驱动用例验证 `480P`、`768P`、`2K` 均成功映射，`1K` 返回包含 `480P、768P 或 2K` 的错误。

- [ ] **Step 2: 运行测试并确认红灯**

Run: `go test ./internal/httpapi/v2 -run 'TestValidateCreate.*Resolution' -count=1`

Expected: FAIL，`480P` 被现有硬编码拒绝。

- [ ] **Step 3: 修改允许值并验证**

将 `ValidateCreate` 中的条件改为：

```go
switch request.Resolution {
case "480P", "768P", "2K":
default:
	return ValidatedRequest{}, fmt.Errorf("resolution 仅支持 480P、768P 或 2K")
}
```

同步修正受影响的 handler fixture。

Run: `gofmt -w internal/httpapi/v2/validation.go internal/httpapi/v2/validation_test.go internal/httpapi/v2/handler_test.go; go test ./internal/httpapi/v2 -count=1`

Expected: PASS。

### Task 3: SQLite v2 数据迁移

**Files:**
- Create: `migrations/002_resolution_tiers.sql`
- Modify: `migrations/001_init.sql`
- Modify: `migrations/embed.go`
- Modify: `internal/store/sqlite/store.go`
- Modify: `internal/store/sqlite/store_test.go`

- [ ] **Step 1: 写旧数据库迁移失败测试**

测试用 `strings.Replace(migrations.Initial, "('480P','768P','2K')", "('768P','2K')", 1)` 建立 v1 数据库，插入 `resolution='768P'` 且 `request_json.resolution='768P'` 的任务，再调用 `Open`。断言版本 2 存在、历史两处值均为 `480P`，并可新建 `480P` 任务。

- [ ] **Step 2: 运行测试并确认红灯**

Run: `go test ./internal/store/sqlite -run TestOpenMigratesLegacyResolutionTier -count=1`

Expected: FAIL，v1 CHECK 拒绝写入 `480P` 或 migration version 2 不存在。

- [ ] **Step 3: 增加嵌入式 v2 迁移**

更新初始结构允许 `('480P','768P','2K')`。`002_resolution_tiers.sql` 在事务内使用 `video_tasks_v2` 新表复制所有列，复制表达式使用：

```sql
CASE WHEN resolution = '768P' THEN '480P' ELSE resolution END,
CASE WHEN resolution = '768P'
     THEN json_set(request_json, '$.resolution', '480P')
     ELSE request_json END
```

随后删除旧表、重命名并重建四个任务索引。`embed.go` 暴露 `ResolutionTiers`。

- [ ] **Step 4: 让迁移器按版本幂等执行**

`migrate` 先执行 Initial 并记录版本 1，再查询版本 2；缺失时执行 `migrations.ResolutionTiers` 并记录版本 2。全部操作保留在当前事务中，重复启动不再重建表。

- [ ] **Step 5: 验证迁移与存储回归**

Run: `gofmt -w migrations/embed.go internal/store/sqlite/store.go internal/store/sqlite/store_test.go; go test ./internal/store/sqlite -count=1`

Expected: PASS。

### Task 4: 文档、全量验证与交付

**Files:**
- Modify: `README.md`
- Modify: `specs/product/requirements/version_0.0.1.md`
- Modify: `specs/developing/version_0.0.1/api-modules/video-generation.md`
- Modify: `specs/developing/version_0.0.1/DATABASE_DESIGN.md`

- [ ] **Step 1: 同步公开允许值和示例**

把所有面向调用方的 `768P/2K` 允许值更新为 `480P/768P/2K`，README 示例保留 `2K`。数据库设计记录 version 2 CHECK 与历史 `768P -> 480P` 迁移语义。

- [ ] **Step 2: 全仓搜索残留硬编码**

Run: `rg -n --glob '!docs/**' "仅支持 768P|IN \('768P','2K'\)|\[\]string\{\"768P\", \"2K\"\}" .`

Expected: 无运行时代码或初始 schema 残留。

- [ ] **Step 3: 执行完整质量门禁**

Run: `go test ./...`

Run: `go vet ./...`

Run: `go build ./cmd/server ./cmd/healthcheck`

Expected: 全部 exit 0。

- [ ] **Step 4: Docker 构建与健康检查**

Run: `docker compose build && docker compose up -d`

Run: `docker inspect minimax-h3-tc --format '{{.State.Health.Status}}'`

Expected: 镜像构建成功，最终状态 `healthy`，启动日志无配置或数据库迁移错误。

- [ ] **Step 5: 提交并推送 main**

```bash
git add config.example.yaml config.docker.yaml internal migrations README.md specs docs/superpowers
git commit -m "feat: correct video resolution tiers"
git push origin main
```

Expected: `main...origin/main` 无 ahead/behind，工作区干净。
