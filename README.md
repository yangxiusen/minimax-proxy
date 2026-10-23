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

Readable text logs expose the following diagnostic events:

```text
2026-10-23 10:19:09.362 +0800 WARN  远程任务素材准备失败 | task_id=task-1 source=object_storage http_status=403 retryable=false
2026-10-23 10:19:11.272 +0800 INFO  tk2sd 返回结果 | task_id=task-2 operation=query response.status=running
```

The timestamp includes milliseconds and the runtime's timezone offset; levels are aligned, followed by the message and `key=value` fields. Nested sanitized summaries use dotted names and array indexes, such as `response.status` and `request.content[0].type`. Messages and field values escape line breaks so each record stays on one line. Existing INFO filtering and diagnostic events remain in place.

| Filter | What to check |
| --- | --- |
| `stage=proxy_create`, `event=request` | Received model and duration, plus `duration_provided`; correlate by `request_id`. |
| `stage=proxy_create`, `event=normalized` | `effective_duration` and `duration_source=request` or `default`. An omitted tk2sd duration produces a warning. |
| `stage=proxy_create`, `event=response` | HTTP status and the public `task_id`; links the request ID to the task ID. |
| `stage=tk2sd_api`, `event=request` | Parameters actually sent, including `duration`; correlate by `task_id` and `node_id`. |
| `stage=tk2sd_api`, `event=response` | Upstream HTTP status and sanitized response structure for upload, create, query, metadata and cancel. |
| `stage=tk2sd_api`, `event=task_id_mapping` | The associated `task_id` and `upstream_task_id`. |
| `event=input_failed` | Input failure source, stage, reason, content index/type/role, `http_status`, `error_code`, `cause_class` and `retryable`; correlate by `task_id` and `node_id`. |
| `event=source_open_started` / `source_opened` | Input source access and media format detection. |
| `event=asset_upload_started` / `asset_uploaded` / `asset_reused` | Upstream asset upload, integrity validation and reuse without another upload. |

If generation keeps using 5 seconds, compare the received and forwarded values first. `duration_source=default` means the JSON arriving at the Proxy omitted `duration`; inspect the caller or New API forwarding configuration. Logs preserve IDs, model parameters, numeric metadata and error codes. Prompts, raw Base64, credentials, signed URLs and unknown response strings (including error messages that can echo inputs) are redacted; multipart bodies are omitted. Logging does not change the HTTP bodies sent or returned. See the log directory information below.

Input preparation failures now use specific `task.error.code/message` values. For example, `tk2sd_input_source_http_error` with `对象存储：素材下载失败（HTTP 403）；content[1] image_url` means the Proxy failed to download a persisted object-storage input before submitting generation. `content_index` is the zero-based index in the original `content` array. Filter `event=input_failed` and the task ID: `source=object_storage` identifies a persisted `ObjectURL`, `media_url` an external URL, `local_spool` a local input file, `data_uri` inline media, `proxy_store` the input/asset records, and `upstream` the tk2sd asset service. An arbitrary external URL is not assumed to belong to object storage.

Other input codes distinguish `tk2sd_input_request_invalid`, `tk2sd_input_source_invalid`, `tk2sd_input_local_file_invalid`, `tk2sd_input_media_invalid`, `tk2sd_input_integrity_mismatch` and `tk2sd_asset_upload_rejected`. Temporary source reads, uploads, malformed upstream asset responses and persistence failures are logged as `tk2sd_input_source_read_failed`, `tk2sd_asset_upload_unavailable`, `tk2sd_asset_response_invalid` or `tk2sd_input_persistence_failed` while the task keeps its existing pending/retry behavior; `retryable` records that decision, not a new retry policy. Source non-200 responses and upstream upload 400/413/422 responses keep their previous terminal behavior. Raw storage responses, URLs, file paths, prompts and binary data are not included in these diagnostics.

The manager task list shows the exact requested model and supports model-substring search alongside task/customer ID prefix search. It lists only unexpired, undeleted tasks created through this Proxy; tasks submitted directly to tk2sd remain in tk2sd's own database.

For a `tk2sd-v1` node, set **并发任务数** to this Proxy's intended in-flight limit for the node (default `1`, maximum `100`). The Proxy also observes tk2sd's remaining capacity; raising its own limit does not change tk2sd's global **每条线路并发数**, create accounts or increase model quotas. For example, five lines at three slots each offer 15 total slots; set the Proxy limit to 15 to use that capacity when available, or a lower value to reserve capacity for other callers. The per-line limit (1–20) remains configured in tk2sd. Raising the Proxy limit on an active node is supported without changing its connection. H3 routing and its single-task node behavior remain separate.

