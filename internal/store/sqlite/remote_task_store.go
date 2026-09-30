package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"minimax-h3-tc/internal/domain"
)

const remoteRunSelect = `SELECT task_id,node_id,phase,submission_key,COALESCE(request_body_json,''),COALESCE(request_body_hash,''),COALESCE(upstream_task_id,''),COALESCE(upstream_status,''),cancel_state,reconciliation_required,submit_attempts,COALESCE(lease_token,''),COALESCE(lease_expires_at,0),COALESCE(next_poll_at,0),last_error_code,created_at,updated_at FROM task_remote_runs`

func scanRemoteRun(row rowScanner) (r domain.RemoteRun, err error) {
	err = row.Scan(&r.TaskID, &r.NodeID, &r.Phase, &r.SubmissionKey, &r.RequestBodyJSON, &r.RequestBodyHash, &r.UpstreamTaskID, &r.UpstreamStatus, &r.CancelState, &r.ReconciliationRequired, &r.SubmitAttempts, &r.LeaseToken, &r.LeaseExpiresAt, &r.NextPollAt, &r.LastErrorCode, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = domain.ErrTaskNotFound
	}
	return
}

func (s *Store) GetRemoteRun(ctx context.Context, taskID string) (domain.RemoteRun, error) {
	return scanRemoteRun(s.db.QueryRowContext(ctx, remoteRunSelect+` WHERE task_id=?`, taskID))
}

