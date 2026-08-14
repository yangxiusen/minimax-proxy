# MiniMax-H3-Proxy v0.0.1

MiniMax-H3-Proxy manages multiple MiniMax-H3 inference nodes, exposes the authenticated V2 task API, and distributes work across healthy compatible nodes. SQLite persists tasks, published request profiles, stages, attempts, artifacts, callbacks, and physical deletion jobs.

## Security and configuration

Start from `config.example.yaml`. Configure the administrator password and a separate 32-byte master key:

```powershell
$env:MINIMAX_ADMIN_PASSWORD='replace-with-a-long-random-password'
$env:MINIMAX_PROXY_MASTER_KEY='replace-with-64-hex-or-32-byte-base64'
go run ./cmd/server -config config.example.yaml
```

After signing in to `/manager`, open **密钥管理** to create public V2 Bearer keys. Full keys are stored as plaintext in SQLite and can be copied from the list at any time. Renaming, enabling, and disabling take effect immediately without restarting the Proxy; keys referenced by tasks can be disabled but not deleted. Existing YAML `api_keys` are imported into SQLite once on upgrade and remain valid. Protect the database file and its backups because they contain usable credentials.

`MINIMAX_PROXY_MASTER_KEY` encrypts Node API keys and callback targets and derives independent callback and artifact signing keys. Back it up in a secret manager; changing or losing it makes existing encrypted values unreadable. Use HTTPS and set `admin.secure_cookie: true` when TLS is terminated by a reverse proxy.

Successful tasks return 48-hour signed video URLs on the artifact's MiniMax-H3 node. The node `service_url` saved in Manager must therefore be an HTTP/HTTPS root reachable by both the Proxy and API clients. Video bytes flow directly from the node and do not pass through the Proxy; the signed URL can be reused and shared until it expires.

Open `http://127.0.0.1:8080/manager`. A new `h3-node-v1` node needs only:

- the single MiniMax-H3 service URL, normally `https://node:7860`;
- the write-only 32-character Node API Key from the node's `conf.yml`;
- request timeout, polling interval, and enabled state.

The Proxy never needs remote access to node port `8188`. The management session, public V2 API keys, Node keys, and MiniMax-H3 UI password are separate credentials.

## Request profiles and execution

The management console keeps one immediately active profile for each logical resolution. A profile contains all seven ratio mappings (`adaptive`, `21:9`, `16:9`, `4:3`, `1:1`, `3:4`, `9:16`) and is shared by text-to-video, image-to-video, and all-reference requests.

Profiles configure generation acceleration, up to four LoRAs, RIFE 2x, and SeedVR2/FlashVSR restoration. Saving replaces the active settings immediately; profiles can be deleted without checking running tasks. Task creation freezes the selected configuration and stages without retaining a Profile foreign key, so later edits or deletion do not alter queued or running tasks. Generation FPS is fixed at 24 and model filenames follow the selected high-quality or low-memory mode.

`480P`, `768P`, and `2K` are logical resolutions. The selected ratio maps to base generation dimensions and optional restoration dimensions; `2K` is not forwarded blindly to the model.

## Public API

```powershell
curl.exe -X POST http://127.0.0.1:8080/v2/video_generation `
  -H "Authorization: Bearer replace-with-client-key" `
  -H "Content-Type: application/json" `
  -d '{"model":"MiniMax-H3","content":[{"type":"text","text":"海边日落"}],"resolution":"2K","duration":5,"ratio":"16:9","aigc_watermark":true}'
```

`aigc_watermark` is optional and defaults to `false`. A watermark stage is added only when the request explicitly sends `true`.

Query with `GET /v2/query/video_generation/{task_id}`. Successful tasks return a reusable, 48-hour signed URL on the artifact's MiniMax-H3 node. The node serves the video directly with Range support, so video traffic does not pass through the Proxy. The URL reveals the configured node address but never its API key; treat the complete URL as a temporary bearer credential.

`callback_url` is optional. When present it is challenged before task creation, encrypted at rest, and notified with a stable HMAC-signed body and retry policy. When absent it causes no callback network request. Callback and remote input URLs reject local, private, link-local, metadata, reserved, and DNS-rebinding targets.

## Deletion and cleanup

Deleting a terminal task immediately hides it and transactionally creates a durable physical deletion intent. Node outages are retried. The management console also provides an age-based cleanup flow: preview candidates, enter the exact confirmation string, execute asynchronously, inspect per-node progress, and retry failures. Preview alone never mutates tasks or files.

Physical deletion cannot be undone. Back up required outputs and the SQLite database before confirming.

## Verification and Docker

```powershell
go test ./...
go vet ./...
```

For Docker, copy `.env.docker.example` to `.env.docker`, replace every example secret, then run:

```powershell
docker compose --env-file .env.docker build
docker compose --env-file .env.docker up -d
```

The SQLite database lives in `data/`. Migrations are forward-only and run atomically on startup; take a database backup before changing binaries. Legacy `legacy-gradio-v1` nodes remain readable for old tasks, while new v0.0.1 profiles require compatible `h3-node-v1` nodes.
