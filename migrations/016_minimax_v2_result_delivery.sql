ALTER TABLE model_service_nodes ADD COLUMN upstream_model TEXT NOT NULL DEFAULT '';
ALTER TABLE model_service_nodes ADD COLUMN max_concurrency INTEGER NOT NULL DEFAULT 1 CHECK (max_concurrency BETWEEN 1 AND 100);
ALTER TABLE model_service_nodes ADD COLUMN replace_result_url INTEGER NOT NULL DEFAULT 0 CHECK (replace_result_url IN (0,1));

ALTER TABLE video_tasks ADD COLUMN upstream_slot_active INTEGER NOT NULL DEFAULT 0 CHECK (upstream_slot_active IN (0,1));
ALTER TABLE video_tasks ADD COLUMN upstream_node_version INTEGER NOT NULL DEFAULT 0 CHECK (upstream_node_version >= 0);
ALTER TABLE video_tasks ADD COLUMN delivery_required INTEGER NOT NULL DEFAULT 0 CHECK (delivery_required IN (0,1));

DROP INDEX IF EXISTS uq_video_tasks_active_upstream;
CREATE INDEX idx_video_tasks_upstream_capacity
    ON video_tasks(upstream_id,upstream_slot_active,status,queue_seq)
    WHERE deleted_at IS NULL;

CREATE TRIGGER protect_video_tasks_original_result_url
BEFORE UPDATE OF result_internal_url ON video_tasks
WHEN OLD.result_internal_url IS NOT NULL
 AND (NEW.result_internal_url IS NULL OR NEW.result_internal_url <> OLD.result_internal_url)
BEGIN
    SELECT RAISE(ABORT, 'result_internal_url is immutable');
END;

CREATE TABLE object_storage_configs (
    id INTEGER PRIMARY KEY CHECK (id=1),
    provider TEXT NOT NULL CHECK (provider='ucloud-us3'),
    bucket_name TEXT NOT NULL CHECK (length(bucket_name) BETWEEN 1 AND 63),
    file_host TEXT NOT NULL CHECK (length(file_host) > 0),
    public_base_url TEXT NOT NULL CHECK (length(public_base_url) > 0),
    public_key_ciphertext BLOB NOT NULL,
    public_key_nonce BLOB NOT NULL,
    public_key_fingerprint TEXT NOT NULL,
    private_key_ciphertext BLOB NOT NULL,
    private_key_nonce BLOB NOT NULL,
    private_key_fingerprint TEXT NOT NULL,
    request_timeout_ms INTEGER NOT NULL CHECK (request_timeout_ms BETWEEN 1000 AND 1800000),
    last_test_status TEXT NOT NULL DEFAULT 'untested' CHECK (last_test_status IN ('untested','passed','failed')),
    last_tested_at INTEGER,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE result_upload_jobs (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    task_id TEXT NOT NULL UNIQUE REFERENCES video_tasks(task_id),
    object_key TEXT NOT NULL UNIQUE CHECK (length(object_key) > 0),
    status TEXT NOT NULL CHECK (status IN ('pending','uploading','retry_wait','succeeded','failed')),
    round_no INTEGER NOT NULL DEFAULT 1 CHECK (round_no >= 1),
    attempt_no INTEGER NOT NULL DEFAULT 0 CHECK (attempt_no BETWEEN 0 AND 3),
    max_attempts INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts=3),
    next_attempt_at INTEGER,
    lease_token TEXT,
    lease_expires_at INTEGER,
    public_url TEXT NOT NULL DEFAULT '',
    last_error_code TEXT NOT NULL DEFAULT '',
    last_error_message TEXT NOT NULL DEFAULT '',
    bytes_uploaded INTEGER NOT NULL DEFAULT 0 CHECK (bytes_uploaded >= 0),
    started_at INTEGER,
    finished_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1)
);

CREATE INDEX idx_result_upload_jobs_claim
    ON result_upload_jobs(status,next_attempt_at,lease_expires_at);
CREATE INDEX idx_result_upload_jobs_task_status
    ON result_upload_jobs(task_id,status);

PRAGMA user_version = 16;
