package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"minimax-h3-tc/migrations"

	_ "modernc.org/sqlite"
)

func TestMigrationV13BackfillsResolutionKeys(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v12.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	scripts := []struct {
		version int
		sql     string
	}{
		{1, migrations.Initial}, {2, migrations.ResolutionTiers}, {3, migrations.TaskLifecycleClosure},
		{4, migrations.ModelServiceNodes}, {5, migrations.ProfilesAndStages}, {6, migrations.ArtifactLifecycle},
		{7, migrations.SingleEndpointNodes}, {8, migrations.CallbackDeliveryPayload}, {10, migrations.ExternalAPIKeys},
		{11, migrations.RequestProfileSimplification}, {12, migrations.ExternalAPIKeyPlaintext},
	}
	for _, item := range scripts {
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply v%d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(?,1)`, item.version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO model_request_profiles(id,model,resolution,scenario,profile_version,status,config_json,config_hash,created_by,created_at,updated_at,updated_by) VALUES('custom','MiniMax-H3','2K','r2v',1,'active','{}','hash','admin',1,1,'admin')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var key string
	if err := store.db.QueryRowContext(ctx, `SELECT resolution_key FROM request_profiles WHERE id='custom'`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "2k" {
		t.Fatalf("resolution_key=%q", key)
	}
	var userVersion int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil || userVersion != 16 {
		t.Fatalf("user_version=%d err=%v", userVersion, err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO request_profiles(id,resolution,resolution_key,config_json,config_hash,created_by,updated_by,created_at,updated_at) VALUES('dynamic','1080P','1080p','{}','hash','admin','admin',1,1)`); err != nil {
		t.Fatalf("insert dynamic resolution: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO request_profiles(id,resolution,resolution_key,config_json,config_hash,created_by,updated_by,created_at,updated_at) VALUES('duplicate','1080p','1080p','{}','hash','admin','admin',1,1)`); err == nil {
		t.Fatal("case-insensitive duplicate unexpectedly accepted")
	}
	var foreignKeyViolations int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyViolations); err != nil {
		t.Fatal(err)
	}
	if foreignKeyViolations != 0 {
		t.Fatalf("foreign key violations=%d", foreignKeyViolations)
	}
}
