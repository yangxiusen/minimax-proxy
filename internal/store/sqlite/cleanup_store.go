package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrCleanupPreviewStale = errors.New("清理预览已过期或候选已变化")
	ErrCleanupNotFound     = errors.New("清理作业不存在")
	ErrCleanupNotRetryable = errors.New("清理作业没有可重试项")
)

type CleanupPreview struct {
	Token              string
	ExpiresAt          int64
	CutoffAt           int64
	CandidateCount     int
	CandidateBytes     int64
	SkippedActiveCount int
	ByNode             []CleanupNodeProgress
}

type CleanupJobDetail struct {
	ID, Reason, Status, Scope, RequestedBy, ErrorSummary  string
	OlderThanDays                                         int
	CutoffAt, CreatedAt, StartedAt, FinishedAt, UpdatedAt int64
	TotalCount, SucceededCount, FailedCount, SkippedCount int
	CandidateBytes, DeletedBytes                          int64
}

type CleanupNodeProgress struct {
	NodeID       string `json:"node_id"`
	Count        int    `json:"count,omitempty"`
	Pending      int    `json:"pending,omitempty"`
	Deleting     int    `json:"deleting,omitempty"`
	Succeeded    int    `json:"succeeded,omitempty"`
	Failed       int    `json:"failed,omitempty"`
	Skipped      int    `json:"skipped,omitempty"`
	Bytes        int64  `json:"bytes,omitempty"`
	DeletedBytes int64  `json:"deleted_bytes,omitempty"`
}

type CleanupItemDetail struct {
	ID, ArtifactID, LocationID, NodeID, Status, LastErrorCode, LastErrorMessage string
	AttemptCount                                                                int
	NextAttemptAt, UpdatedAt                                                    int64
}

type cleanupCandidate struct {
	TaskID, ArtifactID, LocationID, NodeID string
	SizeBytes                              int64
}

type rowQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) PreviewArtifactCleanup(ctx context.Context, olderThanDays int, requestedBy string) (CleanupPreview, error) {
	if olderThanDays < 1 || olderThanDays > 3650 || requestedBy == "" {
		return CleanupPreview{}, errors.New("清理预览参数无效")
	}
	now := time.UnixMilli(s.nowMillis()).UTC()
	cutoff := now.Add(-time.Duration(olderThanDays) * 24 * time.Hour).UnixMilli()
	candidates, skipped, err := cleanupCandidates(ctx, s.db, cutoff)
	if err != nil {
		return CleanupPreview{}, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return CleanupPreview{}, err
	}
	jobID := uuid.NewString()
	token := jobID + "." + hex.EncodeToString(secret)
	tokenHash := sha256.Sum256([]byte(token))
	digest := cleanupCandidateDigest(candidates)
	preview := CleanupPreview{Token: token, ExpiresAt: now.Add(15 * time.Minute).UnixMilli(), CutoffAt: cutoff, SkippedActiveCount: skipped}
	preview.CandidateCount, preview.CandidateBytes, preview.ByNode = cleanupCandidateSummary(candidates)
	_, err = s.db.ExecContext(ctx, `INSERT INTO artifact_deletion_jobs(id,reason,status,scope,older_than_days,cutoff_at,preview_token_hash,dry_run,requested_by,total_count,skipped_count,candidate_bytes,created_at,updated_at,error_summary) VALUES(?,'manual_cleanup','preview','managed_task_artifacts',?,?,?,1,?,?,?,?,?,?,?)`, jobID, olderThanDays, cutoff, hex.EncodeToString(tokenHash[:]), requestedBy, preview.CandidateCount, skipped, preview.CandidateBytes, now.UnixMilli(), now.UnixMilli(), "preview_digest:"+digest)
	return preview, err
}

