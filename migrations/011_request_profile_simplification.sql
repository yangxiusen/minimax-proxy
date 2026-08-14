DELETE FROM profile_test_runs;

UPDATE video_tasks
SET profile_id = NULL
WHERE profile_id IS NOT NULL
  AND profile_id NOT IN (
      SELECT id
      FROM (
          SELECT id,
                 ROW_NUMBER() OVER (PARTITION BY resolution ORDER BY updated_at DESC, id DESC) AS position
          FROM model_request_profiles
      ) ranked
      WHERE position = 1
  );

UPDATE model_request_profiles
SET source_profile_id = NULL;

DELETE FROM model_request_profiles
WHERE id NOT IN (
    SELECT id
    FROM (
        SELECT id,
               ROW_NUMBER() OVER (PARTITION BY resolution ORDER BY updated_at DESC, id DESC) AS position
        FROM model_request_profiles
    ) ranked
    WHERE position = 1
);

ALTER TABLE model_request_profiles ADD COLUMN updated_by TEXT;
UPDATE model_request_profiles SET updated_by = created_by WHERE updated_by IS NULL;

DROP INDEX IF EXISTS uq_profiles_active;
DROP INDEX IF EXISTS idx_profiles_history;
CREATE UNIQUE INDEX uq_profiles_resolution ON model_request_profiles(resolution);

PRAGMA user_version = 11;
