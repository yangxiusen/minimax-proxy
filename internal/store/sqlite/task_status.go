package sqlite

// 列表过滤与详情共享远程排队语义，结果交付的 reconciling 仍为 running。
const taskPublicStatusSQL = `CASE
 WHEN status IN ('succeeded','failed','cancelled') THEN status
 WHEN protocol_version='tk2sd-v1' AND (SELECT cancel_state FROM task_remote_runs r WHERE r.task_id=video_tasks.task_id)='requested' THEN 'running'
 WHEN protocol_version='tk2sd-v1' AND EXISTS(SELECT 1 FROM task_remote_runs r WHERE r.task_id=video_tasks.task_id AND (r.phase IN ('preparing','prepared') OR (r.phase='submitted' AND r.upstream_status='queued'))) THEN 'queued'
 WHEN status IN ('queued_open','queued_locked') THEN 'queued'
 ELSE 'running' END`
