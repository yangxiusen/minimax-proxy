ALTER TABLE task_input_spool_files
ADD COLUMN object_url TEXT
CHECK (object_url IS NULL OR (length(object_url) > 8 AND substr(lower(object_url), 1, 8) = 'https://'));