func (s *Store) ClaimNextRemote(ctx context.Context, nodeID string, nodeVersion int64, capacity int) (task domain.Task, err error) {
	if capacity < 1 {
		return task, domain.ErrUpstreamBusy
	}
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return task, err
	}
	defer completeTransaction(finish, &err)
	var serviceURL, keyFingerprint string
	var delivery, configuredCapacity int
	err = conn.QueryRowContext(ctx, `SELECT service_url,replace_result_url,max_concurrency,COALESCE(api_key_fingerprint,'') FROM model_service_nodes WHERE id=? AND version=? AND enabled=1 AND deleted_at IS NULL AND protocol_version='tk2sd-v1'`, nodeID, nodeVersion).Scan(&serviceURL, &delivery, &configuredCapacity, &keyFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return task, domain.ErrNodeConfigStale
	}
	if err != nil {
		return task, err
	}
	capacity = min(capacity, configuredCapacity)
	var active int
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_tasks WHERE upstream_id=? AND upstream_slot_active=1`, nodeID).Scan(&active); err != nil {
		return task, err
	}
	if active >= capacity {
		return task, domain.ErrUpstreamBusy
	}
	now := s.nowUnix()
	fingerprint := CatalogFingerprint(domain.ModelNodeInput{ProtocolVersion: domain.ProtocolTK2SD, ServiceURL: serviceURL, APIKeyFingerprint: keyFingerprint})
	rows, err := conn.QueryContext(ctx, `SELECT t.task_id,t.api_key_id,t.model,t.routing_snapshot_json,m.capabilities_json
	 FROM video_tasks t JOIN node_models m ON m.node_id=? AND m.model_id=t.model COLLATE BINARY
	 JOIN node_model_catalogs c ON c.node_id=m.node_id
	 WHERE t.protocol_version='tk2sd-v1' AND t.route_state='ready' AND t.status IN ('queued_open','queued_locked') AND t.deleted_at IS NULL
	 AND t.upstream_slot_active=0 AND m.enabled=1 AND m.present=1 AND c.last_success_at IS NOT NULL
	 AND c.connection_fingerprint=? AND (c.source<>'discovered' OR c.valid_until>?) AND t.expires_at>?
	 AND NOT EXISTS(SELECT 1 FROM task_remote_runs r WHERE r.task_id=t.task_id)
	 ORDER BY t.queue_seq`, nodeID, fingerprint, now, now)
	if err != nil {
		return task, err
	}
	var taskID, owner, model string
	for rows.Next() {
		var id, key, m, snapshot, capability string
		if err = rows.Scan(&id, &key, &m, &snapshot, &capability); err != nil {
			break
		}
		var route domain.RouteSnapshot
		var caps domain.ModelCapability
		if json.Unmarshal([]byte(snapshot), &route) != nil || json.Unmarshal([]byte(capability), &caps) != nil || route.Model != m || route.ProtocolVersion != domain.ProtocolTK2SD || !caps.Accepts(route.Requirements) {
			continue
		}
		taskID, owner, model = id, key, m
		break
	}
	readErr := rows.Err()
	rows.Close()
	if err != nil {
		return task, err
	}
	if readErr != nil {
		return task, readErr
	}
	if taskID == "" {
		return task, domain.ErrQueueEmpty
	}
	var instance string
	if err = conn.QueryRowContext(ctx, `SELECT instance_id FROM routing_state WHERE id=1`).Scan(&instance); err != nil {
		return task, err
	}
	key := "proxy:" + instance + ":" + taskID
	if instance == "" || len(key) > 200 || strings.ContainsAny(key, "\r\n") {
		return task, domain.ErrStateConflict
	}
	dispatch, _ := json.Marshal(map[string]any{"schema_version": 1, "node_id": nodeID, "node_version": nodeVersion, "protocol_version": domain.ProtocolTK2SD, "service_url": serviceURL, "upstream_model": model})
	updated, err := conn.ExecContext(ctx, `UPDATE video_tasks SET status='dispatching',cancel_locked=1,upstream_id=?,upstream_slot_active=1,upstream_node_version=?,delivery_required=?,dispatch_snapshot_json=?,started_at=COALESCE(started_at,?),attempt_started_at=?,updated_at=?,version=version+1 WHERE task_id=? AND status IN ('queued_open','queued_locked')`, nodeID, nodeVersion, delivery, string(dispatch), now, now, now, taskID)
	if err = oneRow(updated, err); err != nil {
		return task, err
	}
	if _, err = conn.ExecContext(ctx, `INSERT INTO task_remote_runs(task_id,node_id,phase,submission_key,created_at,updated_at) VALUES(?,?,'preparing',?,?,?)`, taskID, nodeID, key, now, now); err != nil {
		return task, err
	}
	if err = s.rebalance(ctx, conn, now); err != nil {
		return task, err
	}
	return getWith(ctx, conn, owner, taskID, now)
}

func (s *Store) ListActiveRemoteTasks(ctx context.Context, nodeID string) ([]domain.Task, error) {
	rows, err := s.db.QueryContext(ctx, taskSelect+` WHERE upstream_id=? AND upstream_slot_active=1 AND deleted_at IS NULL AND protocol_version='tk2sd-v1' AND task_id IN (SELECT task_id FROM task_remote_runs WHERE phase<>'terminal' AND reconciliation_required=0 AND COALESCE(next_poll_at,0)<=? AND COALESCE(lease_expires_at,0)<=?) ORDER BY queue_seq`, nodeID, s.nowUnix(), s.nowUnix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []domain.Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *Store) AcquireRemoteLease(ctx context.Context, taskID, nodeID, token string, ttl time.Duration) (r domain.RemoteRun, err error) {
	if token == "" || ttl < time.Second {
		return r, domain.ErrStateConflict
	}
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return r, err
	}
	defer completeTransaction(finish, &err)
	now := s.nowUnix()
	updated, err := conn.ExecContext(ctx, `UPDATE task_remote_runs SET lease_token=?,lease_expires_at=?,updated_at=? WHERE task_id=? AND node_id=? AND phase<>'terminal' AND reconciliation_required=0 AND COALESCE(lease_expires_at,0)<=? AND EXISTS(SELECT 1 FROM video_tasks t WHERE t.task_id=task_remote_runs.task_id AND t.upstream_id=task_remote_runs.node_id AND t.upstream_slot_active=1 AND t.deleted_at IS NULL)`, token, now+int64(ttl/time.Second), now, taskID, nodeID, now)
	if err = oneRow(updated, err); err != nil {
		return r, err
	}
	return scanRemoteRun(conn.QueryRowContext(ctx, remoteRunSelect+` WHERE task_id=?`, taskID))
}

func (s *Store) RenewRemoteLease(ctx context.Context, r domain.RemoteRun, ttl time.Duration) error {
	if ttl < time.Second {
		return domain.ErrStateConflict
	}
	now := s.nowUnix()
	res, err := s.db.ExecContext(ctx, `UPDATE task_remote_runs SET lease_expires_at=?,updated_at=? WHERE task_id=? AND node_id=? AND lease_token=? AND lease_expires_at>? AND phase<>'terminal'`, now+int64(ttl/time.Second), now, r.TaskID, r.NodeID, r.LeaseToken, now)
	return oneRow(res, err)
}

func (s *Store) ReleaseRemoteLease(ctx context.Context, r domain.RemoteRun, next int64, code string, reconcile bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE task_remote_runs SET lease_token=NULL,lease_expires_at=NULL,next_poll_at=?,last_error_code=?,reconciliation_required=?,updated_at=? WHERE task_id=? AND node_id=? AND lease_token=? AND lease_expires_at>? AND phase<>'terminal'`, next, code, reconcile, s.nowUnix(), r.TaskID, r.NodeID, r.LeaseToken, s.nowUnix())
	return oneRow(res, err)
}

