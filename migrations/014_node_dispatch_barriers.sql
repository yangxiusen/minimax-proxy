ALTER TABLE stage_attempts ADD COLUMN request_snapshot_json TEXT
    CHECK (request_snapshot_json IS NULL OR json_valid(request_snapshot_json));

CREATE TABLE node_dispatch_barriers (
    node_id TEXT PRIMARY KEY REFERENCES model_service_nodes(id),
    task_id TEXT NOT NULL REFERENCES video_tasks(task_id),
    stage_id TEXT NOT NULL REFERENCES task_stages(id),
    attempt_id TEXT NOT NULL REFERENCES stage_attempts(id),
    operation_id TEXT NOT NULL CHECK (length(operation_id) > 0),
    execution_id TEXT,
    cancel_operation_id TEXT NOT NULL UNIQUE CHECK (length(cancel_operation_id) > 0),
    last_error_code TEXT,
    retry_count INTEGER NOT NULL DEFAULT 0 CHECK (retry_count >= 0),
    next_retry_at INTEGER NOT NULL DEFAULT 0 CHECK (next_retry_at >= 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    row_version INTEGER NOT NULL DEFAULT 1 CHECK (row_version >= 1)
);

CREATE INDEX idx_node_dispatch_barriers_retry
    ON node_dispatch_barriers(next_retry_at,node_id);
CREATE INDEX idx_node_dispatch_barriers_task
    ON node_dispatch_barriers(task_id);
