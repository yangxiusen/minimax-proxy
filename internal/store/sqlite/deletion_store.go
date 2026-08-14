package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrDeletionNotFound    = errors.New("删除作业不存在")
	ErrNoClaimableDeletion = errors.New("没有可领取删除明细")
)

type ArtifactDeletionJob struct {
	ID          string
	Reason      string
	Status      string
	RequestedBy string
	TotalCount  int
	CreatedAt   int64
	UpdatedAt   int64
}

type ArtifactDeletionItem struct {
	ID             string
	JobID          string
	ArtifactID     string
	LocationID     string
	NodeID         string
	Status         string
	OperationID    string
	AttemptCount   int
	NextAttemptAt  int64
	LeaseToken     string
	LeaseExpiresAt int64
	CreatedAt      int64
	UpdatedAt      int64
}

func (s *Store) RequestOwnedTaskDeletion(ctx context.Context, owner, taskID string) (ArtifactDeletionJob, error) {
	var taskOwner string
	var status string
	var deletedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT api_key_id,status,deleted_at FROM video_tasks WHERE task_id=?`, taskID).Scan(&taskOwner, &status, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) || taskOwner != owner || deletedAt.Valid {
		return ArtifactDeletionJob{}, ErrDeletionNotFound
	}
	if err != nil {
		return ArtifactDeletionJob{}, err
	}
	if status != "succeeded" && status != "failed" && status != "cancelled" {
		return ArtifactDeletionJob{}, errors.New("任务当前状态不可删除")
	}
	return s.RequestTaskDeletion(ctx, taskID, "task_delete", owner)
}

const deletionItemSelect = `SELECT id,job_id,artifact_id,location_id,node_id,status,operation_id,attempt_count,COALESCE(next_attempt_at,0),COALESCE(lease_token,''),COALESCE(lease_expires_at,0),created_at,updated_at FROM artifact_deletion_items`

func (s *Store) RequestTaskDeletion(ctx context.Context, taskID, reason, requestedBy string) (jobResult ArtifactDeletionJob, err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return ArtifactDeletionJob{}, err
	}
	defer completeTransaction(finish, &err)
	var deletedAt sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT deleted_at FROM video_tasks WHERE task_id=?`, taskID).Scan(&deletedAt); errors.Is(err, sql.ErrNoRows) {
		return ArtifactDeletionJob{}, ErrDeletionNotFound
	} else if err != nil {
		return ArtifactDeletionJob{}, err
	}
	if deletedAt.Valid {
		return ArtifactDeletionJob{}, fmt.Errorf("任务已存在删除意图")
	}
	job := ArtifactDeletionJob{ID: uuid.NewString(), Reason: reason, Status: "pending", RequestedBy: requestedBy, CreatedAt: s.nowMillis(), UpdatedAt: s.nowMillis()}
	rows, err := conn.QueryContext(ctx, `SELECT a.id,l.id,l.node_id,l.size_bytes FROM task_artifacts a JOIN artifact_locations l ON l.artifact_id=a.id WHERE a.task_id=? AND a.state NOT IN ('deleted','missing') AND l.state NOT IN ('deleted','missing') ORDER BY a.id,l.id`, taskID)
	if err != nil {
		return ArtifactDeletionJob{}, err
	}
	type candidate struct {
		artifactID, locationID, nodeID string
		sizeBytes                      int64
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.artifactID, &item.locationID, &item.nodeID, &item.sizeBytes); err != nil {
			rows.Close()
			return ArtifactDeletionJob{}, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return ArtifactDeletionJob{}, err
	}
	job.TotalCount = len(candidates)
	var finishedAt any
	deletionState := "pending"
	if job.TotalCount == 0 {
		job.Status = "succeeded"
		deletionState = "deleted"
		finishedAt = job.UpdatedAt
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO artifact_deletion_jobs(id,reason,status,scope,requested_by,total_count,created_at,updated_at,finished_at) VALUES(?,?,?,'managed_task_artifacts',?,?,?,?,?)`, job.ID, job.Reason, job.Status, job.RequestedBy, job.TotalCount, job.CreatedAt, job.UpdatedAt, finishedAt); err != nil {
		return ArtifactDeletionJob{}, err
	}
	for _, candidate := range candidates {
		itemID, operationID := uuid.NewString(), uuid.NewString()
		if _, err := conn.ExecContext(ctx, `INSERT INTO artifact_deletion_items(id,job_id,artifact_id,location_id,node_id,status,operation_id,created_at,updated_at) VALUES(?,?,?,?,?,'pending',?,?,?)`, itemID, job.ID, candidate.artifactID, candidate.locationID, candidate.nodeID, operationID, job.CreatedAt, job.UpdatedAt); err != nil {
			return ArtifactDeletionJob{}, err
		}
	}
	if _, err := conn.ExecContext(ctx, `UPDATE artifact_locations SET state='delete_pending',is_primary=0 WHERE artifact_id IN (SELECT id FROM task_artifacts WHERE task_id=?) AND state NOT IN ('deleted','missing')`, taskID); err != nil {
		return ArtifactDeletionJob{}, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE task_artifacts SET state='delete_pending' WHERE task_id=? AND state NOT IN ('deleted','missing')`, taskID); err != nil {
		return ArtifactDeletionJob{}, err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE task_id=?`, taskID); err != nil {
		return ArtifactDeletionJob{}, err
	}
	result, err := conn.ExecContext(ctx, `UPDATE video_tasks SET deleted_at=?,deletion_state=?,updated_at=?,version=version+1 WHERE task_id=? AND deleted_at IS NULL`, job.CreatedAt, deletionState, job.UpdatedAt, taskID)
	if err := oneRow(result, err); err != nil {
		return ArtifactDeletionJob{}, err
	}
	return job, nil
}

func (s *Store) ClaimDeletionItem(ctx context.Context, nodeID, leaseToken string, leaseDuration time.Duration) (result ArtifactDeletionItem, err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return ArtifactDeletionItem{}, err
	}
	defer completeTransaction(finish, &err)
	now := s.nowMillis()
	item, err := scanDeletionItem(conn.QueryRowContext(ctx, deletionItemSelect+` WHERE node_id=? AND status IN ('pending','retry_wait','deleting') AND (next_attempt_at IS NULL OR next_attempt_at<=?) AND (lease_expires_at IS NULL OR lease_expires_at<=?) ORDER BY created_at,id LIMIT 1`, nodeID, now, now))
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactDeletionItem{}, ErrNoClaimableDeletion
	}
	if err != nil {
		return ArtifactDeletionItem{}, err
	}
	expires := now + leaseDuration.Milliseconds()
	update, err := conn.ExecContext(ctx, `UPDATE artifact_deletion_items SET status='deleting',lease_token=?,lease_expires_at=?,attempt_count=attempt_count+1,updated_at=? WHERE id=? AND (lease_expires_at IS NULL OR lease_expires_at<=?)`, leaseToken, expires, now, item.ID, now)
	if err := oneRow(update, err); err != nil {
		return ArtifactDeletionItem{}, ErrNoClaimableDeletion
	}
	if _, err := conn.ExecContext(ctx, `UPDATE artifact_locations SET state='deleting' WHERE id=? AND state IN ('active','delete_pending','delete_failed','deleting')`, item.LocationID); err != nil {
		return ArtifactDeletionItem{}, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE artifact_deletion_jobs SET status='running',started_at=COALESCE(started_at,?),updated_at=? WHERE id=? AND status='pending'`, now, now, item.JobID); err != nil {
		return ArtifactDeletionItem{}, err
	}
	return scanDeletionItem(conn.QueryRowContext(ctx, deletionItemSelect+` WHERE id=?`, item.ID))
}

