package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrNoCallbackDelivery = errors.New("没有待投递 callback")

type CallbackDelivery struct {
	ID                    string
	TaskID                string
	ExternalStatus        string
	StateVersion          int64
	Status                string
	AttemptCount          int
	NextAttemptAt         int64
	RequestBodyHash       string
	RequestBody           []byte
	CallbackURLCiphertext []byte
	CallbackURLNonce      []byte
	APIKeyID              string
	HTTPStatus            int
	LastError             string
	CreatedAt             int64
	UpdatedAt             int64
	DeliveredAt           int64
}

func (s *Store) CreateCallbackDelivery(ctx context.Context, delivery CallbackDelivery) error {
	if delivery.Status == "" {
		delivery.Status = "pending"
	}
	if delivery.CreatedAt == 0 {
		delivery.CreatedAt = s.nowMillis()
	}
	if delivery.UpdatedAt == 0 {
		delivery.UpdatedAt = delivery.CreatedAt
	}
	if delivery.RequestBody == nil {
		delivery.RequestBody = []byte{}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO callback_deliveries(id,task_id,external_status,state_version,status,attempt_count,next_attempt_at,request_body_hash,request_body,http_status,last_error,created_at,updated_at,delivered_at) VALUES(?,?,?,?,?,?,NULLIF(?,0),?,?,NULLIF(?,0),NULLIF(?,''),?,?,NULLIF(?,0))`, delivery.ID, delivery.TaskID, delivery.ExternalStatus, delivery.StateVersion, delivery.Status, delivery.AttemptCount, delivery.NextAttemptAt, delivery.RequestBodyHash, delivery.RequestBody, delivery.HTTPStatus, delivery.LastError, delivery.CreatedAt, delivery.UpdatedAt, delivery.DeliveredAt)
	return err
}

func (s *Store) CreateTaskCallbackDelivery(ctx context.Context, taskID, externalStatus string, stateVersion int64, content json.RawMessage) error {
	if taskID == "" || stateVersion < 1 {
		return errors.New("callback 状态事件无效")
	}
	var callbackConfigured int
	if err := s.db.QueryRowContext(ctx, `SELECT CASE WHEN callback_url_ciphertext IS NOT NULL THEN 1 ELSE 0 END FROM video_tasks WHERE task_id=?`, taskID).Scan(&callbackConfigured); err != nil {
		return err
	}
	if callbackConfigured == 0 {
		return nil
	}
	payload := struct {
		TaskID  string          `json:"task_id"`
		Status  string          `json:"status"`
		Content json.RawMessage `json:"content,omitempty"`
	}{TaskID: taskID, Status: externalStatus, Content: content}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	return s.CreateCallbackDelivery(ctx, CallbackDelivery{
		ID: uuid.NewString(), TaskID: taskID, ExternalStatus: externalStatus, StateVersion: stateVersion,
		RequestBody: body, RequestBodyHash: hex.EncodeToString(digest[:]),
	})
}

func createCallbackDeliveryWithConn(ctx context.Context, conn *sql.Conn, taskID, externalStatus string, now int64) error {
	var callbackConfigured int
	var stateVersion int64
	if err := conn.QueryRowContext(ctx, `SELECT CASE WHEN callback_url_ciphertext IS NOT NULL THEN 1 ELSE 0 END,version FROM video_tasks WHERE task_id=?`, taskID).Scan(&callbackConfigured, &stateVersion); err != nil {
		return err
	}
	if callbackConfigured == 0 {
		return nil
	}
	body, err := json.Marshal(struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}{TaskID: taskID, Status: externalStatus})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	_, err = conn.ExecContext(ctx, `INSERT INTO callback_deliveries(id,task_id,external_status,state_version,status,request_body_hash,request_body,created_at,updated_at) VALUES(?,?,?,?,'pending',?,?,?,?) ON CONFLICT(task_id,state_version) DO NOTHING`, uuid.NewString(), taskID, externalStatus, stateVersion, hex.EncodeToString(digest[:]), body, now, now)
	return err
}

func (s *Store) ClaimCallbackDelivery(ctx context.Context, now, leaseUntil time.Time) (delivery CallbackDelivery, err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return delivery, err
	}
	defer completeTransaction(finish, &err)
	nowMS := now.UTC().UnixMilli()
	err = conn.QueryRowContext(ctx, `SELECT d.id,d.task_id,d.external_status,d.state_version,d.status,d.attempt_count,COALESCE(d.next_attempt_at,0),d.request_body_hash,d.request_body,COALESCE(d.http_status,0),COALESCE(d.last_error,''),d.created_at,d.updated_at,COALESCE(d.delivered_at,0),t.callback_url_ciphertext,t.callback_url_nonce,t.api_key_id FROM callback_deliveries d JOIN video_tasks t ON t.task_id=d.task_id WHERE t.callback_url_ciphertext IS NOT NULL AND d.status IN ('pending','retry_wait','sending') AND (d.next_attempt_at IS NULL OR d.next_attempt_at<=?) AND (d.lease_expires_at IS NULL OR d.lease_expires_at<=?) ORDER BY d.created_at,d.id LIMIT 1`, nowMS, nowMS).Scan(
		&delivery.ID, &delivery.TaskID, &delivery.ExternalStatus, &delivery.StateVersion, &delivery.Status, &delivery.AttemptCount,
		&delivery.NextAttemptAt, &delivery.RequestBodyHash, &delivery.RequestBody, &delivery.HTTPStatus, &delivery.LastError,
		&delivery.CreatedAt, &delivery.UpdatedAt, &delivery.DeliveredAt, &delivery.CallbackURLCiphertext, &delivery.CallbackURLNonce, &delivery.APIKeyID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CallbackDelivery{}, ErrNoCallbackDelivery
	}
	if err != nil {
		return CallbackDelivery{}, err
	}
	result, err := conn.ExecContext(ctx, `UPDATE callback_deliveries SET status='sending',lease_expires_at=?,updated_at=? WHERE id=? AND (lease_expires_at IS NULL OR lease_expires_at<=?)`, leaseUntil.UTC().UnixMilli(), nowMS, delivery.ID, nowMS)
	if err := oneRow(result, err); err != nil {
		return CallbackDelivery{}, ErrNoCallbackDelivery
	}
	return delivery, nil
}

func (s *Store) MarkCallbackSucceeded(ctx context.Context, id string, httpStatus int, deliveredAt time.Time) error {
	now := deliveredAt.UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx, `UPDATE callback_deliveries SET status='succeeded',attempt_count=attempt_count+1,http_status=?,last_error=NULL,next_attempt_at=NULL,lease_expires_at=NULL,delivered_at=?,updated_at=? WHERE id=? AND status='sending'`, httpStatus, now, now, id)
	return oneRow(result, err)
}

func (s *Store) ScheduleCallbackRetry(ctx context.Context, id string, attempt, httpStatus int, message string, nextAttempt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE callback_deliveries SET status='retry_wait',attempt_count=?,http_status=NULLIF(?,0),last_error=?,next_attempt_at=?,lease_expires_at=NULL,updated_at=? WHERE id=? AND status='sending'`, attempt, httpStatus, message, nextAttempt.UTC().UnixMilli(), s.nowMillis(), id)
	return oneRow(result, err)
}

func (s *Store) MarkCallbackFailed(ctx context.Context, id string, attempt, httpStatus int, message string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE callback_deliveries SET status='failed',attempt_count=?,http_status=NULLIF(?,0),last_error=?,next_attempt_at=NULL,lease_expires_at=NULL,updated_at=? WHERE id=? AND status='sending'`, attempt, httpStatus, message, s.nowMillis(), id)
	return oneRow(result, err)
}
