DROP TABLE IF EXISTS video_tasks_v3;

CREATE TABLE video_tasks_v3 (
    queue_seq INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id TEXT NOT NULL UNIQUE,
    api_key_id TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT 'MiniMax-H3',
    task_type TEXT NOT NULL DEFAULT 'generation',
    modality TEXT NOT NULL DEFAULT 'video',
    scenario TEXT NOT NULL CHECK (scenario IN ('t2va','i2va','r2va')),
    request_json TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued_open' CHECK (status IN ('queued_open','queued_locked','dispatching','running','reconciling','cancelling','succeeded','failed','cancelled')),
    cancel_locked INTEGER NOT NULL DEFAULT 0 CHECK (cancel_locked IN (0,1)),
    upstream_id TEXT,
    gradio_event_id TEXT,
    upstream_job_id TEXT,
    upstream_jobs_before_json TEXT NOT NULL DEFAULT '[]',
    retry_count INTEGER NOT NULL DEFAULT 0 CHECK (retry_count BETWEEN 0 AND 1),
    attempt_started_at INTEGER,
    cancel_requested_at INTEGER,
    gallery_before_json TEXT,
    result_internal_url TEXT,
    result_public_url TEXT,
    resolution TEXT NOT NULL CHECK (resolution IN ('480P','768P','2K')),
    duration INTEGER NOT NULL CHECK (duration BETWEEN 4 AND 15),
    ratio_requested TEXT NOT NULL DEFAULT 'adaptive',
    ratio_actual TEXT,
    usage_total_seconds INTEGER NOT NULL DEFAULT 0,
    usage_input_seconds INTEGER NOT NULL DEFAULT 0,
    usage_output_seconds INTEGER NOT NULL DEFAULT 0,
    usage_input_image_count INTEGER NOT NULL DEFAULT 0,
    error_code TEXT,
    error_message TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    started_at INTEGER,
    finished_at INTEGER,
    expires_at INTEGER NOT NULL,
    deleted_at INTEGER,
    version INTEGER NOT NULL DEFAULT 0
);

INSERT INTO video_tasks_v3 (
    queue_seq, task_id, api_key_id, model, task_type, modality, scenario,
    request_json, request_hash, status, cancel_locked, upstream_id,
    gradio_event_id, gallery_before_json, result_internal_url,
    result_public_url, resolution, duration, ratio_requested, ratio_actual,
    usage_total_seconds, usage_input_seconds, usage_output_seconds,
    usage_input_image_count, error_code, error_message, created_at, updated_at,
    started_at, finished_at, expires_at, deleted_at, version
)
SELECT
    queue_seq, task_id, api_key_id, model, task_type, modality, scenario,
    request_json, request_hash, status, cancel_locked, upstream_id,
    gradio_event_id, gallery_before_json, result_internal_url,
    result_public_url, resolution, duration, ratio_requested, ratio_actual,
    usage_total_seconds, usage_input_seconds, usage_output_seconds,
    usage_input_image_count, error_code, error_message, created_at, updated_at,
    started_at, finished_at, expires_at, deleted_at, version
FROM video_tasks;

DROP TABLE video_tasks;
ALTER TABLE video_tasks_v3 RENAME TO video_tasks;

CREATE INDEX idx_video_tasks_owner_time ON video_tasks(api_key_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_video_tasks_owner_status ON video_tasks(api_key_id, status, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_video_tasks_queue ON video_tasks(queue_seq) WHERE status IN ('queued_open','queued_locked');
CREATE INDEX idx_video_tasks_expiry ON video_tasks(expires_at) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX uq_video_tasks_active_upstream ON video_tasks(upstream_id) WHERE status IN ('dispatching','running','reconciling','cancelling');
