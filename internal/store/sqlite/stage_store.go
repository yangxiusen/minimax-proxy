package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"minimax-h3-tc/internal/domain"
)

var ErrNoClaimableStage = errors.New("没有可领取阶段")

type TaskStage struct {
	ID                 string
	TaskID             string
	StageOrder         int
	StageType          string
	Required           bool
	Status             string
	AttemptCount       int
	MaxAttempts        int
	PreferredNodeID    string
	CurrentNodeID      string
	InputArtifactID    string
	OutputArtifactID   string
	ConfigSnapshotJSON string
	LeaseToken         string
	LeaseExpiresAt     int64
	NextAttemptAt      int64
	CreatedAt          int64
	UpdatedAt          int64
	RowVersion         int64
}

type StageAttempt struct {
	ID                  string
	StageID             string
	AttemptNo           int
	OperationID         string
	NodeID              string
	LeaseToken          string
	ExecutionID         string
	Status              string
	InputArtifactID     string
	OutputArtifactID    string
	MediaBeforeJSON     string
	MediaAfterJSON      string
	RequestSnapshotJSON string
	ErrorCode           string
	ErrorMessage        string
	StartedAt           int64
	HeartbeatAt         int64
	FinishedAt          int64
}

const stageSelect = `SELECT id,task_id,stage_order,stage_type,required,status,attempt_count,max_attempts,COALESCE(preferred_node_id,''),COALESCE(current_node_id,''),COALESCE(input_artifact_id,''),COALESCE(output_artifact_id,''),config_snapshot_json,COALESCE(lease_token,''),COALESCE(lease_expires_at,0),COALESCE(next_attempt_at,0),created_at,updated_at,row_version FROM task_stages`

const claimStageCandidateSelect = `SELECT candidate.id FROM task_stages candidate JOIN video_tasks task ON task.task_id=candidate.task_id WHERE task.deleted_at IS NULL AND task.status IN ('queued_open','queued_locked','dispatching','running','reconciling') AND ((candidate.status='pending' AND candidate.attempt_count<candidate.max_attempts) OR (candidate.status='leased' AND candidate.attempt_count<=candidate.max_attempts) OR candidate.status IN ('dispatching','running','validating','unknown')) AND (candidate.next_attempt_at IS NULL OR candidate.next_attempt_at<=?) AND (candidate.lease_expires_at IS NULL OR candidate.lease_expires_at<=?) AND (candidate.preferred_node_id IS NULL OR candidate.preferred_node_id=?) AND (candidate.status NOT IN ('dispatching','running','validating','unknown') OR candidate.current_node_id=?) AND NOT EXISTS (SELECT 1 FROM task_stages earlier WHERE earlier.task_id=candidate.task_id AND earlier.stage_order<candidate.stage_order AND earlier.status NOT IN ('succeeded','skipped')) AND NOT EXISTS (SELECT 1 FROM json_each(task.request_json,'$.content') content WHERE json_extract(content.value,'$.type') IN ('image_url','video_url','audio_url') AND substr(lower(COALESCE(json_extract(content.value,'$.image_url.url'),json_extract(content.value,'$.video_url.url'),json_extract(content.value,'$.audio_url.url'),'')),1,10)='mm_file://') ORDER BY task.queue_seq,candidate.stage_order,candidate.id LIMIT 1`