func (s *Store) ConfirmArtifactCleanup(ctx context.Context, token, confirmation string) (job CleanupJobDetail, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(parts[0]) != 36 || len(parts[1]) != 64 {
		return job, ErrCleanupPreviewStale
	}
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return job, err
	}
	defer completeTransaction(finish, &err)
	var storedHash, summary string
	err = conn.QueryRowContext(ctx, cleanupJobSelect+` WHERE id=? AND status='preview'`, parts[0]).Scan(cleanupJobScanArgs(&job, &storedHash, &summary)...)
	if errors.Is(err, sql.ErrNoRows) {
		return CleanupJobDetail{}, ErrCleanupPreviewStale
	}
	if err != nil {
		return CleanupJobDetail{}, err
	}
	actualHash := sha256.Sum256([]byte(token))
	stored, decodeErr := hex.DecodeString(storedHash)
	if decodeErr != nil || len(stored) != sha256.Size || subtle.ConstantTimeCompare(actualHash[:], stored) != 1 || s.nowMillis() > job.CreatedAt+int64(15*time.Minute/time.Millisecond) {
		return CleanupJobDetail{}, ErrCleanupPreviewStale
	}
	wantConfirmation := fmt.Sprintf("DELETE %d ARTIFACTS", job.TotalCount)
	if confirmation != wantConfirmation {
		return CleanupJobDetail{}, errors.New("清理确认文本不匹配")
	}
	candidates, _, err := cleanupCandidates(ctx, conn, job.CutoffAt)
	if err != nil {
		return CleanupJobDetail{}, err
	}
	count, bytes, _ := cleanupCandidateSummary(candidates)
	if count != job.TotalCount || bytes != job.CandidateBytes || summary != "preview_digest:"+cleanupCandidateDigest(candidates) {
		return CleanupJobDetail{}, ErrCleanupPreviewStale
	}
	now := s.nowMillis()
	for _, candidate := range candidates {
		if _, err := conn.ExecContext(ctx, `INSERT INTO artifact_deletion_items(id,job_id,artifact_id,location_id,node_id,status,operation_id,created_at,updated_at) VALUES(?,?,?,?,?,'pending',?,?,?)`, uuid.NewString(), job.ID, candidate.ArtifactID, candidate.LocationID, candidate.NodeID, uuid.NewString(), now, now); err != nil {
			return CleanupJobDetail{}, err
		}
	}
	if _, err := conn.ExecContext(ctx, `UPDATE artifact_locations SET state='delete_pending',is_primary=0 WHERE id IN (SELECT location_id FROM artifact_deletion_items WHERE job_id=?)`, job.ID); err != nil {
		return CleanupJobDetail{}, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE task_artifacts SET state='delete_pending' WHERE id IN (SELECT artifact_id FROM artifact_deletion_items WHERE job_id=?)`, job.ID); err != nil {
		return CleanupJobDetail{}, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE video_tasks SET deleted_at=?,deletion_state='pending',updated_at=?,version=version+1 WHERE task_id IN (SELECT DISTINCT a.task_id FROM artifact_deletion_items i JOIN task_artifacts a ON a.id=i.artifact_id WHERE i.job_id=?) AND deleted_at IS NULL`, now, now, job.ID); err != nil {
		return CleanupJobDetail{}, err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE task_id IN (SELECT DISTINCT a.task_id FROM artifact_deletion_items i JOIN task_artifacts a ON a.id=i.artifact_id WHERE i.job_id=?)`, job.ID); err != nil {
		return CleanupJobDetail{}, err
	}
	status := "pending"
	var finished any
	if len(candidates) == 0 {
		status, finished = "succeeded", now
	}
	if _, err := conn.ExecContext(ctx, `UPDATE artifact_deletion_jobs SET status=?,preview_token_hash=NULL,dry_run=0,finished_at=?,updated_at=?,error_summary=NULL WHERE id=? AND status='preview'`, status, finished, now, job.ID); err != nil {
		return CleanupJobDetail{}, err
	}
	return getCleanupJobWith(ctx, conn, job.ID)
}

func (s *Store) GetArtifactCleanup(ctx context.Context, jobID string) (CleanupJobDetail, []CleanupNodeProgress, error) {
	job, err := getCleanupJobWith(ctx, s.db, jobID)
	if err != nil {
		return CleanupJobDetail{}, nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT node_id,COUNT(*),SUM(CASE WHEN status IN ('pending','retry_wait') THEN 1 ELSE 0 END),SUM(CASE WHEN status='deleting' THEN 1 ELSE 0 END),SUM(CASE WHEN status IN ('deleted','already_absent') THEN 1 ELSE 0 END),SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),SUM(CASE WHEN status='skipped' THEN 1 ELSE 0 END),COALESCE(SUM(deleted_bytes),0) FROM artifact_deletion_items WHERE job_id=? GROUP BY node_id ORDER BY node_id`, jobID)
	if err != nil {
		return job, nil, err
	}
	defer rows.Close()
	var nodes []CleanupNodeProgress
	for rows.Next() {
		var item CleanupNodeProgress
		if err := rows.Scan(&item.NodeID, &item.Count, &item.Pending, &item.Deleting, &item.Succeeded, &item.Failed, &item.Skipped, &item.DeletedBytes); err != nil {
			return job, nil, err
		}
		nodes = append(nodes, item)
	}
	return job, nodes, rows.Err()
}

