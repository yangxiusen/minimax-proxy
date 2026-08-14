package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

func TestCleanupNodeProgressUsesDistinctJSONFields(t *testing.T) {
	payload, err := json.Marshal(CleanupNodeProgress{
		NodeID: "node-1", Count: 6, Pending: 1, Deleting: 1, Succeeded: 2,
		Failed: 1, Skipped: 1, Bytes: 100, DeletedBytes: 40,
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"node_id", "count", "pending", "deleting", "succeeded", "failed", "skipped", "bytes", "deleted_bytes"} {
		if _, ok := decoded[field]; !ok {
			t.Fatalf("missing %s in %s", field, payload)
		}
	}
}

func TestCleanupPreviewDoesNotMutateAndConfirmCreatesDeletionItems(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	store := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 100, Now: func() time.Time { return now }})
	ctx := context.Background()
	insertNodeAPINode(t, store, "cleanup-node")
	created, err := store.Create(ctx, domain.NewTask{TaskID: "cleanup-task", APIKeyID: "owner", Model: "MiniMax-H3", Scenario: "t2va", RequestJSON: `{}`, RequestHash: "hash", Resolution: "2K", Duration: 5, Ratio: "16:9"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	old := now.Add(-72 * time.Hour).UnixMilli()
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET status='succeeded',finished_at=? WHERE task_id=?`, old/1000, created.TaskID); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateArtifact(ctx, TaskArtifact{ID: "cleanup-artifact", TaskID: created.TaskID, Kind: "final_video", SizeBytes: 9, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", CreatedAt: old, ExpiresAt: now.Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateArtifactLocation(ctx, ArtifactLocation{ID: "cleanup-location", ArtifactID: "cleanup-artifact", NodeID: "cleanup-node", NodeArtifactID: "node-artifact", State: "active", IsPrimary: true, SizeBytes: 9, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", CreatedAt: old}); err != nil {
		t.Fatal(err)
	}

	preview, err := store.PreviewArtifactCleanup(ctx, 2, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if preview.CandidateCount != 1 || preview.CandidateBytes != 9 {
		t.Fatalf("preview=%+v", preview)
	}
	if _, err := store.Get(ctx, "owner", created.TaskID); err != nil {
		t.Fatalf("preview mutated task: %v", err)
	}
	job, err := store.ConfirmArtifactCleanup(ctx, preview.Token, "DELETE 1 ARTIFACTS")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "pending" || job.TotalCount != 1 {
		t.Fatalf("job=%+v", job)
	}
	if _, err := store.Get(ctx, "owner", created.TaskID); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Fatalf("confirmed task remains visible: %v", err)
	}
	items, err := store.ListArtifactCleanupItems(ctx, job.ID, "", "", 50)
	if err != nil || len(items) != 1 || items[0].NodeID != "cleanup-node" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
}

func TestCleanupConfirmRejectsChangedCandidateSet(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	store := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 100, Now: func() time.Time { return now }})
	ctx := context.Background()
	preview, err := store.PreviewArtifactCleanup(ctx, 2, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE artifact_deletion_jobs SET total_count=1 WHERE id=?`, preview.Token[:36]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmArtifactCleanup(ctx, preview.Token, "DELETE 1 ARTIFACTS"); !errors.Is(err, ErrCleanupPreviewStale) {
		t.Fatalf("confirm error=%v", err)
	}
}
