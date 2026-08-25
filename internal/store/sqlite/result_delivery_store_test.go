package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

func TestObjectStorageConfigUsesOptimisticVersioning(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	store := newStore(t, Options{Now: func() time.Time { return now }})
	ctx := context.Background()
	input := objectStorageConfig()

	created, err := store.PutObjectStorageConfig(ctx, 0, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || created.LastTestStatus != "untested" {
		t.Fatalf("created=%+v", created)
	}
	input.BucketName = "updated-bucket"
	if _, err := store.PutObjectStorageConfig(ctx, 2, input); !errors.Is(err, domain.ErrObjectStorageVersionConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	updated, err := store.PutObjectStorageConfig(ctx, 1, input)
	if err != nil || updated.Version != 2 || updated.BucketName != "updated-bucket" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
}

func TestResultUploadJobLeaseFailureAndManualRetry(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100, Now: func() time.Time { return now }})
	ctx := context.Background()
	if _, err := store.Create(ctx, task("delivery-task", "owner"), "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET status='reconciling',result_internal_url='https://origin.example/video.mp4',delivery_required=1 WHERE task_id='delivery-task'`); err != nil {
		t.Fatal(err)
	}
	job := domain.ResultUploadJob{ID: "upload-1", TaskID: "delivery-task", ObjectKey: "MiniMax-H3/2033-05-18/delivery-task.mp4"}
	if err := store.CreateResultUploadJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimResultUploadJob(ctx, "lease-1", time.Minute)
	if err != nil || claimed.Status != domain.UploadUploading || claimed.AttemptNo != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if err := store.FailResultUploadAttempt(ctx, claimed.ID, "lease-1", "ucloud_error", "temporary", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryResultUpload(ctx, "delivery-task"); err != nil {
		t.Fatal(err)
	}
	retried, err := store.GetResultUploadJob(ctx, "delivery-task")
	if err != nil || retried.Status != domain.UploadPending || retried.RoundNo != 2 || retried.AttemptNo != 0 {
		t.Fatalf("retried=%+v err=%v", retried, err)
	}
}

func TestResultUploadJobStopsAfterThreeAutomaticAttempts(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100, Now: func() time.Time { return now }})
	ctx := context.Background()
	if _, err := store.Create(ctx, task("three-attempts", "owner"), "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET status='reconciling',result_internal_url='https://origin.example/video.mp4',delivery_required=1 WHERE task_id='three-attempts'`); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateResultUploadJob(ctx, domain.ResultUploadJob{ID: "upload-three", TaskID: "three-attempts", ObjectKey: "MiniMax-H3/2033-05-18/three-attempts.mp4"}); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		claimed, err := store.ClaimResultUploadJob(ctx, "lease-"+string(rune('0'+attempt)), time.Minute)
		if err != nil || claimed.AttemptNo != attempt {
			t.Fatalf("attempt=%d claimed=%+v err=%v", attempt, claimed, err)
		}
		if err := store.FailResultUploadAttempt(ctx, claimed.ID, claimed.LeaseToken, "ucloud_upload_failed", "UCloud 上传失败", true, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		now = now.Add(2 * time.Second)
	}
	job, err := store.GetResultUploadJob(ctx, "three-attempts")
	if err != nil || job.Status != domain.UploadFailed || job.AttemptNo != 3 || !job.NextAttemptAt.IsZero() {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if _, err := store.ClaimResultUploadJob(ctx, "lease-4", time.Minute); !errors.Is(err, domain.ErrQueueEmpty) {
		t.Fatalf("fourth automatic claim error=%v", err)
	}
}

func objectStorageConfig() domain.ObjectStorageConfig {
	return domain.ObjectStorageConfig{
		Provider: "ucloud-us3", BucketName: "video-bucket", FileHost: "cn.example.com", PublicBaseURL: "https://cdn.example.com",
		PublicKeyCiphertext: []byte("public-cipher"), PublicKeyNonce: []byte("public-nonce"), PublicKeyFingerprint: "sha256:public",
		PrivateKeyCiphertext: []byte("private-cipher"), PrivateKeyNonce: []byte("private-nonce"), PrivateKeyFingerprint: "sha256:private",
		RequestTimeout: 30 * time.Second,
	}
}
