# 验证与验收

## 自动化

- 线路上限为 2、5，默认值 2，总容量 7：读取成功，保留各自剩余名额，读取全部活动任务 ID。
- 总容量错误、单线路上限 0 或 21、剩余名额错误：拒绝响应。
- 旧单名额、统一多名额、下调上限后的超额占用、冷却、分页完整性：既有测试覆盖。
- 修复前运行独立上限回归测试，确认失败：independent account limits rejected: tk2sd: invalid response。

修复后验证（2026-10-30）：`go test ./...` 全部通过；`go vet ./...`、`go build ./cmd/server ./cmd/healthcheck`、`node --check internal/httpapi/manager/web/manager.js` 和 `git diff --check` 通过。适配器、运行时、远程处理器、SQLite 和管理 API 定向测试通过。

自查：逐线路限制和剩余名额仍严格校验；不要求活动占用小于新上限，保留下调并发时的恢复能力；单名额旧响应容量合计不变；活动任务分页及自有占用去重逻辑不变。无真实服务写入或任务重提。

## 人工确认（待完成）

部署更新后的 Proxy，确认上游统计及各线路实际容量正常、既有排队任务继续领取。如果仍失败，收集 tk2sd_capacity_unavailable 及 dashboard/活动任务分页的脱敏现场响应。未执行真实上游联调或生产验收。
