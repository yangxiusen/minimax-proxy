CREATE TABLE IF NOT EXISTS model_service_nodes (
    id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64),
    base_url TEXT NOT NULL CHECK (length(base_url) > 0),
    jobs_base_url TEXT NOT NULL CHECK (length(jobs_base_url) > 0),
    public_base_url TEXT NOT NULL CHECK (length(public_base_url) > 0),
    health_path TEXT NOT NULL DEFAULT '/' CHECK (length(health_path) BETWEEN 1 AND 256),
    submit_api_name TEXT NOT NULL DEFAULT 'submit_minimax_from_slots' CHECK (length(submit_api_name) BETWEEN 1 AND 128),
    check_api_name TEXT NOT NULL DEFAULT 'check_and_get_video' CHECK (length(check_api_name) BETWEEN 1 AND 128),
    poll_interval_ms INTEGER NOT NULL DEFAULT 3000 CHECK (poll_interval_ms BETWEEN 1000 AND 300000),
    request_timeout_ms INTEGER NOT NULL DEFAULT 30000 CHECK (request_timeout_ms BETWEEN 1000 AND 300000),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    deleted_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_model_service_nodes_active
    ON model_service_nodes(enabled, id)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS node_config_bootstrap (
    source TEXT PRIMARY KEY CHECK (source = 'yaml_upstreams'),
    imported_count INTEGER NOT NULL DEFAULT 0 CHECK (imported_count >= 0),
    completed_at INTEGER NOT NULL
);
