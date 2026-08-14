ALTER TABLE video_tasks ADD COLUMN deletion_state TEXT NOT NULL DEFAULT 'not_requested'
    CHECK (deletion_state IN ('not_requested','pending','partial','deleted','failed'));
ALTER TABLE video_tasks ADD COLUMN callback_url_ciphertext BLOB;
ALTER TABLE video_tasks ADD COLUMN callback_url_nonce BLOB;

CREATE TABLE task_artifacts (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES video_tasks(task_id),
    stage_id TEXT REFERENCES task_stages(id),
    kind TEXT NOT NULL CHECK (kind IN ('audio_source','intermediate_video','final_video','media_manifest','test_output')),
    size_bytes INTEGER NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    sha256 TEXT,
    media_json TEXT CHECK (media_json IS NULL OR json_valid(media_json)),
    state TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','delete_pending','deleting','deleted','delete_failed','missing')),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    deleted_at INTEGER
);

CREATE INDEX idx_artifacts_expiry ON task_artifacts(state,expires_at,task_id);
CREATE INDEX idx_artifacts_task ON task_artifacts(task_id,created_at,id);

CREATE TABLE artifact_locations (
    id TEXT PRIMARY KEY,
    artifact_id TEXT NOT NULL REFERENCES task_artifacts(id),
    node_id TEXT NOT NULL REFERENCES model_service_nodes(id),
    node_artifact_id TEXT NOT NULL,
    storage_key_fingerprint TEXT,
    state TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('importing','active','delete_pending','deleting','deleted','delete_failed','missing')),
    is_primary INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0,1)),
    size_bytes INTEGER NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    sha256 TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    verified_at INTEGER,
    deleted_at INTEGER,
    UNIQUE(node_id,node_artifact_id)
);

CREATE UNIQUE INDEX uq_artifact_locations_primary
    ON artifact_locations(artifact_id)
    WHERE is_primary=1 AND state='active';
CREATE INDEX idx_artifact_locations_delete
    ON artifact_locations(state,node_id,created_at);
CREATE INDEX idx_artifact_locations_artifact
    ON artifact_locations(artifact_id,created_at,id);

CREATE TABLE artifact_deletion_jobs (
    id TEXT PRIMARY KEY,
    reason TEXT NOT NULL CHECK (reason IN ('task_delete','retention','manual_cleanup','orphan_cleanup')),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('preview','pending','running','partial_failed','succeeded','failed')),
    scope TEXT NOT NULL DEFAULT 'managed_task_artifacts',
    older_than_days INTEGER CHECK (older_than_days IS NULL OR older_than_days BETWEEN 1 AND 3650),
    cutoff_at INTEGER,
    preview_token_hash TEXT,
    dry_run INTEGER NOT NULL DEFAULT 0 CHECK (dry_run IN (0,1)),
    requested_by TEXT NOT NULL,
    total_count INTEGER NOT NULL DEFAULT 0 CHECK (total_count >= 0),
    succeeded_count INTEGER NOT NULL DEFAULT 0 CHECK (succeeded_count >= 0),
    failed_count INTEGER NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    skipped_count INTEGER NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),
    candidate_bytes INTEGER NOT NULL DEFAULT 0 CHECK (candidate_bytes >= 0),
    deleted_bytes INTEGER NOT NULL DEFAULT 0 CHECK (deleted_bytes >= 0),
    created_at INTEGER NOT NULL,
    started_at INTEGER,
    finished_at INTEGER,
    updated_at INTEGER NOT NULL,
    error_summary TEXT
);

CREATE INDEX idx_deletion_jobs_status ON artifact_deletion_jobs(status,created_at,id);

CREATE TABLE artifact_deletion_items (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES artifact_deletion_jobs(id),
    artifact_id TEXT NOT NULL REFERENCES task_artifacts(id),
    location_id TEXT NOT NULL REFERENCES artifact_locations(id),
    node_id TEXT NOT NULL REFERENCES model_service_nodes(id),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','deleting','retry_wait','deleted','already_absent','skipped','failed')),
    operation_id TEXT NOT NULL UNIQUE,
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at INTEGER,
    lease_token TEXT,
    lease_expires_at INTEGER,
    last_error_code TEXT,
    last_error_message TEXT,
    deleted_bytes INTEGER NOT NULL DEFAULT 0 CHECK (deleted_bytes >= 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    deleted_at INTEGER,
    UNIQUE(job_id,location_id)
);

CREATE INDEX idx_deletion_items_claim
    ON artifact_deletion_items(status,next_attempt_at,lease_expires_at,node_id);

CREATE TABLE callback_deliveries (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES video_tasks(task_id),
    external_status TEXT NOT NULL,
    state_version INTEGER NOT NULL CHECK (state_version >= 1),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sending','retry_wait','succeeded','failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at INTEGER,
    request_body_hash TEXT NOT NULL,
    http_status INTEGER,
    last_error TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    delivered_at INTEGER,
    UNIQUE(task_id,state_version)
);

CREATE INDEX idx_callback_deliveries_claim
    ON callback_deliveries(status,next_attempt_at,created_at);
