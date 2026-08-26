ALTER TABLE video_tasks
ADD COLUMN official_submission_baseline_saved INTEGER NOT NULL DEFAULT 0
CHECK (official_submission_baseline_saved IN (0,1));

-- Pre-v19 active official tasks are ambiguous when their saved baseline is [].
-- Prefer reconciliation for those tasks to avoid duplicate upstream generation.
UPDATE video_tasks
SET official_submission_baseline_saved=1
WHERE upstream_job_id IS NOT NULL
   OR upstream_jobs_before_json <> '[]'
   OR (
       upstream_slot_active=1
       AND status IN ('dispatching','running','reconciling')
       AND EXISTS (
           SELECT 1
           FROM model_service_nodes node
           WHERE node.id=video_tasks.upstream_id
             AND node.protocol_version='minimax-v2'
       )
   );
