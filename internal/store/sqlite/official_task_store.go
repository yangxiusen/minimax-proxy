package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"minimax-h3-tc/internal/domain"
)

func (s *Store) SaveOfficialSubmissionBaseline(ctx context.Context, taskID, nodeID string, taskIDs []string) error {
	data, err := json.Marshal(taskIDs)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE video_tasks SET upstream_jobs_before_json=?,official_submission_baseline_saved=1,updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND upstream_slot_active=1 AND status='dispatching' AND protocol_version='minimax-v2' AND route_state='ready'`, string(data), s.nowUnix(), taskID, nodeID)
	return oneRow(result, err)
}

func (s *Store) ListActiveOfficialTasks(ctx context.Context, nodeID string) ([]domain.Task, error) {
	rows, err := s.db.QueryContext(ctx, taskSelect+` WHERE upstream_id=? AND upstream_slot_active=1 AND deleted_at IS NULL AND protocol_version='minimax-v2' AND route_state='ready' AND status IN ('dispatching','running') ORDER BY queue_seq`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.Task, 0)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, task)
	}
	return result, rows.Err()
}

func (s *Store) ActiveOfficialCount(ctx context.Context, nodeID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_tasks WHERE upstream_id=? AND upstream_slot_active=1`, nodeID).Scan(&count)
	return count, err
}

// ClaimNextOfficial atomically enforces the configured node capacity and claims
// the oldest queued task that the MiniMax V2 API can execute.
func (s *Store) ClaimNextOfficial(ctx context.Context, nodeID string, nodeVersion int64, capacity int) (taskResult domain.Task, err error) {
	if capacity < 1 {
		return taskResult, domain.ErrUpstreamBusy
	}
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return taskResult, err
	}
	defer completeTransaction(finish, &err)

	var replaceResultURL, configuredCapacity, legacyCompat int
	var serviceURL, upstreamModel string
	checkErr := conn.QueryRowContext(ctx, `SELECT replace_result_url,max_concurrency,legacy_model_compat,service_url,upstream_model FROM model_service_nodes WHERE id=? AND version=? AND enabled=1 AND deleted_at IS NULL AND protocol_version='minimax-v2'`, nodeID, nodeVersion).Scan(&replaceResultURL, &configuredCapacity, &legacyCompat, &serviceURL, &upstreamModel)
	if errors.Is(checkErr, sql.ErrNoRows) {
		return taskResult, domain.ErrNodeConfigStale
	}
	if checkErr != nil {
		return taskResult, checkErr
	}
	capacity = min(capacity, configuredCapacity)
	var active int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_tasks WHERE upstream_id=? AND upstream_slot_active=1`, nodeID).Scan(&active); err != nil {
		return taskResult, err
	}
	if active >= capacity {
		return taskResult, domain.ErrUpstreamBusy
	}

	selected, err := nextEligibleTask(ctx, conn, nodeID, s.nowUnix())
	if err != nil {
		return taskResult, err
	}
	taskID, owner, wanted := selected.TaskID, selected.APIKeyID, selected.Status
	model := selected.Model
	if legacyCompat == 1 {
		model = upstreamModel
	}
	dispatch, err := json.Marshal(map[string]any{"schema_version": 1, "node_id": nodeID, "node_version": nodeVersion, "protocol_version": domain.ProtocolOfficial, "service_url": serviceURL, "upstream_model": model})
	if err != nil {
		return taskResult, err
	}
	now := s.nowUnix()
	updated, err := conn.ExecContext(ctx, `UPDATE video_tasks SET status='dispatching',cancel_locked=1,upstream_id=?,upstream_slot_active=1,upstream_node_version=?,delivery_required=?,dispatch_snapshot_json=?,started_at=COALESCE(started_at,?),attempt_started_at=?,updated_at=?,version=version+1 WHERE task_id=? AND status=?`, nodeID, nodeVersion, replaceResultURL, string(dispatch), now, now, now, taskID, wanted)
	if err := oneRow(updated, err); err != nil {
		return taskResult, err
	}
	if err := s.rebalance(ctx, conn, now); err != nil {
		return taskResult, err
	}
	return getWith(ctx, conn, owner, taskID, now)
}

func (s *Store) BindOfficialTask(ctx context.Context, taskID, nodeID, upstreamTaskID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE video_tasks SET upstream_job_id=?,status='running',updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND upstream_slot_active=1 AND status='dispatching' AND protocol_version='minimax-v2' AND route_state='ready'`, upstreamTaskID, s.nowUnix(), taskID, nodeID)
	return oneRow(result, err)
}

