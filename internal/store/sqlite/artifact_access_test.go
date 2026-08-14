package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

func TestArtifactLocationsCanBeResolvedAndMigrationCommitIsAtomic(t *testing.T) {
	store := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	insertNodeAPINode(t, store, "source-node")
	insertNodeAPINode(t, store, "target-node")
	task, err := store.Create(ctx, domain.NewTask{TaskID: "artifact-task", APIKeyID: "owner-a", Model: "MiniMax-H3", Scenario: "t2va", RequestJSON: `{}`, RequestHash: "hash", Resolution: "2K", Duration: 5, Ratio: "16:9"}, "", func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.CreateArtifact(ctx, TaskArtifact{ID: "logical-artifact", TaskID: task.TaskID, Kind: "final_video", SizeBytes: 10, SHA256: "digest", ExpiresAt: now.Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET status='succeeded',result_artifact_id='logical-artifact',finished_at=?,expires_at=? WHERE task_id=?`, now.Unix(), now.Add(time.Hour).Unix(), task.TaskID); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateArtifactLocation(ctx, ArtifactLocation{ID: "source-location", ArtifactID: "logical-artifact", NodeID: "source-node", NodeArtifactID: "source-artifact", State: "active", IsPrimary: true, SizeBytes: 10, SHA256: "digest"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateArtifactLocation(ctx, ArtifactLocation{ID: "target-location", ArtifactID: "logical-artifact", NodeID: "target-node", NodeArtifactID: "target-artifact", State: "importing", SizeBytes: 10, SHA256: "digest"}); err != nil {
		t.Fatal(err)
	}

	source, err := store.GetActiveArtifactLocation(ctx, "logical-artifact", "source-node")
	if err != nil || source.NodeArtifactID != "source-artifact" {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	if err := store.ActivatePrimaryArtifactLocation(ctx, "logical-artifact", "target-location", 10, "digest"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateImportingArtifactLocation(ctx, "target-location", "target-artifact", 10, "digest"); err != nil {
		t.Fatalf("idempotent target location update failed: %v", err)
	}
	primary, err := store.GetPrimaryArtifactLocation(ctx, "logical-artifact")
	if err != nil || primary.ID != "target-location" || primary.State != "active" {
		t.Fatalf("primary=%+v err=%v", primary, err)
	}
	source, err = store.GetActiveArtifactLocation(ctx, "logical-artifact", "source-node")
	if err != nil || source.IsPrimary || source.State != "active" {
		t.Fatalf("retained source=%+v err=%v", source, err)
	}
	access, err := store.GetArtifactAccess(ctx, "logical-artifact")
	if err != nil || access.APIKeyID != "owner-a" || access.TaskStatus != domain.StatusSucceeded || access.Location.ID != "target-location" {
		t.Fatalf("access=%+v err=%v", access, err)
	}
}

func TestMigrationCommitRejectsIntegrityMismatchWithoutChangingPrimary(t *testing.T) {
	store := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	insertNodeAPINode(t, store, "node-a")
	insertNodeAPINode(t, store, "node-b")
	if _, err := store.db.ExecContext(ctx, `INSERT INTO video_tasks(task_id,api_key_id,model,scenario,request_json,request_hash,status,resolution,duration,ratio_requested,created_at,updated_at,expires_at) VALUES('task','owner','MiniMax-H3','t2va','{}','hash','succeeded','2K',5,'16:9',1,1,9999999999)`); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateArtifact(ctx, TaskArtifact{ID: "artifact", TaskID: "task", Kind: "final_video", SizeBytes: 10, SHA256: "good", ExpiresAt: 9999999999000}); err != nil {
		t.Fatal(err)
	}
	for _, location := range []ArtifactLocation{
		{ID: "old", ArtifactID: "artifact", NodeID: "node-a", NodeArtifactID: "old", State: "active", IsPrimary: true, SizeBytes: 10, SHA256: "good"},
		{ID: "new", ArtifactID: "artifact", NodeID: "node-b", NodeArtifactID: "new", State: "importing", SizeBytes: 10, SHA256: "good"},
	} {
		if err := store.CreateArtifactLocation(ctx, location); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ActivatePrimaryArtifactLocation(ctx, "artifact", "new", 9, "bad"); err == nil {
		t.Fatal("integrity mismatch was accepted")
	}
	primary, err := store.GetPrimaryArtifactLocation(ctx, "artifact")
	if err != nil || primary.ID != "old" {
		t.Fatalf("primary=%+v err=%v", primary, err)
	}
}

func TestArtifactAccessRejectsSupersededFinalArtifact(t *testing.T) {
	store := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	insertNodeAPINode(t, store, "node")
	if _, err := store.db.ExecContext(ctx, `INSERT INTO video_tasks(task_id,api_key_id,model,scenario,request_json,request_hash,status,resolution,duration,ratio_requested,created_at,updated_at,expires_at) VALUES('task','owner','MiniMax-H3','t2va','{}','hash','succeeded','2K',5,'16:9',1,1,9999999999)`); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateArtifact(ctx, TaskArtifact{ID: "old-final", TaskID: "task", Kind: "final_video", SizeBytes: 10, SHA256: "digest", ExpiresAt: 9999999999000}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateArtifactLocation(ctx, ArtifactLocation{ID: "old-location", ArtifactID: "old-final", NodeID: "node", NodeArtifactID: "old", State: "active", IsPrimary: true, SizeBytes: 10, SHA256: "digest"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetArtifactAccess(ctx, "old-final"); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("superseded artifact access err=%v", err)
	}
}

func TestRegisterStageOutputIsIdempotentAndCreatesFinalPrimaryLocation(t *testing.T) {
	store := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 100, Retention: time.Hour})
	ctx := context.Background()
	insertNodeAPINode(t, store, "node")
	if _, err := store.Create(ctx, domain.NewTask{TaskID: "stage-task", APIKeyID: "owner", Model: "MiniMax-H3", Scenario: "t2va", RequestJSON: `{}`, RequestHash: "hash", Resolution: "2K", Duration: 5, Ratio: "16:9"}, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateStages(ctx, []TaskStage{{ID: "final-stage", TaskID: "stage-task", StageOrder: 10, StageType: "watermark", MaxAttempts: 2, ConfigSnapshotJSON: `{}`}}); err != nil {
		t.Fatal(err)
	}
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	manifest := `{"video":{"width":64,"height":64},"audio":{"present":false}}`
	first, err := store.RegisterStageOutput(ctx, "stage-task", "final-stage", "node", "node-artifact", 123, digest, manifest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RegisterStageOutput(ctx, "stage-task", "final-stage", "node", "node-artifact", 123, digest, manifest)
	if err != nil || second != first {
		t.Fatalf("second=%q first=%q err=%v", second, first, err)
	}
	artifact, err := store.GetArtifact(ctx, first)
	if err != nil || artifact.Kind != "final_video" || artifact.SizeBytes != 123 {
		t.Fatalf("artifact=%+v err=%v", artifact, err)
	}
	location, err := store.GetPrimaryArtifactLocation(ctx, first)
	if err != nil || location.NodeArtifactID != "node-artifact" || location.NodeID != "node" {
		t.Fatalf("location=%+v err=%v", location, err)
	}
}
