CREATE TABLE external_api_keys (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL CHECK(length(trim(name)) BETWEEN 1 AND 128),
    key_digest BLOB NOT NULL CHECK(length(key_digest) = 32),
    key_prefix TEXT NOT NULL CHECK(length(key_prefix) BETWEEN 4 AND 16),
    key_suffix TEXT NOT NULL CHECK(length(key_suffix) BETWEEN 4 AND 16),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK(version >= 1),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX uq_external_api_keys_name_ci ON external_api_keys(lower(name));
CREATE UNIQUE INDEX uq_external_api_keys_digest ON external_api_keys(key_digest);
CREATE INDEX idx_external_api_keys_enabled ON external_api_keys(enabled,id);

CREATE TABLE api_key_config_bootstrap (
    source TEXT PRIMARY KEY CHECK(source = 'yaml_api_keys'),
    imported_count INTEGER NOT NULL CHECK(imported_count >= 0),
    completed_at INTEGER NOT NULL
);