func (s *Store) ListArtifactCleanupItems(ctx context.Context, jobID, status, nodeID string, limit int) ([]CleanupItemDetail, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	query := `SELECT id,artifact_id,location_id,node_id,status,attempt_count,COALESCE(next_attempt_at,0),COALESCE(last_error_code,''),COALESCE(last_error_message,''),updated_at FROM artifact_deletion_items WHERE job_id=?`
	args := []any{jobID}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	if nodeID != "" {
		query += ` AND node_id=?`
		args = append(args, nodeID)
	}
	query += ` ORDER BY created_at,id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]CleanupItemDetail, 0)
	for rows.Next() {
		var item CleanupItemDetail
		if err := rows.Scan(&item.ID, &item.ArtifactID, &item.LocationID, &item.NodeID, &item.Status, &item.AttemptCount, &item.NextAttemptAt, &item.LastErrorCode, &item.LastErrorMessage, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) RetryArtifactCleanup(ctx context.Context, jobID string, nodeIDs []string) (CleanupJobDetail, error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return CleanupJobDetail{}, err
	}
	defer completeTransaction(finish, &err)
	query := `UPDATE artifact_deletion_items SET status='pending',next_attempt_at=NULL,lease_token=NULL,lease_expires_at=NULL,last_error_code=NULL,last_error_message=NULL,updated_at=? WHERE job_id=? AND status='failed'`
	args := []any{s.nowMillis(), jobID}
	if len(nodeIDs) > 0 {
		placeholders := make([]string, len(nodeIDs))
		for index, id := range nodeIDs {
			if id == "" {
				return CleanupJobDetail{}, errors.New("节点 ID 无效")
			}
			placeholders[index] = "?"
			args = append(args, id)
		}
		query += ` AND node_id IN (` + strings.Join(placeholders, ",") + `)`
	}
	result, err := conn.ExecContext(ctx, query, args...)
	if err != nil {
		return CleanupJobDetail{}, err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return CleanupJobDetail{}, ErrCleanupNotRetryable
	}
	now := s.nowMillis()
	if _, err := conn.ExecContext(ctx, `UPDATE artifact_locations SET state='delete_pending' WHERE id IN (SELECT location_id FROM artifact_deletion_items WHERE job_id=? AND status='pending')`, jobID); err != nil {
		return CleanupJobDetail{}, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE artifact_deletion_jobs SET status='pending',failed_count=(SELECT COUNT(*) FROM artifact_deletion_items WHERE job_id=? AND status='failed'),finished_at=NULL,updated_at=?,error_summary=NULL WHERE id=?`, jobID, now, jobID); err != nil {
		return CleanupJobDetail{}, err
	}
	return getCleanupJobWith(ctx, conn, jobID)
}

