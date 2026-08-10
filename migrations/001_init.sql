CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS video_tasks (
    queue_seq INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id TEXT NOT NULL UNIQUE,
    api_key_id TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT 'MiniMax-H3',
    task_type TEXT NOT NULL DEFAULT 'generation',
    modality TEXT NOT NULL DEFAULT 'video',
    scenario TEXT NOT NULL CHECK (scenario IN ('t2va','i2va','r2va')),
    request_json TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued_open' CHECK (status IN ('queued_open','queued_locked','dispatching','running','reconciling','succeeded','failed','cancelled')),
    cancel_locked INTEGER NOT NULL DEFAULT 0 CHECK (cancel_locked IN (0,1)),
    upstream_id TEXT,
    gradio_event_id TEXT,
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

CREATE INDEX IF NOT EXISTS idx_video_tasks_owner_time ON video_tasks(api_key_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_video_tasks_owner_status ON video_tasks(api_key_id, status, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_video_tasks_queue ON video_tasks(queue_seq) WHERE status IN ('queued_open','queued_locked');
CREATE INDEX IF NOT EXISTS idx_video_tasks_expiry ON video_tasks(expires_at) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_video_tasks_active_upstream ON video_tasks(upstream_id) WHERE status IN ('dispatching','running','reconciling');

CREATE TABLE IF NOT EXISTS idempotency_keys (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    api_key_id TEXT NOT NULL,
    key_hash TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    task_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    UNIQUE(api_key_id, key_hash)
);

CREATE INDEX IF NOT EXISTS idx_idempotency_task ON idempotency_keys(task_id);
CREATE INDEX IF NOT EXISTS idx_idempotency_expiry ON idempotency_keys(expires_at);
