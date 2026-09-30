-- 由 migrateV23 在专用连接重建任务表后执行；版本和历史回填在同一事务内完成。
ALTER TABLE model_service_nodes ADD COLUMN legacy_model_compat INTEGER NOT NULL DEFAULT 0 CHECK (legacy_model_compat IN (0,1));

ALTER TABLE video_tasks ADD COLUMN protocol_version TEXT;
ALTER TABLE video_tasks ADD COLUMN route_state TEXT NOT NULL DEFAULT 'awaiting_route'
    CHECK (route_state IN ('ready','awaiting_route','migration_blocked','legacy_unknown'));
ALTER TABLE video_tasks ADD COLUMN routing_snapshot_json TEXT CHECK (routing_snapshot_json IS NULL OR json_valid(routing_snapshot_json));
ALTER TABLE video_tasks ADD COLUMN dispatch_snapshot_json TEXT CHECK (dispatch_snapshot_json IS NULL OR json_valid(dispatch_snapshot_json));
ALTER TABLE video_tasks ADD COLUMN request_normalizer TEXT NOT NULL DEFAULT 'legacy-h3-v1';
ALTER TABLE video_tasks ADD COLUMN routing_wait_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE video_tasks ADD COLUMN latest_result_url TEXT;
ALTER TABLE video_tasks ADD COLUMN latest_result_expires_at INTEGER;
ALTER TABLE video_tasks ADD COLUMN result_metadata_json TEXT CHECK (result_metadata_json IS NULL OR json_valid(result_metadata_json));
ALTER TABLE video_tasks ADD COLUMN metadata_status TEXT NOT NULL DEFAULT 'none' CHECK (metadata_status IN ('none','pending','ready','unavailable'));

CREATE INDEX idx_tasks_protocol_queue ON video_tasks(protocol_version,model,queue_seq)
    WHERE deleted_at IS NULL AND status IN ('queued_open','queued_locked') AND route_state='ready';
CREATE INDEX idx_tasks_owner_model_time ON video_tasks(api_key_id,model,created_at DESC) WHERE deleted_at IS NULL;

CREATE TRIGGER validate_video_tasks_route_insert BEFORE INSERT ON video_tasks
WHEN NEW.route_state='ready' AND (NEW.protocol_version IS NULL OR length(NEW.protocol_version)=0
    OR NEW.routing_snapshot_json IS NULL
    OR json_extract(NEW.routing_snapshot_json,'$.protocol_version') IS NOT NEW.protocol_version)
BEGIN SELECT RAISE(ABORT,'ready task requires matching routing snapshot'); END;
CREATE TRIGGER validate_video_tasks_route_update BEFORE UPDATE ON video_tasks
WHEN NEW.route_state='ready' AND (NEW.protocol_version IS NULL OR length(NEW.protocol_version)=0
    OR NEW.routing_snapshot_json IS NULL
    OR json_extract(NEW.routing_snapshot_json,'$.protocol_version') IS NOT NEW.protocol_version)
BEGIN SELECT RAISE(ABORT,'ready task requires matching routing snapshot'); END;
CREATE TRIGGER protect_video_tasks_route BEFORE UPDATE ON video_tasks
WHEN OLD.route_state='ready' AND OLD.protocol_version IS NOT NULL AND (
    NEW.protocol_version IS NOT OLD.protocol_version OR NEW.routing_snapshot_json IS NOT OLD.routing_snapshot_json
    OR NEW.route_state IS NOT OLD.route_state OR NEW.request_normalizer IS NOT OLD.request_normalizer)
BEGIN SELECT RAISE(ABORT,'task routing snapshot is immutable'); END;

CREATE TABLE routing_state (
    id INTEGER PRIMARY KEY CHECK (id=1),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision>=1),
    instance_id TEXT NOT NULL UNIQUE CHECK (length(instance_id)>0),
    updated_at INTEGER NOT NULL
);
CREATE TABLE node_model_catalogs (
    node_id TEXT NOT NULL PRIMARY KEY REFERENCES model_service_nodes(id),
    source TEXT NOT NULL CHECK (source IN ('builtin','manual','discovered')),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision>=1),
    connection_fingerprint TEXT NOT NULL CHECK (length(connection_fingerprint)=64),
    last_status TEXT NOT NULL CHECK (last_status IN ('uninitialized','ready','empty','error')),
    last_attempt_at INTEGER,
    last_success_at INTEGER,
    valid_until INTEGER,
    last_error_code TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX idx_node_catalog_refresh ON node_model_catalogs(source,last_attempt_at);
