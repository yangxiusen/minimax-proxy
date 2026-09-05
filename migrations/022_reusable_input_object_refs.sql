CREATE TABLE task_input_spool_files_v22 (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    task_id TEXT NOT NULL REFERENCES video_tasks(task_id),
    content_index INTEGER NOT NULL CHECK (content_index >= 0),
    content_type TEXT NOT NULL CHECK (content_type IN ('image_url','video_url','audio_url')),
    role TEXT NOT NULL CHECK (length(role) > 0),
    source_kind TEXT NOT NULL DEFAULT 'data_uri' CHECK (source_kind = 'data_uri'),
    declared_mime TEXT,
    detected_mime TEXT,
    media_type TEXT NOT NULL CHECK (length(media_type) > 0),
    extension TEXT NOT NULL CHECK (length(extension) > 0),
    relative_path TEXT NOT NULL CHECK (length(relative_path) > 0),
    object_url TEXT CHECK (object_url IS NULL OR (length(object_url) > 8 AND substr(lower(object_url), 1, 8) = 'https://')),
    size_bytes INTEGER NOT NULL CHECK (size_bytes > 0),
    sha256 TEXT NOT NULL CHECK (length(sha256) = 64),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(task_id, content_index)
);

INSERT INTO task_input_spool_files_v22 (
    id, task_id, content_index, content_type, role, source_kind,
    declared_mime, detected_mime, media_type, extension, relative_path,
    object_url, size_bytes, sha256, created_at, updated_at
)
SELECT
    id, task_id, content_index, content_type, role, source_kind,
    declared_mime, detected_mime, media_type, extension, relative_path,
    object_url, size_bytes, sha256, created_at, updated_at
FROM task_input_spool_files;

DROP TABLE task_input_spool_files;
ALTER TABLE task_input_spool_files_v22 RENAME TO task_input_spool_files;

CREATE INDEX idx_task_input_spool_files_task
    ON task_input_spool_files(task_id);

CREATE INDEX idx_task_input_spool_files_relative_path
    ON task_input_spool_files(relative_path);

PRAGMA user_version = 22;
