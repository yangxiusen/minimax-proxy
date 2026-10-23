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

func TestExpiredFinalResultUploadCannotBlockQueue(t *testing.T) {
	for _, follower := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty_after_recovery", true: "next_job_claimed"}[follower], func(t *testing.T) {
			now := time.Unix(2_000_000_000, 0).UTC()
			store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100, Now: func() time.Time { return now }})
			ctx := context.Background()
			createDeliveryFixture(t, store, "expired-final", "upload-expired")
			_, err := store.db.Exec(`UPDATE result_upload_jobs SET status='uploading',attempt_no=3,lease_token='old',lease_expires_at=?,last_error_code='ucloud_upload_failed',last_error_message='UCloud 上传失败' WHERE id='upload-expired'`, now.Add(-time.Second).UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			if follower {
				now = now.Add(time.Millisecond)
				createDeliveryFixture(t, store, "next-delivery", "upload-next")
			}
			claimed, err := store.ClaimResultUploadJob(ctx, "new-worker", time.Minute)
			if follower {
				if err != nil || claimed.ID != "upload-next" || claimed.AttemptNo != 1 {
					t.Fatalf("queue blocked: %+v %v", claimed, err)
				}
			} else if !errors.Is(err, domain.ErrQueueEmpty) {
				t.Fatalf("expected empty queue after recovery: %v", err)
			}
			expired, err := store.GetResultUploadJob(ctx, "expired-final")
			if err != nil || expired.Status != domain.UploadFailed || expired.AttemptNo != 3 || expired.LeaseToken != "" {
				t.Fatalf("exhausted job not finalized: %+v %v", expired, err)
			}
			main, err := store.Get(ctx, "owner", "expired-final")
			if err != nil || main.Status != domain.StatusFailed || main.ErrorCode != "result_upload_failed" {
				t.Fatalf("main task not failed: %+v %v", main, err)
			}
			if err := store.RetryResultUpload(ctx, "expired-final"); err != nil {
				t.Fatal("manual retry blocked", err)
			}
			retried, err := store.GetResultUploadJob(ctx, "expired-final")
			if err != nil || retried.Status != domain.UploadPending || retried.RoundNo != 2 || retried.AttemptNo != 0 {
				t.Fatalf("manual retry: %+v %v", retried, err)
			}
		})
	}
}

func TestResultUploadRecoveryPreservesLiveFinalLease(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100, Now: func() time.Time { return now }})
	createDeliveryFixture(t, store, "live-final", "upload-live")
	if _, err := store.db.Exec(`UPDATE result_upload_jobs SET status='uploading',attempt_no=3,lease_token='live',lease_expires_at=? WHERE id='upload-live'`, now.Add(time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimResultUploadJob(t.Context(), "other", time.Minute); !errors.Is(err, domain.ErrQueueEmpty) {
		t.Fatal(err)
	}
	job, err := store.GetResultUploadJob(t.Context(), "live-final")
	if err != nil || job.Status != domain.UploadUploading || job.LeaseToken != "live" || job.AttemptNo != 3 {
		t.Fatalf("live attempt interrupted: %+v %v", job, err)
	}
	if err := store.CompleteResultUpload(t.Context(), job.ID, "live", "https://cdn.example/video", 5); err != nil {
		t.Fatal(err)
	}
}

func createDeliveryFixture(t *testing.T, store *Store, taskID, jobID string) {
	t.Helper()
	if _, err := store.Create(t.Context(), task(taskID, "owner"), "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE video_tasks SET status='reconciling',result_internal_url='https://origin.example/video',delivery_required=1 WHERE task_id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateResultUploadJob(t.Context(), domain.ResultUploadJob{ID: jobID, TaskID: taskID, ObjectKey: taskID + ".mp4"}); err != nil {
		t.Fatal(err)
	}
}