func (s *Store) CreateStages(ctx context.Context, stages []TaskStage) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	now := s.nowMillis()
	for _, stage := range stages {
		required := stage.Required
		if !stage.Required {
			required = true
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO task_stages(id,task_id,stage_order,stage_type,required,max_attempts,preferred_node_id,input_artifact_id,config_snapshot_json,created_at,updated_at) VALUES(?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,?,?)`, stage.ID, stage.TaskID, stage.StageOrder, stage.StageType, boolInt(required), stage.MaxAttempts, stage.PreferredNodeID, stage.InputArtifactID, stage.ConfigSnapshotJSON, now, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ClaimStage(ctx context.Context, nodeID, leaseToken string, leaseDuration time.Duration) (result TaskStage, err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return TaskStage{}, err
	}
	defer completeTransaction(finish, &err)
	var blocked int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_dispatch_barriers WHERE node_id=?`, nodeID).Scan(&blocked); err != nil {
		return TaskStage{}, err
	}
	if blocked > 0 {
		return TaskStage{}, ErrNoClaimableStage
	}
	now := s.nowMillis()
	stage, err := scanStage(conn.QueryRowContext(ctx, stageSelect+` WHERE task_stages.id=(`+claimStageCandidateSelect+`)`, now, now, nodeID, nodeID))
	if errors.Is(err, sql.ErrNoRows) {
		return TaskStage{}, ErrNoClaimableStage
	}
	if err != nil {
		return TaskStage{}, err
	}
	expires := now + leaseDuration.Milliseconds()
	resultSQL, err := conn.ExecContext(ctx, `UPDATE task_stages SET status='leased',current_node_id=?,lease_token=?,lease_expires_at=?,updated_at=?,row_version=row_version+1 WHERE id=? AND row_version=? AND status IN ('pending','leased','dispatching','running','validating','unknown') AND (lease_expires_at IS NULL OR lease_expires_at<=?)`, nodeID, leaseToken, expires, now, stage.ID, stage.RowVersion, now)
	if err := oneRow(resultSQL, err); err != nil {
		return TaskStage{}, ErrNoClaimableStage
	}
	return scanStage(conn.QueryRowContext(ctx, stageSelect+` WHERE id=?`, stage.ID))
}

func (s *Store) RenewStageLease(ctx context.Context, stageID, leaseToken string, leaseDuration time.Duration) error {
	now := s.nowMillis()
	result, err := s.db.ExecContext(ctx, `UPDATE task_stages SET lease_expires_at=?,updated_at=?,row_version=row_version+1 WHERE id=? AND lease_token=? AND status IN ('leased','dispatching','running','validating') AND lease_expires_at>?`, now+leaseDuration.Milliseconds(), now, stageID, leaseToken, now)
	return oneRow(result, err)
}