The tk2sd monitor also reads `GET /v1/dashboard` using the node's Bearer token. Its **上游排队** and task totals count all jobs in tk2sd, including jobs submitted outside the Proxy; **Proxy 待派发（tk2sd 全局）** counts only unassigned live tk2sd tasks in this Proxy, and **Proxy 运行任务** counts local node slots. These queue counts must not be added together as if they came from one queue. The manager shows upstream running/queued/succeeded/uncertain/failed/canceled counts, account occupancy, anonymized account state and credits, and login dispatch state. If the dashboard cannot be read, these upstream metrics show unavailable and are not reused from an older poll; node health remains based on the existing health probe. The manager does not relay raw account identifiers, leases, login messages or upstream errors, and cannot log in to tk2sd on an operator's behalf.

Each healthy tk2sd node tries the shared, model-compatible Proxy queue independently. A new task is sent only when the latest dashboard permits dispatch, the upstream queue is empty, and a slot is available after subtracting external occupancy, cooling lines' unused slots and this Proxy's unobserved reservations. New servers expose `slot_capacity`, `slot_occupied`, `concurrency_per_line` and each account's `active_count`, `concurrency_limit` and `available_slots`; the existing `capacity`/`occupied` still count lines. The manager displays both line and slot occupancy, per-line limits and remaining slots. Legacy dashboards without any slot fields use one slot per line.

The old account `lease` field identifies only one active task on a multi-slot line. When new dispatch is possible, the monitor therefore reads **all pages** of `GET /api/v3/contents/generations/tasks?filter.status=running&page_num=N&page_size=100` to identify this Proxy's own occupancy without double counting. This upstream status includes preparation, submission, generation and uncertain submissions. Partial, duplicate or changing pagination is rejected and retried on the next monitor pass; at most 100 pages are read within the probe's overall request timeout. A later task list showing more activity than the dashboard is counted conservatively. Full, queued or dispatch-disabled snapshots do not need this extra read. No raw task list, account identifiers or credentials are exposed in the manager or capacity logs.

The node's configured concurrency remains an upper bound. A full node or a slow/failed capacity probe does not block another eligible node; if every compatible node is unavailable, tasks wait in the Proxy queue. Failed or stale snapshots pause only new dispatch; active tasks continue to be queried. Decreasing tk2sd's limit may temporarily leave occupied slots above capacity; these snapshots remain valid and existing tasks are not interrupted. After slots free or probes recover, queued tasks resume automatically. Filter `stage=tk2sd_capacity` for capacity changes, dispatch constraints and safe probe failure reasons. Snapshots are polled, so a simultaneous external submission can still cause brief upstream queuing; this is not an atomic distributed reservation.

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

Container text logs are written to stdout and daily files under `/app/logs`, for example `/app/logs/server-2026-10-23.log`. If the directory or file cannot be opened, stdout keeps the same readable format. The default compose file mounts `./logs` to `/app/logs`; mount that subdirectory rather than all of `/app` so the server binary and configuration remain available. `MINIMAX_LOG_DIR` overrides the in-container log directory. Rebuild and restart the server to apply the new format; historical records are not rewritten, so an existing daily file may contain both earlier JSON records and newer text records. Log collectors must read text instead of decoding every line as JSON.

Result delivery logs include the task ID, round, attempt, node video HTTP status, safe failure category, byte counts and whether an automatic retry is scheduled. Temporary node errors (409/408/429/5xx, transport failure, timeout and incomplete video) use the existing maximum of three attempts per round. Authentication and invalid media responses still require investigation or a manual retry. Filter `stage=result_delivery` and `stage=tk2sd_video_download` in the daily log to follow a download; video URLs and tokens are not logged.

Generation and result delivery are separate: an upstream task may already be `succeeded` while the Proxy is waiting to download, upload or verify the video. The manager distinguishes **等待上传**, **上传结果**, **等待上传重试** and **上传失败**; a nonterminal upload's **上次尝试失败** is the previous attempt's error. Pending delivery does not occupy the upstream generation slot.

Expired result-upload jobs that already used all three attempts are finalized as failed before claiming the next job. An active third-attempt lease is left intact; a failed job can start a new round through **重新上传**. This also recovers legacy `uploading` jobs that otherwise caused `CHECK constraint failed: attempt_no BETWEEN 0 AND 3` and blocked the queue. Back up the database before deploying recovery changes. [The read-only diagnostic SQL](scripts/result-delivery-diagnostics.sql) shows persisted task/delivery status and expired final attempts without exposing media URLs or credentials.

Object-storage failures additionally include `storage_operation`, `http_status`, `failure_reason` and an available safe `storage_request_id`. Operations distinguish multipart initialization, part upload, completion, authenticated object verification and public-read verification. Cleanup responses and intermediate redirects do not replace the actual failure status. The message and logs retain safe diagnostics; SDK causes remain available for error classification without printing private addresses or credentials. Old generic `UCloud 上传失败` messages cannot reveal their lost original HTTP status retroactively.