func (s *Store) RenewDeletionLease(ctx context.Context, itemID, leaseToken string, leaseDuration time.Duration) error {
	now := s.nowMillis()
	result, err := s.db.ExecContext(ctx, `UPDATE artifact_deletion_items SET lease_expires_at=?,updated_at=? WHERE id=? AND lease_token=? AND status='deleting' AND lease_expires_at>?`, now+leaseDuration.Milliseconds(), now, itemID, leaseToken, now)
	return oneRow(result, err)
}

func (s *Store) FailDeletionItem(ctx context.Context, itemID, leaseToken, code, message string, nextAttempt time.Time, terminal bool) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	var jobID, artifactID, locationID string
	if err := conn.QueryRowContext(ctx, `SELECT job_id,artifact_id,location_id FROM artifact_deletion_items WHERE id=? AND lease_token=? AND status='deleting'`, itemID, leaseToken).Scan(&jobID, &artifactID, &locationID); errors.Is(err, sql.ErrNoRows) {
		return ErrNoClaimableDeletion
	} else if err != nil {
		return err
	}
	now := s.nowMillis()
	itemStatus := "retry_wait"
	var nextAttemptAt any = nextAttempt.UTC().UnixMilli()
	if terminal {
		itemStatus = "failed"
		nextAttemptAt = nil
	}
	if _, err := conn.ExecContext(ctx, `UPDATE artifact_deletion_items SET status=?,next_attempt_at=?,lease_token=NULL,lease_expires_at=NULL,last_error_code=?,last_error_message=?,updated_at=? WHERE id=?`, itemStatus, nextAttemptAt, code, message, now, itemID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE artifact_locations SET state='delete_failed' WHERE id=?`, locationID); err != nil {
		return err
	}
	jobStatus := "running"
	taskState := "pending"
	var finishedAt any
	if terminal {
		var pending int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_deletion_items WHERE job_id=? AND status NOT IN ('deleted','already_absent','skipped','failed')`, jobID).Scan(&pending); err != nil {
			return err
		}
		if pending == 0 {
			var succeeded int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_deletion_items WHERE job_id=? AND status IN ('deleted','already_absent')`, jobID).Scan(&succeeded); err != nil {
				return err
			}
			jobStatus, taskState, finishedAt = "failed", "failed", now
			if succeeded > 0 {
				jobStatus, taskState = "partial_failed", "partial"
			}
		}
	}
	if _, err := conn.ExecContext(ctx, `UPDATE artifact_deletion_jobs SET status=?,failed_count=(SELECT COUNT(*) FROM artifact_deletion_items WHERE job_id=? AND status='failed'),finished_at=?,updated_at=? WHERE id=?`, jobStatus, jobID, finishedAt, now, jobID); err != nil {
		return err
	}
	var taskID string
	if err := conn.QueryRowContext(ctx, `SELECT task_id FROM task_artifacts WHERE id=?`, artifactID).Scan(&taskID); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `UPDATE video_tasks SET deletion_state=?,updated_at=?,version=version+1 WHERE task_id=? AND deleted_at IS NOT NULL`, taskState, now, taskID)
	return err
}

func (s *Store) CompleteDeletionItem(ctx context.Context, itemID, leaseToken string, absent bool, deletedBytes int64) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	var jobID, artifactID, locationID string
	if err := conn.QueryRowContext(ctx, `SELECT job_id,artifact_id,location_id FROM artifact_deletion_items WHERE id=? AND lease_token=? AND status='deleting'`, itemID, leaseToken).Scan(&jobID, &artifactID, &locationID); errors.Is(err, sql.ErrNoRows) {
		return ErrNoClaimableDeletion
	} else if err != nil {
		return err
	}
	now := s.nowMillis()
	status := "deleted"
	locationState := "deleted"
	if absent {
		status = "already_absent"
		locationState = "missing"
	}
	if _, err := conn.ExecContext(ctx, `UPDATE artifact_deletion_items SET status=?,deleted_bytes=?,deleted_at=?,lease_token=NULL,lease_expires_at=NULL,updated_at=? WHERE id=?`, status, deletedBytes, now, now, itemID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE artifact_locations SET state=?,is_primary=0,deleted_at=? WHERE id=?`, locationState, now, locationID); err != nil {
		return err
	}
	var remaining int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_locations WHERE artifact_id=? AND state NOT IN ('deleted','missing')`, artifactID).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		if _, err := conn.ExecContext(ctx, `UPDATE task_artifacts SET state='deleted',deleted_at=? WHERE id=?`, now, artifactID); err != nil {
			return err
		}
	}
	var total, succeeded, failed int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*),SUM(CASE WHEN status IN ('deleted','already_absent') THEN 1 ELSE 0 END),SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END) FROM artifact_deletion_items WHERE job_id=?`, jobID).Scan(&total, &succeeded, &failed); err != nil {
		return err
	}
	jobStatus := "running"
	var finished any
	if succeeded+failed == total {
		jobStatus, finished = "succeeded", now
		if failed > 0 {
			jobStatus = "partial_failed"
		}
	}
	if _, err := conn.ExecContext(ctx, `UPDATE artifact_deletion_jobs SET status=?,succeeded_count=?,failed_count=?,deleted_bytes=deleted_bytes+?,finished_at=?,updated_at=? WHERE id=?`, jobStatus, succeeded, failed, deletedBytes, finished, now, jobID); err != nil {
		return err
	}
	var taskID string
	if err := conn.QueryRowContext(ctx, `SELECT task_id FROM task_artifacts WHERE id=?`, artifactID).Scan(&taskID); err != nil {
		return err
	}
	var taskRemaining, taskFailed int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_locations l JOIN task_artifacts a ON a.id=l.artifact_id WHERE a.task_id=? AND l.state NOT IN ('deleted','missing')`, taskID).Scan(&taskRemaining); err != nil {
		return err
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_deletion_items i JOIN task_artifacts a ON a.id=i.artifact_id WHERE a.task_id=? AND i.status='failed'`, taskID).Scan(&taskFailed); err != nil {
		return err
	}
	taskState := "pending"
	if taskRemaining == 0 {
		taskState = "deleted"
	} else if taskFailed > 0 {
		taskState = "partial"
		if succeeded == 0 {
			taskState = "failed"
		}
	}
	_, err = conn.ExecContext(ctx, `UPDATE video_tasks SET deletion_state=?,updated_at=?,version=version+1 WHERE task_id=? AND deleted_at IS NOT NULL`, taskState, now, taskID)
	return err
}

func scanDeletionItem(scanner rowScanner) (item ArtifactDeletionItem, err error) {
	err = scanner.Scan(&item.ID, &item.JobID, &item.ArtifactID, &item.LocationID, &item.NodeID, &item.Status, &item.OperationID, &item.AttemptCount, &item.NextAttemptAt, &item.LeaseToken, &item.LeaseExpiresAt, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}