const cleanupJobSelect = `SELECT id,reason,status,scope,COALESCE(older_than_days,0),COALESCE(cutoff_at,0),COALESCE(preview_token_hash,''),requested_by,total_count,succeeded_count,failed_count,skipped_count,candidate_bytes,deleted_bytes,created_at,COALESCE(started_at,0),COALESCE(finished_at,0),updated_at,COALESCE(error_summary,'') FROM artifact_deletion_jobs`

func cleanupJobScanArgs(job *CleanupJobDetail, tokenHash, summary *string) []any {
	return []any{&job.ID, &job.Reason, &job.Status, &job.Scope, &job.OlderThanDays, &job.CutoffAt, tokenHash, &job.RequestedBy, &job.TotalCount, &job.SucceededCount, &job.FailedCount, &job.SkippedCount, &job.CandidateBytes, &job.DeletedBytes, &job.CreatedAt, &job.StartedAt, &job.FinishedAt, &job.UpdatedAt, summary}
}

func getCleanupJobWith(ctx context.Context, querier rowQueryer, jobID string) (CleanupJobDetail, error) {
	var job CleanupJobDetail
	var tokenHash, summary string
	err := querier.QueryRowContext(ctx, cleanupJobSelect+` WHERE id=?`, jobID).Scan(cleanupJobScanArgs(&job, &tokenHash, &summary)...)
	if errors.Is(err, sql.ErrNoRows) {
		return CleanupJobDetail{}, ErrCleanupNotFound
	}
	job.ErrorSummary = summary
	return job, err
}

func cleanupCandidates(ctx context.Context, querier rowQueryer, cutoff int64) ([]cleanupCandidate, int, error) {
	rows, err := querier.QueryContext(ctx, `SELECT t.task_id,a.id,l.id,l.node_id,l.size_bytes FROM video_tasks t JOIN task_artifacts a ON a.task_id=t.task_id JOIN artifact_locations l ON l.artifact_id=a.id WHERE t.deleted_at IS NULL AND t.status IN ('succeeded','failed','cancelled') AND a.created_at<? AND a.state='active' AND l.state='active' AND NOT EXISTS (SELECT 1 FROM task_artifacts newer WHERE newer.task_id=t.task_id AND newer.state='active' AND newer.created_at>=?) ORDER BY t.task_id,a.id,l.id`, cutoff, cutoff)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var candidates []cleanupCandidate
	for rows.Next() {
		var item cleanupCandidate
		if err := rows.Scan(&item.TaskID, &item.ArtifactID, &item.LocationID, &item.NodeID, &item.SizeBytes); err != nil {
			return nil, 0, err
		}
		candidates = append(candidates, item)
	}
	var skipped int
	_ = querier.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_tasks WHERE deleted_at IS NULL AND status NOT IN ('succeeded','failed','cancelled') AND created_at<?`, cutoff/1000).Scan(&skipped)
	return candidates, skipped, rows.Err()
}

func cleanupCandidateSummary(items []cleanupCandidate) (int, int64, []CleanupNodeProgress) {
	byNode := make(map[string]*CleanupNodeProgress)
	var bytes int64
	for _, item := range items {
		bytes += item.SizeBytes
		node := byNode[item.NodeID]
		if node == nil {
			node = &CleanupNodeProgress{NodeID: item.NodeID}
			byNode[item.NodeID] = node
		}
		node.Count++
		node.Bytes += item.SizeBytes
	}
	nodes := make([]CleanupNodeProgress, 0, len(byNode))
	for _, node := range byNode {
		nodes = append(nodes, *node)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })
	return len(items), bytes, nodes
}

func cleanupCandidateDigest(items []cleanupCandidate) string {
	hash := sha256.New()
	for _, item := range items {
		fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\x00%d\n", item.TaskID, item.ArtifactID, item.LocationID, item.NodeID, item.SizeBytes)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