func (s *Store) CreateStageAttempt(ctx context.Context, attempt StageAttempt) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	var currentCount, maxAttempts int
	var stageStatus, leaseToken, currentNodeID string
	var taskStatus domain.InternalStatus
	if err := conn.QueryRowContext(ctx, `SELECT s.attempt_count,s.max_attempts,s.status,COALESCE(s.lease_token,''),COALESCE(s.current_node_id,''),t.status FROM task_stages s JOIN video_tasks t ON t.task_id=s.task_id WHERE s.id=? AND t.deleted_at IS NULL`, attempt.StageID).Scan(&currentCount, &maxAttempts, &stageStatus, &leaseToken, &currentNodeID, &taskStatus); err != nil {
		return err
	}
	if stageStatus != "leased" || attempt.LeaseToken == "" || attempt.LeaseToken != leaseToken || attempt.NodeID != currentNodeID || !taskStatus.AdminCanCancel() {
		return domain.ErrStateConflict
	}
	if attempt.AttemptNo != currentCount+1 || attempt.AttemptNo > maxAttempts {
		return errors.New("阶段尝试序号冲突")
	}
	if attempt.StartedAt == 0 {
		attempt.StartedAt = s.nowMillis()
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO stage_attempts(id,stage_id,attempt_no,operation_id,node_id,execution_id,status,input_artifact_id,output_artifact_id,media_before_json,media_after_json,request_snapshot_json,error_code,error_message,started_at,heartbeat_at,finished_at) VALUES(?,?,?,?,?,NULLIF(?,''),?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?,NULLIF(?,0),NULLIF(?,0))`, attempt.ID, attempt.StageID, attempt.AttemptNo, attempt.OperationID, attempt.NodeID, attempt.ExecutionID, attempt.Status, attempt.InputArtifactID, attempt.OutputArtifactID, attempt.MediaBeforeJSON, attempt.MediaAfterJSON, attempt.RequestSnapshotJSON, attempt.ErrorCode, attempt.ErrorMessage, attempt.StartedAt, attempt.HeartbeatAt, attempt.FinishedAt); err != nil {
		return err
	}
	result, err := conn.ExecContext(ctx, `UPDATE task_stages SET attempt_count=attempt_count+1,updated_at=?,row_version=row_version+1 WHERE id=? AND attempt_count=?`, attempt.StartedAt, attempt.StageID, currentCount)
	return oneRow(result, err)
}

func (s *Store) BindStageExecution(ctx context.Context, stageID, leaseToken, attemptID, executionID string) error {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	now := s.nowMillis()
	result, err := conn.ExecContext(ctx, `UPDATE stage_attempts SET execution_id=?,status='running',heartbeat_at=? WHERE id=? AND stage_id=? AND status IN ('dispatching','running','validating','unknown') AND (execution_id IS NULL OR execution_id=?)`, executionID, now, attemptID, stageID, executionID)
	if err := oneRow(result, err); err != nil {
		return err
	}
	result, err = conn.ExecContext(ctx, `UPDATE task_stages SET status='running',updated_at=?,started_at=COALESCE(started_at,?),row_version=row_version+1 WHERE id=? AND lease_token=? AND status='leased'`, now, now, stageID, leaseToken)
	if err := oneRow(result, err); err != nil {
		return err
	}
	var taskID, nodeID string
	if err := conn.QueryRowContext(ctx, `SELECT s.task_id,a.node_id FROM task_stages s JOIN stage_attempts a ON a.stage_id=s.id WHERE s.id=? AND a.id=?`, stageID, attemptID).Scan(&taskID, &nodeID); err != nil {
		return err
	}
	result, err = conn.ExecContext(ctx, `UPDATE video_tasks SET status='running',upstream_id=?,active_stage_id=?,started_at=COALESCE(started_at,?),updated_at=?,version=version+1 WHERE task_id=? AND deleted_at IS NULL AND status IN ('queued_open','queued_locked','dispatching','running','reconciling')`, nodeID, stageID, now/1000, now/1000, taskID)
	if err := oneRow(result, err); err != nil {
		return err
	}
	return createCallbackDeliveryWithConn(ctx, conn, taskID, "running", now)
}

func (s *Store) GetRunningStageAttempt(ctx context.Context, stageID, leaseToken string) (attempt StageAttempt, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT a.id,a.stage_id,a.attempt_no,a.operation_id,a.node_id,COALESCE(a.execution_id,''),a.status,COALESCE(a.input_artifact_id,''),COALESCE(a.output_artifact_id,''),COALESCE(a.media_before_json,''),COALESCE(a.media_after_json,''),COALESCE(a.request_snapshot_json,''),COALESCE(a.error_code,''),COALESCE(a.error_message,''),a.started_at,COALESCE(a.heartbeat_at,0),COALESCE(a.finished_at,0) FROM stage_attempts a JOIN task_stages s ON s.id=a.stage_id WHERE a.stage_id=? AND s.lease_token=? ORDER BY a.attempt_no DESC LIMIT 1`, stageID, leaseToken).Scan(
		&attempt.ID, &attempt.StageID, &attempt.AttemptNo, &attempt.OperationID, &attempt.NodeID, &attempt.ExecutionID,
		&attempt.Status, &attempt.InputArtifactID, &attempt.OutputArtifactID, &attempt.MediaBeforeJSON, &attempt.MediaAfterJSON, &attempt.RequestSnapshotJSON,
		&attempt.ErrorCode, &attempt.ErrorMessage, &attempt.StartedAt, &attempt.HeartbeatAt, &attempt.FinishedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return StageAttempt{}, ErrNoClaimableStage
	}
	return attempt, err
}

func (s *Store) CompleteStage(ctx context.Context, stageID, leaseToken, attemptID, outputArtifactID string) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	return s.completeStageWithConn(ctx, conn, stageID, leaseToken, attemptID, outputArtifactID)
}

