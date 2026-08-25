package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"minimax-h3-tc/internal/domain"
)

const objectStorageSelect = `SELECT provider,bucket_name,file_host,public_base_url,public_key_ciphertext,public_key_nonce,public_key_fingerprint,private_key_ciphertext,private_key_nonce,private_key_fingerprint,request_timeout_ms,last_test_status,COALESCE(last_tested_at,0),version,created_at,updated_at FROM object_storage_configs WHERE id=1`

func (s *Store) GetObjectStorageConfig(ctx context.Context) (domain.ObjectStorageConfig, error) {
	config, err := scanObjectStorageConfig(s.db.QueryRowContext(ctx, objectStorageSelect))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ObjectStorageConfig{}, domain.ErrObjectStorageNotFound
	}
	return config, err
}

func (s *Store) PutObjectStorageConfig(ctx context.Context, expectedVersion int64, input domain.ObjectStorageConfig) (result domain.ObjectStorageConfig, err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return result, err
	}
	defer completeTransaction(finish, &err)
	now := s.nowMillis()
	if expectedVersion == 0 {
		_, err = conn.ExecContext(ctx, `INSERT INTO object_storage_configs(id,provider,bucket_name,file_host,public_base_url,public_key_ciphertext,public_key_nonce,public_key_fingerprint,private_key_ciphertext,private_key_nonce,private_key_fingerprint,request_timeout_ms,last_test_status,version,created_at,updated_at) VALUES(1,?,?,?,?,?,?,?,?,?,?,?,'untested',1,?,?)`,
			input.Provider, input.BucketName, input.FileHost, input.PublicBaseURL,
			input.PublicKeyCiphertext, input.PublicKeyNonce, input.PublicKeyFingerprint,
			input.PrivateKeyCiphertext, input.PrivateKeyNonce, input.PrivateKeyFingerprint,
			input.RequestTimeout.Milliseconds(), now, now)
		if err != nil {
			var count int
			if queryErr := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM object_storage_configs WHERE id=1`).Scan(&count); queryErr == nil && count == 1 {
				return result, domain.ErrObjectStorageVersionConflict
			}
			return result, err
		}
	} else {
		updated, execErr := conn.ExecContext(ctx, `UPDATE object_storage_configs SET provider=?,bucket_name=?,file_host=?,public_base_url=?,public_key_ciphertext=?,public_key_nonce=?,public_key_fingerprint=?,private_key_ciphertext=?,private_key_nonce=?,private_key_fingerprint=?,request_timeout_ms=?,last_test_status='untested',last_tested_at=NULL,version=version+1,updated_at=? WHERE id=1 AND version=?`,
			input.Provider, input.BucketName, input.FileHost, input.PublicBaseURL,
			input.PublicKeyCiphertext, input.PublicKeyNonce, input.PublicKeyFingerprint,
			input.PrivateKeyCiphertext, input.PrivateKeyNonce, input.PrivateKeyFingerprint,
			input.RequestTimeout.Milliseconds(), now, expectedVersion)
		if err := oneRow(updated, execErr); err != nil {
			return result, domain.ErrObjectStorageVersionConflict
		}
	}
	return scanObjectStorageConfig(conn.QueryRowContext(ctx, objectStorageSelect))
}

func (s *Store) MarkObjectStorageTest(ctx context.Context, expectedVersion int64, passed bool) error {
	status := "failed"
	if passed {
		status = "passed"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE object_storage_configs SET last_test_status=?,last_tested_at=?,updated_at=? WHERE id=1 AND version=?`, status, s.nowMillis(), s.nowMillis(), expectedVersion)
	if err := oneRow(result, err); err != nil {
		return domain.ErrObjectStorageVersionConflict
	}
	return nil
}

func scanObjectStorageConfig(scanner rowScanner) (domain.ObjectStorageConfig, error) {
	var config domain.ObjectStorageConfig
	var timeoutMS, testedAt, createdAt, updatedAt int64
	err := scanner.Scan(&config.Provider, &config.BucketName, &config.FileHost, &config.PublicBaseURL,
		&config.PublicKeyCiphertext, &config.PublicKeyNonce, &config.PublicKeyFingerprint,
		&config.PrivateKeyCiphertext, &config.PrivateKeyNonce, &config.PrivateKeyFingerprint,
		&timeoutMS, &config.LastTestStatus, &testedAt, &config.Version, &createdAt, &updatedAt)
	if err != nil {
		return domain.ObjectStorageConfig{}, err
	}
	config.RequestTimeout = time.Duration(timeoutMS) * time.Millisecond
	config.LastTestedAt = optionalUnixMillis(testedAt)
	config.CreatedAt = unixMillis(createdAt)
	config.UpdatedAt = unixMillis(updatedAt)
	return config, nil
}

