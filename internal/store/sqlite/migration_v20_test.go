package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"minimax-h3-tc/migrations"
)

func TestUpstreamFeedbackMigration(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "migration-v20.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 20 {
		t.Fatalf("user_version=%d want=20", version)
	}
	var columnCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('video_tasks') WHERE name='upstream_feedback_json' AND type='TEXT' AND "notnull"=0`).Scan(&columnCount); err != nil {
		t.Fatal(err)
	}
	if columnCount != 1 {
		t.Fatalf("upstream feedback column count=%d", columnCount)
	}
}

func TestUpstreamFeedbackMigrationPreservesRowsAndRejectsInvalidJSON(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration-v20-populated.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE video_tasks (task_id TEXT PRIMARY KEY); INSERT INTO video_tasks(task_id) VALUES ('existing');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations.UpstreamFeedback); err != nil {
		t.Fatal(err)
	}
	var count int
	var feedback *string
	if err := db.QueryRow(`SELECT COUNT(*),upstream_feedback_json FROM video_tasks`).Scan(&count, &feedback); err != nil {
		t.Fatal(err)
	}
	if count != 1 || feedback != nil {
		t.Fatalf("count=%d feedback=%v", count, feedback)
	}
	if _, err := db.Exec(`UPDATE video_tasks SET upstream_feedback_json='not-json' WHERE task_id='existing'`); err == nil {
		t.Fatal("invalid upstream feedback JSON was accepted")
	}
}