func (s *Store) PrepareRemoteRequest(ctx context.Context, r domain.RemoteRun, body string) error {
	if !json.Valid([]byte(body)) {
		return domain.ErrStateConflict
	}
	hash := sha256.Sum256([]byte(body))
	res, err := s.db.ExecContext(ctx, `UPDATE task_remote_runs SET phase='prepared',request_body_json=?,request_body_hash=?,updated_at=? WHERE task_id=? AND node_id=? AND lease_token=? AND lease_expires_at>? AND phase='preparing' AND cancel_state='none'`, body, hex.EncodeToString(hash[:]), s.nowUnix(), r.TaskID, r.NodeID, r.LeaseToken, s.nowUnix())
	return oneRow(res, err)
}

func (s *Store) BeginRemoteSubmission(ctx context.Context, r domain.RemoteRun) error {
	res, err := s.db.ExecContext(ctx, `UPDATE task_remote_runs SET phase='submit_intent',submit_attempts=submit_attempts+1,updated_at=? WHERE task_id=? AND node_id=? AND lease_token=? AND lease_expires_at>? AND reconciliation_required=0 AND (phase='submit_intent' OR (phase='prepared' AND cancel_state='none'))`, s.nowUnix(), r.TaskID, r.NodeID, r.LeaseToken, s.nowUnix())
	return oneRow(res, err)
}

func (s *Store) BindRemoteTask(ctx context.Context, r domain.RemoteRun, id string) (err error) {
	if id == "" {
		return domain.ErrStateConflict
	}
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	now := s.nowUnix()
	res, err := conn.ExecContext(ctx, `UPDATE task_remote_runs SET phase='submitted',upstream_task_id=?,upstream_status='queued',last_error_code='',updated_at=? WHERE task_id=? AND node_id=? AND lease_token=? AND lease_expires_at>? AND phase='submit_intent'`, id, now, r.TaskID, r.NodeID, r.LeaseToken, now)
	if err = oneRow(res, err); err != nil {
		return err
	}
	res, err = conn.ExecContext(ctx, `UPDATE video_tasks SET upstream_job_id=?,status=CASE WHEN status='cancelling' THEN status ELSE 'running' END,updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND upstream_slot_active=1 AND status IN ('dispatching','running','reconciling','cancelling')`, id, now, r.TaskID, r.NodeID)
	return oneRow(res, err)
}

func (s *Store) ObserveRemoteTask(ctx context.Context, r domain.RemoteRun, status, cancelState string) (err error) {
	if status != "queued" && status != "running" {
		return domain.ErrStateConflict
	}
	if cancelState != "none" && cancelState != "requested" && cancelState != "rejected" {
		return domain.ErrStateConflict
	}
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	res, err := conn.ExecContext(ctx, `UPDATE task_remote_runs SET upstream_status=?,cancel_state=CASE WHEN ?='rejected' THEN 'rejected' ELSE cancel_state END,updated_at=? WHERE task_id=? AND node_id=? AND lease_token=? AND lease_expires_at>? AND phase='submitted'`, status, cancelState, s.nowUnix(), r.TaskID, r.NodeID, r.LeaseToken, s.nowUnix())
	if err = oneRow(res, err); err != nil {
		return err
	}
	if cancelState == "rejected" {
		_, err = conn.ExecContext(ctx, `UPDATE video_tasks SET status='running',updated_at=?,version=version+1 WHERE task_id=? AND status='cancelling'`, s.nowUnix(), r.TaskID)
	}
	return err
}

