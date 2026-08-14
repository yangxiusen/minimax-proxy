PRAGMA defer_foreign_keys=ON;

CREATE TABLE model_service_nodes_v7 (
    id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64),
    service_url TEXT NOT NULL CHECK (length(service_url) > 0),
    protocol_version TEXT NOT NULL DEFAULT 'h3-node-v1' CHECK (length(protocol_version) > 0),
    api_key_ciphertext BLOB,
    api_key_nonce BLOB,
    api_key_fingerprint TEXT,
    api_key_id TEXT,
    capabilities_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(capabilities_json)),
    health_path TEXT NOT NULL DEFAULT '/internal/v1/health' CHECK (length(health_path) BETWEEN 1 AND 256),
    poll_interval_ms INTEGER NOT NULL DEFAULT 3000 CHECK (poll_interval_ms BETWEEN 1000 AND 300000),
    request_timeout_ms INTEGER NOT NULL DEFAULT 30000 CHECK (request_timeout_ms BETWEEN 1000 AND 300000),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    deleted_at INTEGER,
    base_url TEXT NOT NULL,
    jobs_base_url TEXT NOT NULL,
    public_base_url TEXT NOT NULL,
    submit_api_name TEXT NOT NULL DEFAULT 'submit_minimax_from_slots',
    check_api_name TEXT NOT NULL DEFAULT 'check_and_get_video',
    CHECK (
        (protocol_version='legacy-gradio-v1' AND api_key_ciphertext IS NULL AND api_key_nonce IS NULL AND api_key_fingerprint IS NULL AND api_key_id IS NULL)
        OR
        (protocol_version<>'legacy-gradio-v1' AND api_key_ciphertext IS NOT NULL AND api_key_nonce IS NOT NULL AND api_key_fingerprint IS NOT NULL AND api_key_id IS NOT NULL)
    )
);

INSERT INTO model_service_nodes_v7 (
    id,service_url,protocol_version,capabilities_json,health_path,poll_interval_ms,
    request_timeout_ms,enabled,version,created_at,updated_at,deleted_at,
    base_url,jobs_base_url,public_base_url,submit_api_name,check_api_name
)
SELECT
    id,base_url,'legacy-gradio-v1','{}',health_path,poll_interval_ms,
    request_timeout_ms,enabled,version,created_at,updated_at,deleted_at,
    base_url,jobs_base_url,public_base_url,submit_api_name,check_api_name
FROM model_service_nodes;

DROP TABLE model_service_nodes;
ALTER TABLE model_service_nodes_v7 RENAME TO model_service_nodes;

CREATE UNIQUE INDEX uq_model_service_nodes_service_url_active
    ON model_service_nodes(service_url)
    WHERE deleted_at IS NULL AND protocol_version<>'legacy-gradio-v1';
CREATE INDEX idx_model_service_nodes_enabled
    ON model_service_nodes(enabled,id)
    WHERE deleted_at IS NULL;
CREATE INDEX idx_model_service_nodes_protocol
    ON model_service_nodes(protocol_version);
