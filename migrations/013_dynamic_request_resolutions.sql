CREATE TABLE request_profiles (
    id TEXT PRIMARY KEY,
    resolution TEXT NOT NULL,
    resolution_key TEXT NOT NULL CHECK (length(resolution_key) BETWEEN 1 AND 32),
    config_json TEXT NOT NULL CHECK (json_valid(config_json)),
    config_hash TEXT NOT NULL CHECK (length(config_hash) > 0),
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    row_version INTEGER NOT NULL DEFAULT 1 CHECK (row_version >= 1)
);

INSERT INTO request_profiles(id,resolution,resolution_key,config_json,config_hash,created_by,updated_by,created_at,updated_at,row_version)
SELECT id,trim(resolution),lower(trim(resolution)),config_json,config_hash,created_by,COALESCE(updated_by,created_by),created_at,updated_at,row_version
FROM model_request_profiles;

CREATE UNIQUE INDEX uq_request_profiles_resolution_key ON request_profiles(resolution_key);

PRAGMA user_version = 13;