func (s *Store) RequestRemoteCancel(ctx context.Context, taskID string) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	r, err := scanRemoteRun(conn.QueryRowContext(ctx, remoteRunSelect+` WHERE task_id=?`, taskID))
	if err != nil {
		return err
	}
	if r.Phase == domain.RemoteTerminal {
		if r.UpstreamStatus == "cancelled" {
			return nil
		}
		return domain.ErrRemoteNotCancellable
	}
	if r.UpstreamStatus == "running" || r.CancelState == "rejected" {
		return domain.ErrRemoteNotCancellable
	}
	res, err := conn.ExecContext(ctx, `UPDATE video_tasks SET status='cancelling',cancel_requested_at=COALESCE(cancel_requested_at,?),updated_at=?,version=version+1 WHERE task_id=? AND upstream_slot_active=1 AND deleted_at IS NULL AND status IN ('dispatching','running','reconciling','cancelling')`, s.nowUnix(), s.nowUnix(), taskID)
	if err = oneRow(res, err); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `UPDATE task_remote_runs SET cancel_state='requested',next_poll_at=NULL,updated_at=? WHERE task_id=?`, s.nowUnix(), taskID)
	return err
}

func (s *Store) GetRemoteAsset(ctx context.Context, taskID, nodeID string, index int) (a domain.NodeAsset, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT task_id,node_id,content_index,asset_id,content_type,role,source_sha256,size_bytes,metadata_json,created_at,updated_at FROM task_node_assets WHERE task_id=? AND node_id=? AND content_index=?`, taskID, nodeID, index).Scan(&a.TaskID, &a.NodeID, &a.ContentIndex, &a.AssetID, &a.ContentType, &a.Role, &a.SourceSHA256, &a.SizeBytes, &a.MetadataJSON, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = domain.ErrTaskNotFound
	}
	return
}

func (s *Store) SaveRemoteAsset(ctx context.Context, r domain.RemoteRun, a domain.NodeAsset) error {
	if a.TaskID != r.TaskID || a.NodeID != r.NodeID {
		return domain.ErrStateConflict
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO task_node_assets(task_id,node_id,content_index,asset_id,content_type,role,source_sha256,size_bytes,metadata_json,created_at,updated_at)
	 SELECT ?,?,?,?,?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM task_remote_runs WHERE task_id=? AND node_id=? AND lease_token=? AND lease_expires_at>? AND phase='preparing' AND cancel_state='none')
	 ON CONFLICT(task_id,node_id,content_index) DO UPDATE SET updated_at=excluded.updated_at
	 WHERE task_node_assets.asset_id=excluded.asset_id AND task_node_assets.content_type=excluded.content_type AND task_node_assets.role=excluded.role AND task_node_assets.source_sha256=excluded.source_sha256 AND task_node_assets.size_bytes=excluded.size_bytes AND task_node_assets.metadata_json=excluded.metadata_json`, a.TaskID, a.NodeID, a.ContentIndex, a.AssetID, a.ContentType, a.Role, a.SourceSHA256, a.SizeBytes, a.MetadataJSON, s.nowUnix(), s.nowUnix(), r.TaskID, r.NodeID, r.LeaseToken, s.nowUnix())
	return oneRow(res, err)
}

