package domain

import "testing"

func TestUploadJobRetryability(t *testing.T) {
	job := ResultUploadJob{Status: UploadFailed, AttemptNo: 3, MaxAttempts: 3}
	if !job.CanRetry() {
		t.Fatal("failed upload job should allow a new manual round")
	}
	job.Status = UploadUploading
	if job.CanRetry() {
		t.Fatal("active upload job must not allow a new manual round")
	}
}