const uploadJobSelect = `SELECT id,task_id,object_key,status,round_no,attempt_no,max_attempts,COALESCE(next_attempt_at,0),COALESCE(lease_token,''),COALESCE(lease_expires_at,0),public_url,last_error_code,last_error_message,bytes_uploaded,COALESCE(started_at,0),COALESCE(finished_at,0),created_at,updated_at,version FROM result_upload_jobs`

func (s *Store) CreateResultUploadJob(ctx context.Context, job domain.ResultUploadJob) error {
	now := s.nowMillis()
	_, err := s.db.ExecContext(ctx, `INSERT INTO result_upload_jobs(id,task_id,object_key,status,round_no,attempt_no,max_attempts,created_at,updated_at) VALUES(?,?,?,'pending',1,0,3,?,?)`, job.ID, job.TaskID, job.ObjectKey, now, now)
	return err
}

func (s *Store) GetResultUploadJob(ctx context.Context, taskID string) (domain.ResultUploadJob, error) {
	job, err := scanResultUploadJob(s.db.QueryRowContext(ctx, uploadJobSelect+` WHERE task_id=?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ResultUploadJob{}, domain.ErrResultUploadNotFound
	}
	return job, err
}

func (s *Store) GetResultUploadSource(ctx context.Context, taskID string) (string, error) {
	var result string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(result_internal_url,'') FROM video_tasks WHERE task_id=? AND deleted_at IS NULL`, taskID).Scan(&result)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.ErrTaskNotFound
	}
	if err != nil {
		return "", err
	}
	if result == "" {
		return "", domain.ErrStateConflict
	}
	return result, nil
}

func (s *Store) ClaimResultUploadJob(ctx context.Context, leaseToken string, leaseDuration time.Duration) (result domain.ResultUploadJob, err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return result, err
	}
	defer completeTransaction(finish, &err)
	now := s.nowMillis()
	job, err := scanResultUploadJob(conn.QueryRowContext(ctx, uploadJobSelect+` WHERE (status='pending' OR (status='retry_wait' AND COALESCE(next_attempt_at,0)<=?) OR (status='uploading' AND COALESCE(lease_expires_at,0)<=?)) ORDER BY created_at,id LIMIT 1`, now, now))
	if errors.Is(err, sql.ErrNoRows) {
		return result, domain.ErrQueueEmpty
	}
	if err != nil {
		return result, err
	}
	updated, err := conn.ExecContext(ctx, `UPDATE result_upload_jobs SET status='uploading',attempt_no=attempt_no+1,lease_token=?,lease_expires_at=?,next_attempt_at=NULL,started_at=COALESCE(started_at,?),updated_at=?,version=version+1 WHERE id=? AND version=?`, leaseToken, now+leaseDuration.Milliseconds(), now, now, job.ID, job.Version)
	if err := oneRow(updated, err); err != nil {
		return result, err
	}
	return scanResultUploadJob(conn.QueryRowContext(ctx, uploadJobSelect+` WHERE id=?`, job.ID))
}

func (s *Store) FailResultUploadAttempt(ctx context.Context, jobID, leaseToken, code, message string, retryable bool, nextAttempt time.Time) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	job, err := scanResultUploadJob(conn.QueryRowContext(ctx, uploadJobSelect+` WHERE id=? AND status='uploading' AND lease_token=?`, jobID, leaseToken))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrStateConflict
	}
	if err != nil {
		return err
	}
	status := domain.UploadFailed
	nextAt := any(nil)
	if retryable && job.AttemptNo < job.MaxAttempts {
		status = domain.UploadRetryWait
		nextAt = nextAttempt.UnixMilli()
	}
	now := s.nowMillis()
	if _, err := conn.ExecContext(ctx, `UPDATE result_upload_jobs SET status=?,next_attempt_at=?,lease_token=NULL,lease_expires_at=NULL,last_error_code=?,last_error_message=?,updated_at=?,version=version+1 WHERE id=?`, status, nextAt, code, message, now, jobID); err != nil {
		return err
	}
	if status == domain.UploadFailed {
		if _, err = conn.ExecContext(ctx, `UPDATE video_tasks SET status='failed',error_code='result_upload_failed',error_message=?,finished_at=?,updated_at=?,version=version+1 WHERE task_id=? AND status='reconciling'`, message, now/1000, now/1000, job.TaskID); err != nil {
			return err
		}
		return createCallbackDeliveryWithConn(ctx, conn, job.TaskID, "failed", now)
	}
	return err
}

