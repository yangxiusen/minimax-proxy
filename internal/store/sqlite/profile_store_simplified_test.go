package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"minimax-h3-tc/internal/domain"
)

func TestProfileStoreImmediateCRUDByResolution(t *testing.T) {
	store := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	created, err := store.CreateProfile(ctx, domain.ModelRequestProfile{
		ID: "profile-2k", Resolution: "2K", ResolutionKey: "2k", ConfigJSON: `{}`, ConfigHash: "hash-1", CreatedBy: "creator", UpdatedBy: "creator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.RowVersion != 1 || created.Resolution != "2K" {
		t.Fatalf("created=%+v", created)
	}
	if _, err := store.CreateProfile(ctx, domain.ModelRequestProfile{ID: "duplicate", Resolution: "2k", ResolutionKey: "2k", ConfigJSON: `{}`, ConfigHash: "hash-2", CreatedBy: "admin", UpdatedBy: "admin"}); !errors.Is(err, domain.ErrProfileKeyConflict) {
		t.Fatalf("duplicate error=%v", err)
	}
	byResolution, err := store.GetProfileByResolution(ctx, "2k")
	if err != nil || byResolution.ID != created.ID {
		t.Fatalf("by resolution=%+v err=%v", byResolution, err)
	}
	updated, err := store.UpdateProfile(ctx, created.ID, created.RowVersion, `{"generation":{"steps":9}}`, "hash-3", "editor")
	if err != nil {
		t.Fatal(err)
	}
	if updated.RowVersion != 2 || updated.UpdatedBy != "editor" || updated.ConfigHash != "hash-3" {
		t.Fatalf("updated=%+v", updated)
	}
	if _, err := store.UpdateProfile(ctx, created.ID, created.RowVersion, `{}`, "stale", "editor"); !errors.Is(err, domain.ErrProfileVersionConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	if err := store.DeleteProfile(ctx, created.ID, created.RowVersion); !errors.Is(err, domain.ErrProfileVersionConflict) {
		t.Fatalf("stale delete error=%v", err)
	}
}

func TestProfileStoreSupportsDynamicResolutionKeys(t *testing.T) {
	store := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	created, err := store.CreateProfile(ctx, domain.ModelRequestProfile{
		ID: "profile-1080p", Resolution: "1080P", ResolutionKey: "1080p", ConfigJSON: `{}`, ConfigHash: "hash", CreatedBy: "admin", UpdatedBy: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ResolutionKey != "1080p" {
		t.Fatalf("created=%+v", created)
	}
	got, err := store.GetProfileByResolution(ctx, "1080p")
	if err != nil || got.ID != created.ID || got.Resolution != "1080P" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestCreateProfileDoesNotMisreportNonNameConstraintAsDuplicate(t *testing.T) {
	store := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 100})
	_, err := store.CreateProfile(context.Background(), domain.ModelRequestProfile{
		ID: "profile-invalid-json", Resolution: "1080P", ResolutionKey: "1080p", ConfigJSON: `{`, ConfigHash: "hash", CreatedBy: "admin", UpdatedBy: "admin",
	})
	if err == nil || errors.Is(err, domain.ErrProfileKeyConflict) {
		t.Fatalf("error=%v", err)
	}
}

func TestDeleteProfileKeepsExistingTaskFrozenSnapshot(t *testing.T) {
	store := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	created, err := store.CreateProfile(ctx, domain.ModelRequestProfile{ID: "profile-delete", Resolution: "480P", ResolutionKey: "480p", ConfigJSON: `{}`, ConfigHash: "hash", CreatedBy: "admin", UpdatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	input := task("task-with-profile", "owner")
	input.Resolution = "480P"
	input.ConfigSnapshotJSON = `{"frozen":true}`
	input.ConfigHash = "hash"
	if _, err := store.Create(ctx, input, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteProfile(ctx, created.ID, created.RowVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetProfile(ctx, created.ID); !errors.Is(err, domain.ErrProfileNotFound) {
		t.Fatalf("get deleted error=%v", err)
	}
	var snapshot sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT config_snapshot_json FROM video_tasks WHERE task_id=?`, input.TaskID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Valid || snapshot.String != input.ConfigSnapshotJSON {
		t.Fatalf("snapshot=%v", snapshot)
	}
}