// MarkOfficialGenerated always persists the immutable origin URL. When delivery
// replacement is requested, public delivery remains empty until the upload job succeeds.
func (s *Store) MarkOfficialGenerated(ctx context.Context, taskID, nodeID, originURL, ratio string, uploadJob *domain.ResultUploadJob) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	now, nowMS := s.nowUnix(), s.nowMillis()
	status, publicURL := domain.StatusSucceeded, originURL
	finishedAt := any(now)
	if uploadJob != nil {
		status, publicURL, finishedAt = domain.StatusReconciling, "", nil
	}
	updated, err := conn.ExecContext(ctx, `UPDATE video_tasks SET status=?,result_internal_url=?,result_public_url=NULLIF(?,''),ratio_actual=?,usage_total_seconds=duration,usage_output_seconds=duration,upstream_slot_active=0,finished_at=?,updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND upstream_slot_active=1 AND status IN ('dispatching','running') AND protocol_version='minimax-v2' AND route_state='ready'`, status, originURL, publicURL, ratio, finishedAt, now, taskID, nodeID)
	if err := oneRow(updated, err); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE task_stages SET status=CASE WHEN stage_type='generation' THEN 'succeeded' ELSE 'skipped' END,lease_token=NULL,lease_expires_at=NULL,next_attempt_at=NULL,updated_at=?,finished_at=COALESCE(finished_at,?),row_version=row_version+1 WHERE task_id=? AND status NOT IN ('succeeded','skipped')`, nowMS, nowMS, taskID); err != nil {
		return err
	}
	if uploadJob != nil {
		_, err = conn.ExecContext(ctx, `INSERT INTO result_upload_jobs(id,task_id,object_key,status,round_no,attempt_no,max_attempts,created_at,updated_at) VALUES(?,?,?,'pending',1,0,3,?,?)`, uploadJob.ID, taskID, uploadJob.ObjectKey, nowMS, nowMS)
		return err
	}
	return createCallbackDeliveryWithConn(ctx, conn, taskID, "succeeded", nowMS)
}

func (s *Store) MarkOfficialFailed(ctx context.Context, taskID, nodeID, code, message string, feedback *domain.UpstreamFeedback) (err error) {
	var feedbackJSON any
	if feedback != nil {
		data, marshalErr := json.Marshal(feedback)
		if marshalErr != nil {
			return fmt.Errorf("序列化上游反馈: %w", marshalErr)
		}
		feedbackJSON = string(data)
	}
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	now, nowMS := s.nowUnix(), s.nowMillis()
	updated, err := conn.ExecContext(ctx, `UPDATE video_tasks SET status='failed',error_code=?,error_message=?,upstream_feedback_json=?,upstream_slot_active=0,finished_at=?,updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND upstream_slot_active=1 AND status IN ('dispatching','running') AND protocol_version='minimax-v2' AND route_state='ready'`, code, message, feedbackJSON, now, now, taskID, nodeID)
	if err := oneRow(updated, err); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE task_stages SET status='failed',error_code=?,error_message=?,lease_token=NULL,lease_expires_at=NULL,updated_at=?,finished_at=?,row_version=row_version+1 WHERE task_id=? AND status NOT IN ('succeeded','skipped')`, code, message, nowMS, nowMS, taskID); err != nil {
		return err
	}
	return createCallbackDeliveryWithConn(ctx, conn, taskID, "failed", nowMS)
}
