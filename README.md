# MiniMax-H3-Proxy v0.0.1

MiniMax-H3-Proxy manages multiple MiniMax-H3 inference nodes, exposes the authenticated V2 task API, and distributes work across healthy compatible nodes. SQLite persists tasks, published request profiles, stages, attempts, artifacts, callbacks, and physical deletion jobs.

## Multi-model protocols (v0.0.2)

The V2 endpoints now accept exact model IDs from configured nodes. Manager supports model discovery for `tk2sd-v1`, manual model declarations for `minimax-v2`, and the existing H3 model. A model is bound to one protocol; ambiguous names require an administrator choice. Tasks retain their selected protocol across retries and restarts, with no cross-protocol fallback.

For tk2sd, configure its service root and Bearer Token, discover the models, select the exposed models, and enable the node. Existing JSON media URLs and Base64 inputs are automatically uploaded to that node. H3 request profiles are not used, and `resolution` may be omitted. Output duration/resolution/ratio reflect actual metadata or `null`; they are not copied from ignored request fields. Unknown submissions recover on the same node with the same persisted request and idempotency key.

Deployments using only direct protocols may omit `generation_profiles`. Existing H3 profiles and nodes remain supported. SQLite migration 023 follows main's reusable-input migration 022, preserves historical configuration and flags legacy model aliases for compatibility. Databases already using the feature branch's routing migration 022 retain their routing identity when upgraded. Back up the database and master key before upgrading. Real upstream generation, media reachability and old-database acceptance still require environment-specific validation.

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

### tk2sd duration and task IDs

Send this JSON to the Proxy's `POST /v2/video_generation` with its public Bearer key, after enabling a node that exposes the model:

```json
{
  "model": "doubao-seedance-2-0-mini-260615",
  "duration": 10,
  "content": [
    {"type": "text", "text": "Show the product details"},
    {
      "type": "image_url",
      "image_url": {"url": "https://example.com/product.png"},
      "role": "reference_image"
    }
  ]
}
```

Replace the example image URL with a publicly reachable image. `duration` must be a top-level integer supported by the discovered model (4-15 seconds for these Seedance models). Omitting it defaults to 5 seconds; mentioning a length in the prompt does not set this field. Explicit invalid values are rejected. The Proxy forwards valid duration values unchanged to tk2sd's `/api/v3/contents/generations/tasks` endpoint.

Successful creation and idempotent replay return `{"task_id":"<proxy-task-id>"}`. This ID is assigned when the Proxy queues the task, before the asynchronous upstream submission. Use it for Proxy queries. The later tk2sd `{"id":"<upstream-task-id>"}` response is stored against that task; it does not replace the public ID. An empty local task ID cannot produce a successful create response. An upstream response without a valid `id` remains subject to the existing uncertain-submission recovery policy.

JSON logs expose the following diagnostic events:

| Filter | What to check |
| --- | --- |
| `stage=proxy_create`, `event=request` | Received model and duration, plus `duration_provided`; correlate by `request_id`. |
| `stage=proxy_create`, `event=normalized` | `effective_duration` and `duration_source=request` or `default`. An omitted tk2sd duration produces a warning. |
| `stage=proxy_create`, `event=response` | HTTP status and the public `task_id`; links the request ID to the task ID. |
| `stage=tk2sd_api`, `event=request` | Parameters actually sent, including `duration`; correlate by `task_id` and `node_id`. |
| `stage=tk2sd_api`, `event=response` | Upstream HTTP status and sanitized response structure for upload, create, query, metadata and cancel. |
| `stage=tk2sd_api`, `event=task_id_mapping` | The associated `task_id` and `upstream_task_id`. |

If generation keeps using 5 seconds, compare the received and forwarded values first. `duration_source=default` means the JSON arriving at the Proxy omitted `duration`; inspect the caller or New API forwarding configuration. Logs preserve IDs, model parameters, numeric metadata and error codes. Prompts, raw Base64, credentials, signed URLs and unknown response strings (including error messages that can echo inputs) are redacted; multipart bodies are omitted. Logging does not change the HTTP bodies sent or returned. See the log directory information below.

The manager task list shows the exact requested model and supports model-substring search alongside task/customer ID prefix search. It lists only unexpired, undeleted tasks created through this Proxy; tasks submitted directly to tk2sd remain in tk2sd's own database.

For a `tk2sd-v1` node, set **并发任务数** to the number of simultaneous jobs its available tk2sd accounts and quota can actually support. The Proxy limits in-flight submissions per node to this configured value (default `1`); raising it on an active node is supported without changing its connection. This does not create extra tk2sd accounts or increase their upstream quota. H3 routing and its single-task node behavior remain separate.

The tk2sd monitor also reads `GET /v1/dashboard` using the node's Bearer token. Its **上游排队** and task totals count all jobs in tk2sd, including jobs submitted outside the Proxy; **Proxy 待派发（tk2sd 全局）** counts only unassigned live tk2sd tasks in this Proxy, and **Proxy 运行任务** counts local node slots. These queue counts must not be added together as if they came from one queue. The manager shows upstream running/queued/succeeded/uncertain/failed/canceled counts, account occupancy, anonymized account state and credits, and login dispatch state. If the dashboard cannot be read, these upstream metrics show unavailable and are not reused from an older poll; node health remains based on the existing health probe. The manager does not relay raw account identifiers, leases, login messages or upstream errors, and cannot log in to tk2sd on an operator's behalf.

Each healthy tk2sd node tries the shared, model-compatible Proxy queue independently. A new task is sent only when the latest dashboard permits dispatch, the upstream queue is empty, and an account is available after subtracting external leases, cooling accounts and this Proxy's unsubmitted reservations. The node's configured concurrency is still an upper bound; set it to the intended maximum rather than leaving the default `1` when multiple accounts should run in parallel. A full node does not block another eligible tk2sd node from claiming the next task. If every compatible node is full or unavailable, the task remains in the Proxy queue; existing upstream jobs continue in tk2sd's own queue. When dashboard data is unavailable or stale, new dispatch pauses while active tasks can still be queried. Account snapshots are polled, so a simultaneous external submission between polls can briefly cause upstream queuing; this is not a distributed account reservation.

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

Container JSON logs are written to stdout and daily files under `/app/logs`, for example `/app/logs/server-2026-10-07.log`. The default compose file mounts `./logs` to `/app/logs`; mount that subdirectory rather than all of `/app` so the server binary and configuration remain available. `MINIMAX_LOG_DIR` overrides the in-container log directory.

Result delivery logs include the task ID, round, attempt, node video HTTP status, safe failure category, byte counts and whether an automatic retry is scheduled. Temporary node errors (409/408/429/5xx, transport failure, timeout and incomplete video) use the existing maximum of three attempts per round. Authentication and invalid media responses still require investigation or a manual retry. Filter `stage=result_delivery` and `stage=tk2sd_video_download` in the daily log to follow a download; video URLs and tokens are not logged.