CREATE TABLE node_models (
    node_id TEXT NOT NULL REFERENCES model_service_nodes(id),
    model_id TEXT NOT NULL COLLATE BINARY CHECK (length(model_id) BETWEEN 1 AND 128),
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
    present INTEGER NOT NULL DEFAULT 1 CHECK (present IN (0,1)),
    capabilities_json TEXT NOT NULL CHECK (json_valid(capabilities_json)),
    verified INTEGER NOT NULL DEFAULT 0 CHECK (verified IN (0,1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(node_id,model_id)
);
CREATE INDEX idx_node_models_route ON node_models(model_id,node_id) WHERE enabled=1 AND present=1;
CREATE TABLE model_routes (
    model_id TEXT NOT NULL PRIMARY KEY COLLATE BINARY CHECK (length(model_id) BETWEEN 1 AND 128),
    protocol_version TEXT,
    selection_mode TEXT NOT NULL CHECK (selection_mode IN ('unresolved','auto','manual')),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version>=1),
    updated_by TEXT NOT NULL DEFAULT 'system',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    CHECK ((selection_mode='unresolved' AND protocol_version IS NULL)
        OR (selection_mode IN ('auto','manual') AND protocol_version IS NOT NULL AND length(protocol_version)>0))
);
CREATE TABLE task_remote_runs (
    task_id TEXT NOT NULL PRIMARY KEY REFERENCES video_tasks(task_id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES model_service_nodes(id),
    phase TEXT NOT NULL CHECK (phase IN ('preparing','prepared','submit_intent','submitted','terminal')),
    submission_key TEXT NOT NULL UNIQUE CHECK (length(submission_key)>0),
    request_body_json TEXT CHECK (request_body_json IS NULL OR json_valid(request_body_json)),
    request_body_hash TEXT CHECK (request_body_hash IS NULL OR length(request_body_hash)=64),
    upstream_task_id TEXT CHECK (upstream_task_id IS NULL OR length(upstream_task_id)>0),
    upstream_status TEXT CHECK (upstream_status IS NULL OR upstream_status IN ('queued','running','succeeded','failed','cancelled')),
    cancel_state TEXT NOT NULL DEFAULT 'none' CHECK (cancel_state IN ('none','requested','confirmed','rejected')),
    reconciliation_required INTEGER NOT NULL DEFAULT 0 CHECK (reconciliation_required IN (0,1)),
    submit_attempts INTEGER NOT NULL DEFAULT 0 CHECK (submit_attempts>=0),
    lease_token TEXT,
    lease_expires_at INTEGER,
    next_poll_at INTEGER,
    last_error_code TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    CHECK ((phase NOT IN ('prepared','submit_intent','submitted') AND NOT (phase='terminal' AND submit_attempts>0))
        OR (request_body_json IS NOT NULL AND request_body_hash IS NOT NULL)),
    CHECK (phase<>'submitted' OR upstream_task_id IS NOT NULL)
);
CREATE UNIQUE INDEX uq_remote_node_task ON task_remote_runs(node_id,upstream_task_id) WHERE upstream_task_id IS NOT NULL;
CREATE INDEX idx_remote_resume ON task_remote_runs(node_id,phase,next_poll_at,lease_expires_at);
CREATE TRIGGER protect_remote_submission BEFORE UPDATE ON task_remote_runs
WHEN (OLD.phase IN ('submit_intent','submitted','terminal') AND (
    NEW.node_id IS NOT OLD.node_id OR NEW.submission_key IS NOT OLD.submission_key
    OR NEW.request_body_json IS NOT OLD.request_body_json OR NEW.request_body_hash IS NOT OLD.request_body_hash
    OR NEW.phase IN ('preparing','prepared')))
    OR (OLD.phase='submitted' AND NEW.phase='submit_intent')
    OR (OLD.phase='terminal' AND NEW.phase<>'terminal')
    OR (OLD.upstream_task_id IS NOT NULL AND NEW.upstream_task_id IS NOT OLD.upstream_task_id)
    OR NEW.submit_attempts<OLD.submit_attempts
BEGIN SELECT RAISE(ABORT,'remote submission evidence is immutable'); END;
CREATE TABLE task_node_assets (
    task_id TEXT NOT NULL REFERENCES video_tasks(task_id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES model_service_nodes(id),
    content_index INTEGER NOT NULL CHECK (content_index>=0),
    asset_id TEXT NOT NULL CHECK (length(asset_id)>0),
    content_type TEXT NOT NULL,
    role TEXT NOT NULL,
    source_sha256 TEXT NOT NULL CHECK (length(source_sha256)=64),
    size_bytes INTEGER NOT NULL CHECK (size_bytes>0),
    metadata_json TEXT NOT NULL CHECK (json_valid(metadata_json)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(task_id,node_id,content_index),
    CHECK ((content_type='image_url' AND role IN ('first_frame','last_frame','reference_image'))
        OR (content_type='video_url' AND role='reference_video')
        OR (content_type='audio_url' AND role='reference_audio'))
);
