package resultdelivery

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/objectstore"
	"minimax-h3-tc/internal/upstream/tk2sd"
)

func TestWorkerCompletesUploadWithPublicURL(t *testing.T) {
	store := &deliveryStoreFake{
		job:    domain.ResultUploadJob{ID: "job-1", TaskID: "task-1", ObjectKey: "MiniMax-H3/2033-05-18/task-1.mp4", AttemptNo: 1},
		config: domain.ObjectStorageConfig{PublicKeyNonce: []byte("public"), PrivateKeyNonce: []byte("private"), RequestTimeout: time.Minute},
	}
	worker := Worker{
		Store: store, Secrets: secretFake{}, Downloader: downloadFake{},
		ObjectStoreFactory: func(domain.ObjectStorageConfig, string, string) (objectstore.Store, error) { return uploadFake{}, nil },
		Now:                func() time.Time { return time.Date(2033, 5, 18, 0, 0, 0, 0, time.UTC) },
	}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.completedURL != "https://cdn.example.com/MiniMax-H3/2033-05-18/task-1.mp4" || store.completedBytes != 5 {
		t.Fatalf("url=%q bytes=%d", store.completedURL, store.completedBytes)
	}
}

func TestWorkerPersistsRetryableUCloudFailure(t *testing.T) {
	store := &deliveryStoreFake{job: domain.ResultUploadJob{ID: "job-1", TaskID: "task-1", AttemptNo: 1}, config: domain.ObjectStorageConfig{RequestTimeout: time.Minute}}
	worker := Worker{
		Store: store, Secrets: secretFake{}, Downloader: downloadFake{},
		ObjectStoreFactory: func(domain.ObjectStorageConfig, string, string) (objectstore.Store, error) {
			return uploadFake{err: &objectstore.Error{Code: "ucloud_upload_failed", Message: "UCloud 上传失败", Retryable: true}}, nil
		},
		Now: func() time.Time { return time.Date(2033, 5, 18, 0, 0, 0, 0, time.UTC) },
	}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.failedCode != "ucloud_upload_failed" || !store.retryable || !store.next.Equal(worker.now().Add(time.Second)) {
		t.Fatalf("code=%q retryable=%v next=%s", store.failedCode, store.retryable, store.next)
	}
}

func TestWorkerLogsDoNotExposeSourceURL(t *testing.T) {
	var logs bytes.Buffer
	store := &deliveryStoreFake{job: domain.ResultUploadJob{ID: "job-1", TaskID: "task-1", AttemptNo: 1}, config: domain.ObjectStorageConfig{RequestTimeout: time.Minute}}
	worker := Worker{
		Store: store, Secrets: secretFake{}, Downloader: downloadFake{err: errors.New("GET https://origin.example/video.mp4?token=private-token failed")},
		ObjectStoreFactory: func(domain.ObjectStorageConfig, string, string) (objectstore.Store, error) { return uploadFake{}, nil },
		Logger:             slog.New(slog.NewJSONHandler(&logs, nil)),
	}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	output := logs.String()
	if strings.Contains(output, "origin.example") || strings.Contains(output, "private-token") || !strings.Contains(output, "[redacted-url]") {
		t.Fatalf("unsafe log output: %s", output)
	}
}

func TestWorkerRetriesTemporaryNodeDownloadAndLogsStages(t *testing.T) {
	var logs bytes.Buffer
	store := &deliveryStoreFake{job: domain.ResultUploadJob{ID: "job-1", TaskID: "task-1", RoundNo: 1, AttemptNo: 1, MaxAttempts: 3}, config: domain.ObjectStorageConfig{RequestTimeout: time.Minute}}
	worker := Worker{
		Store: store, Secrets: secretFake{}, Downloader: downloadFake{err: &tk2sd.HTTPError{StatusCode: 503, Class: tk2sd.ErrorTemporary}},
		ObjectStoreFactory: func(domain.ObjectStorageConfig, string, string) (objectstore.Store, error) { return uploadFake{}, nil },
		Logger:             slog.New(slog.NewJSONHandler(&logs, nil)),
		Now:                func() time.Time { return time.Date(2033, 5, 18, 0, 0, 0, 0, time.UTC) },
	}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !store.retryable || store.failedCode != "result_download_failed" || !store.next.Equal(worker.now().Add(time.Second)) {
		t.Fatalf("retryable=%v code=%q next=%s", store.retryable, store.failedCode, store.next)
	}
	for _, text := range []string{"开始下载结果视频", "结果视频下载失败", `"http_status":503`, `"automatic_retry":true`, `"task_id":"task-1"`} {
		if !strings.Contains(logs.String(), text) {
			t.Fatalf("missing %s: %s", text, logs.String())
		}
	}
}

func TestWorkerDoesNotRetryPermanentNodeDownload(t *testing.T) {
	for _, err := range []error{&tk2sd.HTTPError{StatusCode: 403, Class: tk2sd.ErrorAuthentication}, tk2sd.ErrInvalidResponse} {
		if retryableDownload(err) {
			t.Fatalf("permanent error marked retryable: %v", err)
		}
	}
	for _, err := range []error{&tk2sd.HTTPError{StatusCode: 409, Class: tk2sd.ErrorConflict}, &tk2sd.HTTPError{Class: tk2sd.ErrorTransport}, tk2sd.ErrIncompleteVideo, context.DeadlineExceeded} {
		if !retryableDownload(err) {
			t.Fatalf("temporary error not retryable: %v", err)
		}
	}
}

type deliveryStoreFake struct {
	job                      domain.ResultUploadJob
	config                   domain.ObjectStorageConfig
	completedURL, failedCode string
	completedBytes           int64
	retryable                bool
	next                     time.Time
}

func (s *deliveryStoreFake) ClaimResultUploadJob(context.Context, string, time.Duration) (domain.ResultUploadJob, error) {
	return s.job, nil
}
func (s *deliveryStoreFake) GetResultUploadSource(context.Context, string) (string, error) {
	return "https://origin.example/video.mp4", nil
}
func (s *deliveryStoreFake) GetObjectStorageConfig(context.Context) (domain.ObjectStorageConfig, error) {
	return s.config, nil
}
func (s *deliveryStoreFake) FailResultUploadAttempt(_ context.Context, _, _, code, _ string, retryable bool, next time.Time) error {
	s.failedCode, s.retryable, s.next = code, retryable, next
	return nil
}
func (s *deliveryStoreFake) CompleteResultUpload(_ context.Context, _, _, publicURL string, bytes int64) error {
	s.completedURL, s.completedBytes = publicURL, bytes
	return nil
}

type secretFake struct{}

func (secretFake) Open(nonce, _ []byte) (string, error) { return string(nonce), nil }

type downloadFake struct{ err error }

func (f downloadFake) Download(_ context.Context, _ string, destination string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	if err := os.WriteFile(destination, []byte("video"), 0o600); err != nil {
		return 0, err
	}
	return 5, nil
}

type uploadFake struct{ err error }

func (f uploadFake) UploadFile(_ context.Context, _ string, key, _ string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if key == "" {
		return "", errors.New("missing key")
	}
	return "https://cdn.example.com/" + key, nil
}
