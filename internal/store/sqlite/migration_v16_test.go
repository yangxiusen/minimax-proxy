package sqlite

import (
	"context"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

func TestMigrationV16AddsOfficialNodeAndDeliverySchema(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()

	var version int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 21 {
		t.Fatalf("user_version=%d, want 20", version)
	}

	legacy := modelNodeInput("legacy")
	created, err := store.CreateModelNode(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if created.MaxConcurrency != 1 || created.UpstreamModel != "" || created.ReplaceResultURL {
		t.Fatalf("legacy defaults = %+v", created)
	}

	official := domain.ModelNodeInput{
		ID: "official", ServiceURL: "https://api.example.com", ProtocolVersion: "minimax-v2",
		APIKeyCiphertext: []byte("cipher"), APIKeyNonce: []byte("nonce"), APIKeyFingerprint: "sha256:key",
		UpstreamModel: "MiniMax-H3", MaxConcurrency: 3, ReplaceResultURL: true,
		PollInterval: 3 * time.Second, RequestTimeout: 30 * time.Second, Enabled: true,
	}
	got, err := store.CreateModelNode(ctx, official)
	if err != nil {
		t.Fatal(err)
	}
	if got.UpstreamModel != "MiniMax-H3" || got.MaxConcurrency != 3 || !got.ReplaceResultURL {
		t.Fatalf("official node = %+v", got)
	}

	for _, table := range []string{"object_storage_configs", "result_upload_jobs"} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %s count=%d", table, count)
		}
	}
}

func TestMigrationV16ProtectsOriginalResultURL(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	if _, err := store.Create(ctx, task("immutable-url", "owner"), "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET result_internal_url='https://origin.example/video.mp4' WHERE task_id='immutable-url'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET result_internal_url='https://other.example/video.mp4' WHERE task_id='immutable-url'`); err == nil {
		t.Fatal("overwriting result_internal_url unexpectedly succeeded")
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET result_internal_url=NULL WHERE task_id='immutable-url'`); err == nil {
		t.Fatal("clearing result_internal_url unexpectedly succeeded")
	}
}
