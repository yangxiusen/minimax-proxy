package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"minimax-h3-tc/migrations"

	_ "modernc.org/sqlite"
)

func TestMigrationV11KeepsNewestProfilePerResolutionAndRepairsReferences(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v8.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for version, script := range []string{
		migrations.Initial, migrations.ResolutionTiers, migrations.TaskLifecycleClosure, migrations.ModelServiceNodes,
		migrations.ProfilesAndStages, migrations.ArtifactLifecycle, migrations.SingleEndpointNodes, migrations.CallbackDeliveryPayload,
	} {
		if _, err := db.ExecContext(ctx, script); err != nil {
			t.Fatalf("apply v%d: %v", version+1, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(?,1)`, version+1); err != nil {
			t.Fatal(err)
		}
	}
	profiles := []struct {
		id, resolution, scenario string
		updated                  int64
	}{
		{id: "old-2k", resolution: "2K", scenario: "t2v", updated: 10},
		{id: "new-2k", resolution: "2K", scenario: "r2v", updated: 20},
		{id: "only-480p", resolution: "480P", scenario: "i2v", updated: 15},
	}
	for index, item := range profiles {
		_, err := db.ExecContext(ctx, `INSERT INTO model_request_profiles(id,model,resolution,scenario,profile_version,status,config_json,config_hash,created_by,created_at,updated_at) VALUES(?,'MiniMax-H3',?,?,?,'draft','{}',?,'admin',1,?)`, item.id, item.resolution, item.scenario, index+1, "hash-"+item.id, item.updated)
		if err != nil {
			t.Fatal(err)
		}
	}
	for taskID, profileID := range map[string]string{"old-task": "old-2k", "new-task": "new-2k"} {
		_, err := db.ExecContext(ctx, `INSERT INTO video_tasks(task_id,api_key_id,scenario,request_json,request_hash,resolution,duration,ratio_requested,profile_id,config_snapshot_json,config_hash,created_at,updated_at,expires_at) VALUES(?,'owner','t2va','{}',?,'2K',5,'16:9',?,'{"frozen":true}',?,1,1,100)`, taskID, "request-"+taskID, profileID, "hash-"+profileID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO profile_test_runs(id,profile_id,config_hash,test_scope,status,request_snapshot_json,created_at) VALUES('test-old','old-2k','hash-old-2k','generation','passed','{}',1)`); err != nil {
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
	var ids string
	if err := store.db.QueryRowContext(ctx, `SELECT group_concat(id,',') FROM (SELECT id FROM model_request_profiles ORDER BY resolution)`).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	if ids != "new-2k,only-480p" {
		t.Fatalf("retained profiles=%q", ids)
	}
	var oldRef, oldSnapshot sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT profile_id,config_snapshot_json FROM video_tasks WHERE task_id='old-task'`).Scan(&oldRef, &oldSnapshot); err != nil {
		t.Fatal(err)
	}
	if oldRef.Valid || !oldSnapshot.Valid || oldSnapshot.String != `{"frozen":true}` {
		t.Fatalf("old task ref=%v snapshot=%v", oldRef, oldSnapshot)
	}
	var newRef string
	if err := store.db.QueryRowContext(ctx, `SELECT profile_id FROM video_tasks WHERE task_id='new-task'`).Scan(&newRef); err != nil || newRef != "new-2k" {
		t.Fatalf("new task ref=%q err=%v", newRef, err)
	}
	var tests, version, violations int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM profile_test_runs`).Scan(&tests); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=11`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		violations++
	}
	_ = rows.Close()
	if tests != 0 || version != 1 || violations != 0 {
		t.Fatalf("tests=%d version=%d foreign key violations=%d", tests, version, violations)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO model_request_profiles(id,model,resolution,scenario,profile_version,status,config_json,config_hash,created_by,created_at,updated_at) VALUES('duplicate','MiniMax-H3','2K','t2v',99,'draft','{}','duplicate','admin',1,1)`); err == nil {
		t.Fatal("duplicate resolution unexpectedly accepted")
	}
}
