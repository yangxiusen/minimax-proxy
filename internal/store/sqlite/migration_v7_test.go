package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"minimax-h3-tc/migrations"

	_ "modernc.org/sqlite"
)

func TestOpenMigratesEmptyDatabaseThroughVersionFifteenAndIsRepeatable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "empty.db")
	for attempt := 0; attempt < 2; attempt++ {
		store, err := Open(ctx, path, Options{})
		if err != nil {
			t.Fatalf("open attempt %d: %v", attempt, err)
		}
		var userVersion, migrationCount int
		if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
			t.Fatal(err)
		}
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version IN (1,2,3,4,5,6,7,8,10,11,12,13,14,15)`).Scan(&migrationCount); err != nil {
			t.Fatal(err)
		}
		if userVersion != 15 || migrationCount != 14 {
			t.Fatalf("attempt %d user_version=%d migrations=%d", attempt, userVersion, migrationCount)
		}
		for _, table := range []string{"model_request_profiles", "request_profiles", "profile_test_runs", "task_stages", "stage_attempts", "task_artifacts", "artifact_locations", "artifact_deletion_jobs", "artifact_deletion_items", "callback_deliveries", "external_api_keys", "api_key_config_bootstrap", "task_input_spool_files"} {
			assertTableExists(t, store.db, table)
		}
		var foreignKeyViolations int
		rows, err := store.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			foreignKeyViolations++
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if foreignKeyViolations != 0 {
			t.Fatalf("attempt %d foreign key violations=%d", attempt, foreignKeyViolations)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenMigratesVersionFourDataWithoutInventingTaskSnapshots(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v4.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for version, script := range []string{migrations.Initial, migrations.ResolutionTiers, migrations.TaskLifecycleClosure, migrations.ModelServiceNodes} {
		if _, err := db.ExecContext(ctx, script); err != nil {
			t.Fatalf("apply v%d: %v", version+1, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(?,1)`, version+1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version=4`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO video_tasks(task_id,api_key_id,scenario,request_json,request_hash,status,result_internal_url,result_public_url,resolution,duration,created_at,updated_at,expires_at) VALUES('legacy-task','owner','t2va','{}','hash','succeeded','http://private/result.mp4','https://public/result.mp4','2K',5,1,1,100)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO model_service_nodes(id,base_url,jobs_base_url,public_base_url,created_at,updated_at) VALUES('legacy-node','http://127.0.0.1:7860','http://127.0.0.1:8188','https://video.example.com',1,1)`); err != nil {
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
	var profileID, resultArtifactID, snapshot, hash sql.NullString
	var deletionState, internalURL, publicURL string
	if err := store.db.QueryRowContext(ctx, `SELECT profile_id,result_artifact_id,config_snapshot_json,config_hash,deletion_state,result_internal_url,result_public_url FROM video_tasks WHERE task_id='legacy-task'`).Scan(&profileID, &resultArtifactID, &snapshot, &hash, &deletionState, &internalURL, &publicURL); err != nil {
		t.Fatal(err)
	}
	if profileID.Valid || resultArtifactID.Valid || snapshot.Valid || hash.Valid || deletionState != "not_requested" || internalURL == "" || publicURL == "" {
		t.Fatalf("legacy task changed: profile=%v artifact=%v snapshot=%v hash=%v deletion=%q urls=%q/%q", profileID, resultArtifactID, snapshot, hash, deletionState, internalURL, publicURL)
	}
	var serviceURL, protocolVersion string
	if err := store.db.QueryRowContext(ctx, `SELECT service_url,protocol_version FROM model_service_nodes WHERE id='legacy-node'`).Scan(&serviceURL, &protocolVersion); err != nil {
		t.Fatal(err)
	}
	if serviceURL != "http://127.0.0.1:7860" || protocolVersion != "legacy-gradio-v1" {
		t.Fatalf("legacy node service=%q protocol=%q", serviceURL, protocolVersion)
	}
}

func TestMigrationBatchRollsBackOnFailure(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &Store{db: db, options: Options{Now: func() time.Time { return time.Unix(100, 0) }}}
	err = store.applyMigrations(ctx, []migration{
		{version: 1, name: "valid", sql: `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL); CREATE TABLE should_rollback(id INTEGER);`},
		{version: 2, name: "broken", sql: `CREATE TABLE broken(`},
	})
	if err == nil {
		t.Fatal("migration unexpectedly succeeded")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('schema_migrations','should_rollback')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("partial migration objects=%d", count)
	}
}

func TestOpenForwardsVersionSevenCallbackRowsThroughVersionFifteen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v7.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for version, script := range []string{
		migrations.Initial, migrations.ResolutionTiers, migrations.TaskLifecycleClosure, migrations.ModelServiceNodes,
		migrations.ProfilesAndStages, migrations.ArtifactLifecycle, migrations.SingleEndpointNodes,
	} {
		if _, err := db.ExecContext(ctx, script); err != nil {
			t.Fatalf("apply v%d: %v", version+1, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(?,1)`, version+1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version=7`); err != nil {
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
	var userVersion, columns int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('callback_deliveries') WHERE name IN ('request_body','lease_expires_at')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if userVersion != 15 || columns != 2 {
		t.Fatalf("user_version=%d columns=%d", userVersion, columns)
	}
}

func assertTableExists(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("table %s count=%d", table, count)
	}
}
