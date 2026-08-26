package sqlite

import (
	"context"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/migrations"
)

func TestMigrationV15CreatesInputSpoolFiles(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	var userVersion int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatalf("query user_version: %v", err)
	}
	if userVersion != 20 {
		t.Fatalf("user_version=%d, want 20", userVersion)
	}
	var migrationCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=15`).Scan(&migrationCount); err != nil {
		t.Fatalf("query migration: %v", err)
	}
	if migrationCount != 1 {
		t.Fatalf("migration v15 count=%d, want 1", migrationCount)
	}
	var tableCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='task_input_spool_files'`).Scan(&tableCount); err != nil {
		t.Fatalf("query table: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("task_input_spool_files table count=%d, want 1", tableCount)
	}
}

func TestMigrationV17AllowsVideoInputSpoolFiles(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	var userVersion int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if userVersion != 20 {
		t.Fatalf("user_version=%d, want 20", userVersion)
	}
	input := newStoreTask("video-spool", "key-video-spool")
	input.InputSpoolFiles = []domain.InputSpoolFile{{
		ID: "input_video", TaskID: input.TaskID, ContentIndex: 0, ContentType: "video_url", Role: "reference_video",
		SourceKind: "data_uri", DeclaredMIME: "video/mp4", DetectedMIME: "video/mp4", MediaType: "video/mp4",
		Extension: ".mp4", RelativePath: "video-spool/input_video.mp4", SizeBytes: 12,
		SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}
	if _, err := store.Create(ctx, input, "video-spool-key", func() bool { return true }); err != nil {
		t.Fatalf("Create() video spool error=%v", err)
	}
	files, err := store.ListInputSpoolFiles(ctx, input.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].ContentType != "video_url" || files[0].MediaType != "video/mp4" {
		t.Fatalf("video spool files=%+v", files)
	}
}

func TestMigrationV17TableRebuildPreservesExistingRows(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	input := newStoreTask("preserve-spool", "key-preserve-spool")
	input.InputSpoolFiles = []domain.InputSpoolFile{{
		ID: "input-existing", TaskID: input.TaskID, ContentIndex: 0, ContentType: "image_url", Role: "first_frame",
		SourceKind: "data_uri", MediaType: "image/png", Extension: ".png", RelativePath: "preserve-spool/input-existing.png",
		SizeBytes: 12, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}
	if _, err := store.Create(ctx, input, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, migrations.OfficialV2Base64Inputs); err != nil {
		t.Fatalf("rebuild input spool table: %v", err)
	}
	files, err := store.ListInputSpoolFiles(ctx, input.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].ID != "input-existing" || files[0].ContentType != "image_url" {
		t.Fatalf("preserved files=%+v", files)
	}
}

func TestCreatePersistsInputSpoolFilesAndFindsIdempotentTaskBeforeSpooling(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	input := newStoreTask("spool-task", "key-spool")
	input.RequestHash = "hash-spool"
	input.InputSpoolFiles = []domain.InputSpoolFile{{
		ID:           "input_0123456789abcdef0123456789abcdef",
		TaskID:       "spool-task",
		ContentIndex: 1,
		ContentType:  "image_url",
		Role:         "first_frame",
		SourceKind:   "data_uri",
		DeclaredMIME: "image/png",
		DetectedMIME: "image/png",
		MediaType:    "image/png",
		Extension:    ".png",
		RelativePath: "spool-task/input_0123456789abcdef0123456789abcdef.png",
		SizeBytes:    68,
		SHA256:       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}
	task, err := store.Create(ctx, input, "key-hash-spool", func() bool { return true })
	if err != nil {
		t.Fatalf("Create() error=%v", err)
	}
	files, err := store.ListInputSpoolFiles(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("ListInputSpoolFiles() error=%v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files len=%d, want 1", len(files))
	}
	if files[0].RelativePath != input.InputSpoolFiles[0].RelativePath || files[0].SHA256 != input.InputSpoolFiles[0].SHA256 {
		t.Fatalf("file=%+v, want relative_path and sha256 from input", files[0])
	}
	found, err := store.FindIdempotentTask(ctx, input.APIKeyID, "key-hash-spool", input.RequestHash)
	if err != nil {
		t.Fatalf("FindIdempotentTask() error=%v", err)
	}
	if found.TaskID != task.TaskID {
		t.Fatalf("found task=%s, want %s", found.TaskID, task.TaskID)
	}
	if _, err := store.FindIdempotentTask(ctx, input.APIKeyID, "key-hash-spool", "different-hash"); err != domain.ErrIdempotencyConflict {
		t.Fatalf("FindIdempotentTask(conflict) error=%v, want ErrIdempotencyConflict", err)
	}
}

func TestListInputSpoolFilesReturnsNotFoundForUnknownInput(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if _, err := store.GetInputSpoolFile(ctx, "missing-task", "missing-input"); err != domain.ErrTaskNotFound {
		t.Fatalf("GetInputSpoolFile() error=%v, want ErrTaskNotFound", err)
	}
}

func TestAdminDeletePhysicallyRemovesTaskAndInputSpoolRows(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	input := newStoreTask("delete-spool", "key-delete")
	input.InputSpoolFiles = []domain.InputSpoolFile{{
		ID: "input_delete", TaskID: "delete-spool", ContentIndex: 0, ContentType: "image_url", Role: "first_frame",
		SourceKind: "data_uri", MediaType: "image/png", Extension: ".png", RelativePath: "delete-spool/input_delete.png",
		SizeBytes: 12, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}
	if _, err := store.Create(ctx, input, "delete-key", func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestAdminCancel(ctx, "delete-spool"); err != nil {
		t.Fatal(err)
	}
	if err := store.AdminDelete(ctx, "delete-spool"); err != nil {
		t.Fatalf("AdminDelete() error=%v", err)
	}
	var taskCount, spoolCount, fkCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_tasks WHERE task_id='delete-spool'`).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_input_spool_files WHERE task_id='delete-spool'`).Scan(&spoolCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&fkCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 0 || spoolCount != 0 || fkCount != 0 {
		t.Fatalf("taskCount=%d spoolCount=%d fkCount=%d", taskCount, spoolCount, fkCount)
	}
}

func TestAdminDeleteRejectsWhenPurgedArtifactLocationsChanged(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	input := newStoreTask("delete-location-race", "key-delete")
	if _, err := store.Create(ctx, input, "delete-location-race-key", func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestAdminCancel(ctx, input.TaskID); err != nil {
		t.Fatal(err)
	}
	insertNodeAPINode(t, store, "node-a")
	insertNodeAPINode(t, store, "node-b")
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := store.RegisterInputArtifact(ctx, "input_race", input.TaskID, "test_output", "node-a", "node-artifact-a", 12, digest, ""); err != nil {
		t.Fatal(err)
	}
	purged, err := store.ListTaskArtifactLocations(ctx, input.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(purged) != 1 {
		t.Fatalf("purged locations len=%d, want 1", len(purged))
	}
	if err := store.RegisterInputArtifact(ctx, "input_race", input.TaskID, "test_output", "node-b", "node-artifact-b", 12, digest, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.AdminDelete(ctx, input.TaskID, purged...); err != domain.ErrStateConflict {
		t.Fatalf("AdminDelete() error=%v, want ErrStateConflict", err)
	}
	var taskCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_tasks WHERE task_id=?`, input.TaskID).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 1 {
		t.Fatalf("taskCount=%d, want task retained", taskCount)
	}
}

func newStoreTask(taskID, apiKeyID string) domain.NewTask {
	return domain.NewTask{
		TaskID:          taskID,
		APIKeyID:        apiKeyID,
		Model:           "MiniMax-H3",
		Scenario:        "i2va",
		RequestJSON:     `{"model":"MiniMax-H3","content":[],"resolution":"768P","duration":5}`,
		RequestHash:     "hash-" + taskID,
		Resolution:      "768P",
		Ratio:           "16:9",
		Duration:        5,
		InputImageCount: 1,
		Stages: []domain.NewTaskStage{{
			ID:                 "stage-" + taskID,
			StageType:          "generation",
			StageOrder:         0,
			MaxAttempts:        1,
			ConfigSnapshotJSON: `{"ratios":{"16:9":{"base_width":1280,"base_height":720}}}`,
		}},
		ConfigSnapshotJSON:  `{"ratios":{"16:9":{"base_width":1280,"base_height":720}}}`,
		ConfigHash:          "config-" + taskID,
		CallbackRequestBody: "",
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir()+`\test.db`, Options{
		ProtectedSlots: 0,
		PerKeyLimit:    10,
		GlobalLimit:    100,
		Retention:      24 * time.Hour,
		IdempotencyTTL: 24 * time.Hour,
		Now:            func() time.Time { return time.Unix(1700000000, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("Open() error=%v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