func (s *Store) completeStageWithConn(ctx context.Context, conn *sql.Conn, stageID, leaseToken, attemptID, outputArtifactID string) error {
	now := s.nowMillis()
	result, err := conn.ExecContext(ctx, `UPDATE stage_attempts SET status='succeeded',output_artifact_id=NULLIF(?,''),heartbeat_at=?,finished_at=? WHERE id=? AND stage_id=? AND status IN ('running','validating','unknown')`, outputArtifactID, now, now, attemptID, stageID)
	if err := oneRow(result, err); err != nil {
		return err
	}
	result, err = conn.ExecContext(ctx, `UPDATE task_stages SET status='succeeded',output_artifact_id=NULLIF(?,''),lease_token=NULL,lease_expires_at=NULL,updated_at=?,finished_at=?,row_version=row_version+1 WHERE id=? AND lease_token=? AND status IN ('running','validating','dispatching')`, outputArtifactID, now, now, stageID, leaseToken)
	if err := oneRow(result, err); err != nil {
		return err
	}
	var taskID string
	var stageOrder int
	if err := conn.QueryRowContext(ctx, `SELECT task_id,stage_order FROM task_stages WHERE id=?`, stageID).Scan(&taskID, &stageOrder); err != nil {
		return err
	}
	if outputArtifactID != "" {
		if _, err := conn.ExecContext(ctx, `UPDATE task_stages SET input_artifact_id=? WHERE task_id=? AND stage_order=(SELECT MIN(stage_order) FROM task_stages WHERE task_id=? AND stage_order>?) AND status='pending'`, outputArtifactID, taskID, taskID, stageOrder); err != nil {
			return err
		}
	}
	var remaining int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_stages WHERE task_id=? AND status NOT IN ('succeeded','skipped')`, taskID).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		_, err = conn.ExecContext(ctx, `UPDATE video_tasks SET status='succeeded',result_artifact_id=NULLIF(?,''),active_stage_id=NULL,usage_total_seconds=duration,usage_output_seconds=duration,finished_at=?,updated_at=?,version=version+1 WHERE task_id=? AND deleted_at IS NULL`, outputArtifactID, now/1000, now/1000, taskID)
	} else {
		_, err = conn.ExecContext(ctx, `UPDATE video_tasks SET status='running',upstream_id=NULL,active_stage_id=NULL,updated_at=?,version=version+1 WHERE task_id=? AND deleted_at IS NULL`, now/1000, taskID)
	}
	if err != nil {
		return err
	}
	if remaining == 0 {
		return createCallbackDeliveryWithConn(ctx, conn, taskID, "succeeded", now)
	}
	return createCallbackDeliveryWithConn(ctx, conn, taskID, "running", now)
}

func (s *Store) FailStage(ctx context.Context, stageID, leaseToken, attemptID, code, message string, retryAt time.Time, terminal bool) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	now := s.nowMillis()
	attemptStatus, stageStatus := "failed", "pending"
	var nextAttempt any = retryAt.UTC().UnixMilli()
	if terminal {
		stageStatus, nextAttempt = "failed", nil
	}
	if _, err := conn.ExecContext(ctx, `UPDATE stage_attempts SET status=?,error_code=?,error_message=?,heartbeat_at=?,finished_at=? WHERE id=? AND stage_id=?`, attemptStatus, code, message, now, now, attemptID, stageID); err != nil {
		return err
	}
	result, err := conn.ExecContext(ctx, `UPDATE task_stages SET status=?,error_code=?,error_message=?,next_attempt_at=?,lease_token=NULL,lease_expires_at=NULL,updated_at=?,finished_at=CASE WHEN ? THEN ? ELSE NULL END,row_version=row_version+1 WHERE id=? AND lease_token=?`, stageStatus, code, message, nextAttempt, now, terminal, now, stageID, leaseToken)
	if err := oneRow(result, err); err != nil {
		return err
	}
	var taskID string
	if err := conn.QueryRowContext(ctx, `SELECT task_id FROM task_stages WHERE id=?`, stageID).Scan(&taskID); err != nil {
		return err
	}
	if terminal {
		if _, err = conn.ExecContext(ctx, `UPDATE video_tasks SET status='failed',error_code=?,error_message=?,active_stage_id=NULL,finished_at=?,updated_at=?,version=version+1 WHERE task_id=? AND deleted_at IS NULL`, code, message, now/1000, now/1000, taskID); err != nil {
			return err
		}
		return createCallbackDeliveryWithConn(ctx, conn, taskID, "failed", now)
	}
	_, err = conn.ExecContext(ctx, `UPDATE video_tasks SET status='running',upstream_id=NULL,active_stage_id=NULL,updated_at=?,version=version+1 WHERE task_id=? AND deleted_at IS NULL`, now/1000, taskID)
	return err
}

func scanStage(scanner rowScanner) (stage TaskStage, err error) {
	var required int
	err = scanner.Scan(&stage.ID, &stage.TaskID, &stage.StageOrder, &stage.StageType, &required, &stage.Status, &stage.AttemptCount, &stage.MaxAttempts, &stage.PreferredNodeID, &stage.CurrentNodeID, &stage.InputArtifactID, &stage.OutputArtifactID, &stage.ConfigSnapshotJSON, &stage.LeaseToken, &stage.LeaseExpiresAt, &stage.NextAttemptAt, &stage.CreatedAt, &stage.UpdatedAt, &stage.RowVersion)
	stage.Required = required == 1
	return stage, err
}