func (s *Store) RetryResultUpload(ctx context.Context, taskID string) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	now := s.nowMillis()
	updated, err := conn.ExecContext(ctx, `UPDATE result_upload_jobs SET status='pending',round_no=round_no+1,attempt_no=0,next_attempt_at=NULL,lease_token=NULL,lease_expires_at=NULL,last_error_code='',last_error_message='',started_at=NULL,finished_at=NULL,updated_at=?,version=version+1 WHERE task_id=? AND status='failed'`, now, taskID)
	if err := oneRow(updated, err); err != nil {
		return domain.ErrResultUploadNotRetryable
	}
	updated, err = conn.ExecContext(ctx, `UPDATE video_tasks SET status='reconciling',error_code=NULL,error_message=NULL,finished_at=NULL,updated_at=?,version=version+1 WHERE task_id=? AND status='failed' AND error_code='result_upload_failed' AND result_internal_url IS NOT NULL`, now/1000, taskID)
	if err := oneRow(updated, err); err != nil {
		return domain.ErrResultUploadNotRetryable
	}
	return nil
}

func (s *Store) CompleteResultUpload(ctx context.Context, jobID, leaseToken, publicURL string, bytesUploaded int64) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	job, err := scanResultUploadJob(conn.QueryRowContext(ctx, uploadJobSelect+` WHERE id=? AND status='uploading' AND lease_token=?`, jobID, leaseToken))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrStateConflict
	}
	if err != nil {
		return err
	}
	nowMS, now := s.nowMillis(), s.nowUnix()
	if _, err := conn.ExecContext(ctx, `UPDATE result_upload_jobs SET status='succeeded',public_url=?,bytes_uploaded=?,lease_token=NULL,lease_expires_at=NULL,finished_at=?,updated_at=?,version=version+1 WHERE id=?`, publicURL, bytesUploaded, nowMS, nowMS, jobID); err != nil {
		return err
	}
	updated, err := conn.ExecContext(ctx, `UPDATE video_tasks SET status='succeeded',result_public_url=?,error_code=NULL,error_message=NULL,finished_at=?,updated_at=?,version=version+1 WHERE task_id=? AND status='reconciling' AND result_internal_url IS NOT NULL`, publicURL, now, now, job.TaskID)
	if err := oneRow(updated, err); err != nil {
		return err
	}
	return createCallbackDeliveryWithConn(ctx, conn, job.TaskID, "succeeded", nowMS)
}

func scanResultUploadJob(scanner rowScanner) (domain.ResultUploadJob, error) {
	var job domain.ResultUploadJob
	var status string
	var nextAt, leaseExpires, startedAt, finishedAt, createdAt, updatedAt int64
	err := scanner.Scan(&job.ID, &job.TaskID, &job.ObjectKey, &status, &job.RoundNo, &job.AttemptNo, &job.MaxAttempts,
		&nextAt, &job.LeaseToken, &leaseExpires, &job.PublicURL, &job.LastErrorCode, &job.LastErrorMessage,
		&job.BytesUploaded, &startedAt, &finishedAt, &createdAt, &updatedAt, &job.Version)
	if err != nil {
		return domain.ResultUploadJob{}, err
	}
	job.Status = domain.UploadStatus(status)
	job.NextAttemptAt = optionalUnixMillis(nextAt)
	job.LeaseExpiresAt = optionalUnixMillis(leaseExpires)
	job.StartedAt = optionalUnixMillis(startedAt)
	job.FinishedAt = optionalUnixMillis(finishedAt)
	job.CreatedAt = unixMillis(createdAt)
	job.UpdatedAt = unixMillis(updatedAt)
	return job, nil
}

func optionalUnixMillis(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return unixMillis(value)
}
