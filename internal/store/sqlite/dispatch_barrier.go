package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	ErrNodeDispatchBarrierNotFound = errors.New("节点调度屏障不存在")
	ErrNodeDispatchBarrierConflict = errors.New("节点调度屏障版本冲突")
)

type NodeDispatchBarrier struct {
	NodeID              string
	TaskID              string
	StageID             string
	AttemptID           string
	OperationID         string
	ExecutionID         string
	CancelOperationID   string
	RequestSnapshotJSON string
	LastErrorCode       string
	RetryCount          int
	NextRetryAt         int64
	CreatedAt           int64
	UpdatedAt           int64
	RowVersion          int64
}

const nodeDispatchBarrierSelect = `SELECT b.node_id,b.task_id,b.stage_id,b.attempt_id,b.operation_id,COALESCE(b.execution_id,''),b.cancel_operation_id,COALESCE(a.request_snapshot_json,''),COALESCE(b.last_error_code,''),b.retry_count,b.next_retry_at,b.created_at,b.updated_at,b.row_version FROM node_dispatch_barriers b JOIN stage_attempts a ON a.id=b.attempt_id`

func (s *Store) GetNodeDispatchBarrier(ctx context.Context, nodeID string) (NodeDispatchBarrier, error) {
	barrier, err := scanNodeDispatchBarrier(s.db.QueryRowContext(ctx, nodeDispatchBarrierSelect+` WHERE b.node_id=?`, nodeID))
	if errors.Is(err, sql.ErrNoRows) {
		return NodeDispatchBarrier{}, ErrNodeDispatchBarrierNotFound
	}
	return barrier, err
}

func (s *Store) BindBarrierExecution(ctx context.Context, nodeID string, rowVersion int64, executionID string) error {
	now := s.nowMillis()
	result, err := s.db.ExecContext(ctx, `UPDATE node_dispatch_barriers SET execution_id=?,last_error_code=NULL,next_retry_at=0,updated_at=?,row_version=row_version+1 WHERE node_id=? AND row_version=? AND execution_id IS NULL`, executionID, now, nodeID, rowVersion)
	return barrierOneRow(result, err)
}

func (s *Store) DeferNodeDispatchBarrier(ctx context.Context, nodeID string, rowVersion int64, errorCode string, retryAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE node_dispatch_barriers SET last_error_code=NULLIF(?,''),retry_count=retry_count+1,next_retry_at=?,updated_at=?,row_version=row_version+1 WHERE node_id=? AND row_version=?`, errorCode, retryAt.UTC().UnixMilli(), s.nowMillis(), nodeID, rowVersion)
	return barrierOneRow(result, err)
}

func (s *Store) ResolveNodeDispatchBarrier(ctx context.Context, nodeID string, rowVersion int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM node_dispatch_barriers WHERE node_id=? AND row_version=?`, nodeID, rowVersion)
	return barrierOneRow(result, err)
}

func (s *Store) HasNodeDispatchBarrier(ctx context.Context, nodeID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_dispatch_barriers WHERE node_id=?`, nodeID).Scan(&count)
	return count > 0, err
}

func createNodeDispatchBarrierForTask(ctx context.Context, conn *sql.Conn, taskID string, now int64) (bool, error) {
	var barrier NodeDispatchBarrier
	err := conn.QueryRowContext(ctx, `SELECT a.node_id,s.task_id,s.id,a.id,a.operation_id,COALESCE(a.execution_id,'')
FROM task_stages s JOIN stage_attempts a ON a.stage_id=s.id
WHERE s.task_id=? AND a.status IN ('dispatching','running','validating','unknown')
ORDER BY s.stage_order DESC,a.attempt_no DESC LIMIT 1`, taskID).Scan(
		&barrier.NodeID, &barrier.TaskID, &barrier.StageID, &barrier.AttemptID, &barrier.OperationID, &barrier.ExecutionID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	barrier.CancelOperationID = "stage-cancel-" + barrier.AttemptID
	_, err = conn.ExecContext(ctx, `INSERT INTO node_dispatch_barriers(node_id,task_id,stage_id,attempt_id,operation_id,execution_id,cancel_operation_id,created_at,updated_at) VALUES(?,?,?,?,?,NULLIF(?,''),?,?,?)`,
		barrier.NodeID, barrier.TaskID, barrier.StageID, barrier.AttemptID, barrier.OperationID, barrier.ExecutionID, barrier.CancelOperationID, now, now)
	return true, err
}

func barrierOneRow(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNodeDispatchBarrierConflict
	}
	return nil
}

func scanNodeDispatchBarrier(scanner rowScanner) (NodeDispatchBarrier, error) {
	var barrier NodeDispatchBarrier
	err := scanner.Scan(&barrier.NodeID, &barrier.TaskID, &barrier.StageID, &barrier.AttemptID, &barrier.OperationID, &barrier.ExecutionID, &barrier.CancelOperationID, &barrier.RequestSnapshotJSON, &barrier.LastErrorCode, &barrier.RetryCount, &barrier.NextRetryAt, &barrier.CreatedAt, &barrier.UpdatedAt, &barrier.RowVersion)
	return barrier, err
}
