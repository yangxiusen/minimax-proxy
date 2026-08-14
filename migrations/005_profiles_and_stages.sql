CREATE TABLE model_request_profiles (
    id TEXT PRIMARY KEY,
    model TEXT NOT NULL,
    resolution TEXT NOT NULL CHECK (resolution IN ('480P','768P','2K')),
    scenario TEXT NOT NULL CHECK (scenario IN ('t2v','i2v','r2v')),
    profile_version INTEGER NOT NULL CHECK (profile_version >= 1),
    status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','testing','test_failed','ready_to_publish','active','expired')),
    config_json TEXT NOT NULL CHECK (json_valid(config_json)),
    config_hash TEXT NOT NULL CHECK (length(config_hash) > 0),
    source_profile_id TEXT REFERENCES model_request_profiles(id),
    created_by TEXT NOT NULL,
    published_by TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    published_at INTEGER,
    expired_at INTEGER,
    row_version INTEGER NOT NULL DEFAULT 1 CHECK (row_version >= 1),
    UNIQUE(model,resolution,scenario,profile_version)
);

CREATE UNIQUE INDEX uq_profiles_active
    ON model_request_profiles(model,resolution,scenario)
    WHERE status='active';
CREATE INDEX idx_profiles_history
    ON model_request_profiles(model,resolution,scenario,profile_version DESC);

CREATE TABLE profile_test_runs (
    id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL REFERENCES model_request_profiles(id),
    config_hash TEXT NOT NULL,
    test_scope TEXT NOT NULL CHECK (test_scope IN ('generation','acceleration','lora','interpolation','restoration','watermark','full_chain')),
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','passed','failed','cancelled')),
    node_id TEXT REFERENCES model_service_nodes(id),
    execution_id TEXT,
    request_snapshot_json TEXT NOT NULL CHECK (json_valid(request_snapshot_json)),
    result_json TEXT CHECK (result_json IS NULL OR json_valid(result_json)),
    artifact_id TEXT REFERENCES task_artifacts(id),
    error_code TEXT,
    error_message TEXT,
    created_at INTEGER NOT NULL,
    started_at INTEGER,
    finished_at INTEGER
);

CREATE INDEX idx_profile_tests_gate
    ON profile_test_runs(profile_id,config_hash,test_scope,status);

ALTER TABLE video_tasks ADD COLUMN profile_id TEXT REFERENCES model_request_profiles(id);
ALTER TABLE video_tasks ADD COLUMN profile_version INTEGER;
ALTER TABLE video_tasks ADD COLUMN config_snapshot_json TEXT CHECK (config_snapshot_json IS NULL OR json_valid(config_snapshot_json));
ALTER TABLE video_tasks ADD COLUMN config_hash TEXT;
ALTER TABLE video_tasks ADD COLUMN active_stage_id TEXT REFERENCES task_stages(id);
ALTER TABLE video_tasks ADD COLUMN result_artifact_id TEXT REFERENCES task_artifacts(id);

CREATE TABLE task_stages (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES video_tasks(task_id),
    stage_order INTEGER NOT NULL CHECK (stage_order >= 0),
    stage_type TEXT NOT NULL CHECK (stage_type IN ('generation','interpolation','restoration','watermark','final_validation')),
    required INTEGER NOT NULL DEFAULT 1 CHECK (required IN (0,1)),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','leased','dispatching','running','validating','succeeded','failed','cancelled','skipped')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    max_attempts INTEGER NOT NULL CHECK (max_attempts >= 1),
    preferred_node_id TEXT REFERENCES model_service_nodes(id),
    current_node_id TEXT REFERENCES model_service_nodes(id),
    input_artifact_id TEXT REFERENCES task_artifacts(id),
    output_artifact_id TEXT REFERENCES task_artifacts(id),
    config_snapshot_json TEXT NOT NULL CHECK (json_valid(config_snapshot_json)),
    lease_token TEXT,
    lease_expires_at INTEGER,
    next_attempt_at INTEGER,
    error_code TEXT,
    error_message TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    started_at INTEGER,
    finished_at INTEGER,
    row_version INTEGER NOT NULL DEFAULT 1 CHECK (row_version >= 1),
    UNIQUE(task_id,stage_order)
);

CREATE INDEX idx_stages_claim
    ON task_stages(status,next_attempt_at,lease_expires_at,stage_order);
CREATE INDEX idx_stages_task
    ON task_stages(task_id,stage_order);

CREATE TABLE stage_attempts (
    id TEXT PRIMARY KEY,
    stage_id TEXT NOT NULL REFERENCES task_stages(id),
    attempt_no INTEGER NOT NULL CHECK (attempt_no >= 1),
    operation_id TEXT NOT NULL UNIQUE,
    node_id TEXT NOT NULL REFERENCES model_service_nodes(id),
    execution_id TEXT,
    status TEXT NOT NULL CHECK (status IN ('dispatching','running','validating','succeeded','failed','unknown')),
    input_artifact_id TEXT REFERENCES task_artifacts(id),
    output_artifact_id TEXT REFERENCES task_artifacts(id),
    media_before_json TEXT CHECK (media_before_json IS NULL OR json_valid(media_before_json)),
    media_after_json TEXT CHECK (media_after_json IS NULL OR json_valid(media_after_json)),
    error_code TEXT,
    error_message TEXT,
    started_at INTEGER NOT NULL,
    heartbeat_at INTEGER,
    finished_at INTEGER,
    UNIQUE(stage_id,attempt_no)
);

CREATE UNIQUE INDEX uq_stage_attempt_execution
    ON stage_attempts(node_id,execution_id)
    WHERE execution_id IS NOT NULL;
