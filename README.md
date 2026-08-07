# MiniMax H3 V2 中转服务

将多个私有 MiniMax-H3 Gradio 服务包装为 4 个 MiniMax H3 V2 视频任务接口。服务使用 Go 单进程、SQLite 和本地 FIFO 队列，每个上游实例最多执行一个任务。

## 配置

复制 `config.example.yaml` 为部署配置，至少设置：

```powershell
$env:MINIMAX_API_KEY_CUSTOMER_A='replace-me'
$env:MINIMAX_UPSTREAM_GPU_1_URL='http://192.168.1.200:7861'
```

`public_base_url` 必须是用户可直接访问的 HTTP/HTTPS 下载基址；跨公网部署建议使用 HTTPS。`generation_profiles` 中的模型、尺寸和步数必须在真实私有服务验证。所有文件使用 UTF-8；运行时代码不得读取 `docs/`。

只读监控控制台位于 `http://服务地址/monitor`。`admin.username` 和 `admin.password` 配置登录凭据，`admin.session_ttl` 配置会话有效期，`admin.monitor_interval` 配置节点采集周期。HTTPS 在反向代理终止时必须设置 `admin.secure_cookie: true`；直接 HTTP 本地测试保持 `false`。示例中的 `admin`/`123` 仅供本地测试，服务会在使用该默认组合时输出安全警告；部署前必须替换，日志不会记录管理密码。

示例配置的 SQLite 路径 `/data/minimax-h3-tc.db` 面向容器。本地运行时请将 `database.path` 改为可写路径，例如 `./data/minimax-h3-tc.db`。

## 本地运行

```powershell
go test ./...
go run ./cmd/server -config config.yaml
```

启动后访问 `http://127.0.0.1:8080/monitor` 登录监控控制台。监控会话与 V2 Bearer API Key 相互隔离。

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

Docker Desktop 中通过 `host.docker.internal` 访问宿主机的私有服务。部署到不同服务器时，修改 `.env.docker` 中的 `MINIMAX_UPSTREAM_URL` 和 `MINIMAX_PUBLIC_UPSTREAM_URL`。SQLite 数据保存在宿主机 `data/` 目录。

容器使用 TCP 健康检查，不提供额外 HTTP 健康接口。队首保护区内的任务虽然查询状态为 `queued`，但不可取消。回调、水印、`mm_file://` 和 Data URI 首版不支持。
