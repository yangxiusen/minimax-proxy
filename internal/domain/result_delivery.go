package domain

import (
	"errors"
	"time"
)

var (
	ErrObjectStorageNotFound        = errors.New("对象存储配置不存在")
	ErrObjectStorageVersionConflict = errors.New("对象存储配置版本冲突")
	ErrResultUploadNotFound         = errors.New("结果上传作业不存在")
	ErrResultUploadNotRetryable     = errors.New("结果上传作业当前不可重试")
)

type ObjectStorageConfig struct {
	Provider, BucketName, FileHost, PublicBaseURL string
	PublicKeyCiphertext, PublicKeyNonce           []byte
	PublicKeyFingerprint                          string
	PrivateKeyCiphertext, PrivateKeyNonce         []byte
	PrivateKeyFingerprint                         string
	RequestTimeout                                time.Duration
	LastTestStatus                                string
	LastTestedAt                                  time.Time
	Version                                       int64
	CreatedAt, UpdatedAt                          time.Time
}

type UploadStatus string

const (
	UploadPending   UploadStatus = "pending"
	UploadUploading UploadStatus = "uploading"
	UploadRetryWait UploadStatus = "retry_wait"
	UploadSucceeded UploadStatus = "succeeded"
	UploadFailed    UploadStatus = "failed"
)

type ResultUploadJob struct {
	ID, TaskID, ObjectKey                       string
	Status                                      UploadStatus
	RoundNo, AttemptNo, MaxAttempts             int
	NextAttemptAt                               time.Time
	LeaseToken                                  string
	LeaseExpiresAt                              time.Time
	PublicURL, LastErrorCode, LastErrorMessage  string
	BytesUploaded                               int64
	StartedAt, FinishedAt, CreatedAt, UpdatedAt time.Time
	Version                                     int64
}

func (job ResultUploadJob) CanRetry() bool {
	return job.Status == UploadFailed
}
