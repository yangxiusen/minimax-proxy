-- 只读诊断：不修改任务、租约或上传轮次。展示时间使用北京时间。
-- upstream_status 是 Proxy 已保存的上游状态，不是实时查询结果。
SELECT t.task_id,
       t.status AS proxy_status,
       r.upstream_task_id,
       r.upstream_status AS recorded_upstream_status,
       u.status AS delivery_status,
       u.round_no,
       u.attempt_no,
       u.max_attempts,
       datetime(NULLIF(u.lease_expires_at, 0) / 1000, 'unixepoch', '+8 hours') AS lease_expires_beijing,
       CASE WHEN u.status = 'uploading'
                  AND u.attempt_no >= u.max_attempts
                  AND COALESCE(u.lease_expires_at, 0) <= CAST(strftime('%s', 'now') AS INTEGER) * 1000
            THEN 'expired_final_attempt'
            ELSE '' END AS recovery_needed,
       u.last_error_code
FROM video_tasks t
LEFT JOIN task_remote_runs r ON r.task_id = t.task_id
LEFT JOIN result_upload_jobs u ON u.task_id = t.task_id
WHERE t.deleted_at IS NULL
  AND ((u.status IS NOT NULL AND u.status <> 'succeeded')
       OR (r.phase IS NOT NULL AND r.phase <> 'terminal'))
ORDER BY t.created_at, t.task_id;