func (s *Store) CompleteRemoteRun(ctx context.Context, r domain.RemoteRun, c domain.RemoteCompletion) (err error) {
	if c.Status != "succeeded" && c.Status != "failed" && c.Status != "cancelled" {
		return domain.ErrStateConflict
	}
	if c.Status == "succeeded" && c.ResultURL == "" {
		return domain.ErrStateConflict
	}
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	now, ms := s.nowUnix(), s.nowMillis()
	res, err := conn.ExecContext(ctx, `UPDATE task_remote_runs SET phase='terminal',upstream_status=?,cancel_state=CASE WHEN ?='cancelled' THEN 'confirmed' WHEN cancel_state='requested' THEN 'rejected' ELSE cancel_state END,lease_token=NULL,lease_expires_at=NULL,next_poll_at=NULL,last_error_code=?,updated_at=? WHERE task_id=? AND node_id=? AND lease_token=? AND lease_expires_at>? AND phase<>'terminal'`, c.Status, c.Status, c.ErrorCode, now, r.TaskID, r.NodeID, r.LeaseToken, now)
	if err = oneRow(res, err); err != nil {
		return err
	}
	status, public := c.Status, c.ResultURL
	var finished any = now
	if c.UploadJob != nil && c.Status == "succeeded" {
		status, public, finished = "reconciling", "", nil
	}
	metadataStatus := c.MetadataStatus
	if metadataStatus == "" {
		metadataStatus = "none"
	}
	var feedback any
	if c.Feedback != nil {
		data, e := json.Marshal(c.Feedback)
		if e != nil {
			return e
		}
		feedback = string(data)
	}
	res, err = conn.ExecContext(ctx, `UPDATE video_tasks SET status=?,upstream_slot_active=0,result_internal_url=COALESCE(result_internal_url,NULLIF(?,'')),result_public_url=NULLIF(?,''),latest_result_url=NULLIF(?,''),latest_result_expires_at=NULLIF(?,0),result_metadata_json=NULLIF(?,''),metadata_status=?,ratio_actual=NULLIF(?,''),error_code=NULLIF(?,''),error_message=NULLIF(?,''),upstream_feedback_json=?,finished_at=?,updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND upstream_slot_active=1 AND deleted_at IS NULL AND status IN ('dispatching','running','reconciling','cancelling')`, status, c.ResultURL, public, c.ResultURL, c.ResultExpiresAt, c.MetadataJSON, metadataStatus, c.Ratio, c.ErrorCode, c.ErrorMessage, feedback, finished, now, r.TaskID, r.NodeID)
	if err = oneRow(res, err); err != nil {
		return err
	}
	if c.UploadJob != nil && c.Status == "succeeded" {
		if c.UploadJob.TaskID != r.TaskID {
			return domain.ErrStateConflict
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO result_upload_jobs(id,task_id,object_key,status,round_no,attempt_no,max_attempts,created_at,updated_at) VALUES(?,?,?,'pending',1,0,3,?,?)`, c.UploadJob.ID, r.TaskID, c.UploadJob.ObjectKey, ms, ms)
		return err
	}
	return createCallbackDeliveryWithConn(ctx, conn, r.TaskID, c.Status, ms)
}

func (s *Store) UpdateRemoteResult(ctx context.Context, taskID, nodeID string, u domain.RemoteResultUpdate) error {
	if u.MetadataStatus != "pending" && u.MetadataStatus != "ready" && u.MetadataStatus != "unavailable" {
		return domain.ErrStateConflict
	}
	res, err := s.db.ExecContext(ctx, `UPDATE video_tasks SET latest_result_url=COALESCE(NULLIF(?,''),latest_result_url),latest_result_expires_at=CASE WHEN ?<>'' THEN NULLIF(?,0) ELSE latest_result_expires_at END,result_metadata_json=COALESCE(NULLIF(?,''),result_metadata_json),metadata_status=CASE WHEN metadata_status='ready' THEN 'ready' ELSE ? END,updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND protocol_version='tk2sd-v1' AND status IN ('succeeded','reconciling') AND deleted_at IS NULL AND EXISTS(SELECT 1 FROM task_remote_runs r WHERE r.task_id=video_tasks.task_id AND r.node_id=? AND r.phase='terminal' AND r.upstream_status='succeeded')`, u.URL, u.URL, u.ExpiresAt, u.MetadataJSON, u.MetadataStatus, s.nowUnix(), taskID, nodeID, nodeID)
	return oneRow(res, err)
}
