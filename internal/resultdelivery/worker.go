package resultdelivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/logsafe"
	"minimax-h3-tc/internal/objectstore"
)

type Store interface {
	ClaimResultUploadJob(context.Context, string, time.Duration) (domain.ResultUploadJob, error)
	GetResultUploadSource(context.Context, string) (string, error)
	GetObjectStorageConfig(context.Context) (domain.ObjectStorageConfig, error)
	FailResultUploadAttempt(context.Context, string, string, string, string, bool, time.Time) error
	CompleteResultUpload(context.Context, string, string, string, int64) error
}

type SecretOpener interface {
	Open([]byte, []byte) (string, error)
}

type ObjectStoreFactory func(domain.ObjectStorageConfig, string, string) (objectstore.Store, error)

type VideoDownloader interface {
	Download(context.Context, string, string) (int64, error)
}

type Worker struct {
	Store              Store
	Secrets            SecretOpener
	Downloader         VideoDownloader
	ObjectStoreFactory ObjectStoreFactory
	LeaseDuration      time.Duration
	Interval           time.Duration
	Logger             *slog.Logger
	Now                func() time.Time
}

func (w Worker) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = time.Second
	}
	for {
		err := w.ProcessOne(ctx)
		if err != nil && !errors.Is(err, domain.ErrQueueEmpty) && !errors.Is(err, context.Canceled) {
			w.logger().ErrorContext(ctx, "结果上传处理失败", "stage", "result_delivery", "error_code", "result_delivery_worker_error", "error_reason", logsafe.Error(err))
		}
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return ctx.Err()
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (w Worker) ProcessOne(ctx context.Context) error {
	if w.Store == nil || w.Secrets == nil || w.Downloader == nil || w.ObjectStoreFactory == nil {
		return errors.New("结果上传工作器依赖未配置")
	}
	lease := fmt.Sprintf("upload-%d", w.now().UnixNano())
	duration := w.LeaseDuration
	if duration <= 0 {
		duration = 35 * time.Minute
	}
	job, err := w.Store.ClaimResultUploadJob(ctx, lease, duration)
	if err != nil {
		return err
	}
	sourceURL, err := w.Store.GetResultUploadSource(ctx, job.TaskID)
	if err != nil {
		return w.fail(ctx, job, lease, "result_source_missing", "原始结果地址不可用", false, err)
	}
	config, err := w.Store.GetObjectStorageConfig(ctx)
	if err != nil {
		return w.fail(ctx, job, lease, "object_storage_not_configured", "对象存储配置不可用", false, err)
	}
	operationCtx := ctx
	cancel := func() {}
	if config.RequestTimeout > 0 {
		operationCtx, cancel = context.WithTimeout(ctx, config.RequestTimeout)
	}
	defer cancel()
	publicKey, err := w.Secrets.Open(config.PublicKeyNonce, config.PublicKeyCiphertext)
	if err != nil {
		return w.fail(ctx, job, lease, "object_storage_secret_invalid", "对象存储密钥解密失败", false, err)
	}
	privateKey, err := w.Secrets.Open(config.PrivateKeyNonce, config.PrivateKeyCiphertext)
	if err != nil {
		return w.fail(ctx, job, lease, "object_storage_secret_invalid", "对象存储密钥解密失败", false, err)
	}
	store, err := w.ObjectStoreFactory(config, publicKey, privateKey)
	if err != nil {
		return w.failObjectStore(ctx, job, lease, err)
	}
	file, err := os.CreateTemp("", "minimax-h3-result-*.mp4")
	if err != nil {
		return w.fail(ctx, job, lease, "temporary_file_failed", "创建上传临时文件失败", true, err)
	}
	filePath := file.Name()
	_ = file.Close()
	defer os.Remove(filePath)
	bytesDownloaded, err := w.Downloader.Download(operationCtx, sourceURL, filePath)
	if err != nil {
		return w.fail(ctx, job, lease, "result_download_failed", "官方结果视频下载失败", retryableDownload(err), err)
	}
	publicURL, err := store.UploadFile(operationCtx, filePath, job.ObjectKey, "video/mp4")
	if err != nil {
		return w.failObjectStore(ctx, job, lease, err)
	}
	if err := w.Store.CompleteResultUpload(ctx, job.ID, lease, publicURL, bytesDownloaded); err != nil {
		return err
	}
	w.logger().InfoContext(ctx, "结果上传成功", "stage", "result_delivery", "task_id", job.TaskID, "bytes_uploaded", bytesDownloaded)
	return nil
}

func (w Worker) failObjectStore(ctx context.Context, job domain.ResultUploadJob, lease string, err error) error {
	var storageError *objectstore.Error
	if errors.As(err, &storageError) {
		return w.fail(ctx, job, lease, storageError.Code, storageError.Message, storageError.Retryable, err)
	}
	return w.fail(ctx, job, lease, "ucloud_upload_failed", "UCloud 上传失败", true, err)
}

func (w Worker) fail(ctx context.Context, job domain.ResultUploadJob, lease, code, message string, retryable bool, cause error) error {
	if ctx.Err() != nil {
		return cause
	}
	backoff := []time.Duration{time.Second, 5 * time.Second, 20 * time.Second}
	next := w.now().Add(backoff[min(job.AttemptNo-1, len(backoff)-1)])
	if err := w.Store.FailResultUploadAttempt(ctx, job.ID, lease, code, message, retryable, next); err != nil {
		return errors.Join(cause, err)
	}
	w.logger().WarnContext(ctx, "结果上传尝试失败", "stage", "result_delivery", "task_id", job.TaskID, "attempt", job.AttemptNo, "error_code", code, "error_reason", logsafe.Error(cause))
	return nil
}

func retryableDownload(err error) bool {
	var downloadError *DownloadError
	if errors.As(err, &downloadError) {
		return downloadError.Retryable
	}
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

func (w Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w Worker) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}
