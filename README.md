# MiniMax H3 V2 中转服务

将多个私有 MiniMax-H3 Gradio 服务包装为 4 个 MiniMax H3 V2 视频任务接口。服务使用 Go 单进程、SQLite 和本地 FIFO 队列，每个上游实例最多执行一个任务。

## 配置

复制 `config.example.yaml` 为部署配置，至少设置：

```powershell
$env:MINIMAX_API_KEY_CUSTOMER_A='replace-me'
```

模型服务节点在管理后台中维护并存储于 SQLite。`base_url` 指向 Gradio 服务，`jobs_base_url` 指向同一私有实例的 Jobs API；当前私有服务通常分别监听 `7860` 和 `8188`，两者均正常时实例才参与调度。`public_base_url` 必须是用户可直接访问的 HTTP/HTTPS 下载基址；跨公网部署建议使用 HTTPS。`resolution` 支持 `480P`、`768P`、`2K`，其中 `768P` 使用 `1344x768` 横屏基准。`generation_profiles` 中的模型、尺寸和步数必须在真实私有服务验证。所有文件使用 UTF-8；运行时代码不得读取 `docs/`。

管理后台位于 `http://服务地址/manager`。管理员可配置模型服务节点、中止排队或活动任务、删除已中止/成功/失败任务，并通过成功任务的播放按钮访问公开视频。`admin.username` 和 `admin.password` 配置登录凭据，`admin.session_ttl` 配置会话有效期，`admin.monitor_interval` 配置节点采集周期。HTTPS 在反向代理终止时必须设置 `admin.secure_cookie: true`；直接 HTTP 本地测试保持 `false`。示例中的 `admin`/`123` 仅供本地测试，服务会在使用该默认组合时输出安全警告；部署前必须替换，日志不会记录管理密码。

从旧版本升级时，如果节点表尚未初始化，服务会在首次启动时把 YAML 中原有的 `upstreams` 原子导入一次；此后 SQLite 是节点配置的唯一来源，修改或删除 YAML 中的 `upstreams` 不会改变已导入节点。新安装无需在 YAML 中配置 `upstreams`，服务可在零节点状态启动，再从管理后台新增节点。旧 `/monitor` 页面地址会永久跳转到 `/manager`，旧 `/monitor/api/*` 不再提供管理接口。

`task.execution_timeout` 默认 `10m`，用于结束长期离线或持续卡住的单次执行。私有服务恢复后若确认原模型任务和视频结果均不存在，系统会自动重试一次；超时本身不会触发重试。

示例配置的 SQLite 路径 `/data/minimax-h3-tc.db` 面向容器。本地运行时请将 `database.path` 改为可写路径，例如 `./data/minimax-h3-tc.db`。

## 本地运行

```powershell
go test ./...
go run ./cmd/server -config config.yaml
```

启动后访问 `http://127.0.0.1:8080/manager` 登录管理后台。管理会话与 V2 Bearer API Key 相互隔离。

创建任务：

```powershell
curl.exe -X POST http://127.0.0.1:8080/v2/video_generation `
  -H "Authorization: Bearer replace-me" -H "Content-Type: application/json" `
  -d '{"model":"MiniMax-H3","content":[{"type":"text","text":"海边日落"}],"resolution":"2K","duration":5,"ratio":"16:9"}'
```

使用返回的 `task_id` 请求 `GET /v2/query/video_generation/{task_id}`。成功后视频地址位于 `task.content.url`，服务不代理视频下载流量。

## Docker

```powershell
docker compose --env-file .env.docker build
docker compose --env-file .env.docker up -d
docker compose ps
```

Docker Desktop 中通过 `host.docker.internal` 访问宿主机的私有服务。部署后在 `/manager` 的“节点配置”中填写模型服务、任务服务和视频公开地址。SQLite 数据保存在宿主机 `data/` 目录，节点配置随数据库持久化。

容器使用 TCP 健康检查。队首保护区内的任务虽然查询状态为 `queued`，但不可取消。图片支持 HTTP/HTTPS URL 和 Base64 Data URI；音频支持 HTTP/HTTPS URL 和 WAV/MP3 Base64 Data URI；视频必须使用可访问的 HTTP/HTTPS URL。回调、水印和 `mm_file://` 暂不支持。
