# MiniMax H3 V2 中转服务测试与验收

| 项目 | 内容 |
| --- | --- |
| 版本 | `v0.0.1` |
| 接口范围 | 4 个官方 MiniMax H3 V2 接口 |
| 自动化状态 | 本地自动化验证已完成；竞态检测与 Docker 构建受环境限制 |
| 人工联调 | 需要真实私有 MiniMax-H3 实例 |

## 1. 自动化测试基线

交付前使用以下命令验证：

```powershell
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/server ./cmd/healthcheck
docker build -t minimax-h3-tc:v0.0.1 .
```

单元测试使用 `testing`，HTTP 使用 `httptest`，仓储使用临时 SQLite 文件，上游使用可编程假 Gradio 服务。测试不得读取 `docs/`。

## 2. 官方接口验收

| 编号 | 场景 | 预期 |
| --- | --- | --- |
| API-01 | POST 创建合法 t2va/i2va/r2va | 返回 200 和唯一 `task_id`，不等待上游 |
| API-02 | model、content、角色、数量、时长、比例非法 | 返回官方风格 400，不创建任务 |
| API-03 | GET 单任务 | 只返回当前 Key 的 7 天内任务，成功态含 `task.content.url` |
| API-04 | GET 列表分页及五种状态过滤 | `items/total` 正确，未知参数返回 400 |
| API-05 | DELETE 普通 queued | 原子取消并返回 `action=cancelled` |
| API-06 | DELETE succeeded/failed | 逻辑删除并返回 `action=deleted` |
| API-07 | DELETE running/cancelled | 返回不可操作错误，状态不变 |
| API-08 | 无效 Bearer Key 或跨 Key 查询 | 返回鉴权/无效任务错误，不泄露归属 |

兼容性差异必须有固定回归用例：队首保护区内的 `queued` 不可取消；图片与 WAV/MP3 音频 Data URI 被接受，视频 Data URI、裸 Base64、`callback_url`、`aigc_watermark=true` 和 `mm_file://` 被明确拒绝。

## 3. 队列与并发验收

| 编号 | 场景 | 预期 |
| --- | --- | --- |
| QUEUE-01 | 连续创建 6 个任务，保护位为 3 | FIFO 前 3 个为 locked，其余为 open |
| QUEUE-02 | 取消 open 任务 | 取消成功且保护位重新计算 |
| QUEUE-03 | Worker 领取与取消并发 | 只有一方成功，不重复执行 |
| QUEUE-04 | 每 Key 10、全局 100 未结束上限 | 超限创建返回 429，计数无越界 |
| QUEUE-05 | 同一上游并发领取 | 最多 1 个活跃任务 |
| QUEUE-06 | 两个健康上游 | 可各执行 1 个任务，整体仍按 FIFO 领取 |
| IDEM-01 | 相同 Key、幂等键和请求重放 | 返回同一 task_id |
| IDEM-02 | 相同幂等键、不同请求 | 返回 409 |

以上并发测试至少循环 100 次，并在 `-race` 下执行。

当前领取与取消并发测试已循环 100 次通过。Windows 环境未安装 CGO 所需的 C 编译器，`go test -race ./...` 留待具备竞态检测条件的环境执行。

## 4. 上游适配与恢复验收

- 验证 32 位 Gradio 参数的顺序、空值和三种生成场景映射。
- 验证两阶段调用、SSE complete/error、响应上限和超时分类。
- Gallery 差集仅有一个新视频时成功；无结果或多结果不得猜测。
- 内部 URL 只替换已配置 host，保留 path、转义和 query。
- 提交前连接失败可重新排队；提交状态不明时进入 `reconciling`，不得重提。
- 进程重启后恢复原上游轮询；SQLite 保持状态、队序和 Gallery 基线。
- 上游健康失败只暂停对应 Worker，恢复后自动参与调度。

## 5. 生命周期与部署验收

- 超过 7 天的任务被逻辑删除，列表和单查不可见，不请求上游物理删除。
- 已返回的直接下载地址不承诺撤销或刷新，服务本身不代理视频流量。
- 配置支持 YAML 与 `${ENV}`；私有地址、API Key 缺失或重复时拒绝启动。
- Docker 使用非 HTTP 的 TCP `healthcheck` 程序，挂载 `/data` 后重启数据仍在。
- 日志为 UTF-8 JSON；关键代理阶段使用中文说明，不记录 Key、prompt、媒体 URL 或内网地址。

## 6. 人工确认清单

- [ ] 在真实私有服务验证 t2va、首帧/首尾帧 i2va、图/视频/音频 r2va。
- [ ] 验证全部允许的 `resolution + ratio` 配置与实际输出。
- [ ] 验证 Gallery 与 SSE 的真实响应结构和状态文本。
- [ ] 验证多个物理实例并行及 `public_base_url` 从用户网络可达。
- [ ] 在目标 Docker 环境完成长任务、异常重启和 SQLite Volume 恢复测试。

## 7. 本地验证记录

- `go test -count=1 ./...`：通过。
- `go vet ./...`：通过。
- `go build ./cmd/server ./cmd/healthcheck`：通过。
- UTF-8 BOM 与运行时代码 `docs/` 引用扫描：通过。
- `go test -race ./...`：未执行，当前 Windows Go 环境缺少 CGO C 编译器。
- `docker build -t minimax-h3-tc:v0.0.1 .`：已尝试；Docker Desktop Linux 引擎未运行，未能完成镜像构建。
