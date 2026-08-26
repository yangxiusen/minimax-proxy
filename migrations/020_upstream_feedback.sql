ALTER TABLE video_tasks
ADD COLUMN upstream_feedback_json TEXT
CHECK (upstream_feedback_json IS NULL OR json_valid(upstream_feedback_json));
