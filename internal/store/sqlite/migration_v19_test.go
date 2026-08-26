package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"minimax-h3-tc/migrations"
)

func TestOfficialSubmissionBaselineStateMigration(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "migration-v19.db"), Options{})
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
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('video_tasks') WHERE name='official_submission_baseline_saved' AND type='INTEGER' AND "notnull"=1 AND dflt_value='0'`).Scan(&columnCount); err != nil {
		t.Fatal(err)
	}
	if columnCount != 1 {
		t.Fatalf("official submission baseline column count=%d", columnCount)
	}
}

func TestOfficialSubmissionBaselineStateMigrationBackfillsOnlySubmissionEvidence(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration-v19-backfill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`
CREATE TABLE model_service_nodes (
    id TEXT PRIMARY KEY,
    protocol_version TEXT NOT NULL
);
CREATE TABLE video_tasks (
    task_id TEXT PRIMARY KEY,
    upstream_job_id TEXT,
    upstream_jobs_before_json TEXT NOT NULL DEFAULT '[]',
    upstream_slot_active INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL,
    upstream_id TEXT
);
INSERT INTO model_service_nodes (id, protocol_version) VALUES
    ('official', 'minimax-v2'),
    ('internal', 'h3-node-v1');
INSERT INTO video_tasks (task_id, upstream_job_id, upstream_jobs_before_json, upstream_slot_active, status, upstream_id) VALUES
    ('queued-empty', NULL, '[]', 0, 'queued_open', NULL),
    ('official-active-empty', NULL, '[]', 1, 'dispatching', 'official'),
    ('official-job', 'job-1', '[]', 0, 'failed', 'official'),
    ('nonempty-baseline', NULL, '["before"]', 0, 'failed', 'official'),
    ('internal-active-empty', NULL, '[]', 1, 'dispatching', 'internal');
`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations.OfficialSubmissionBaselineState); err != nil {
		t.Fatal(err)
	}

	want := map[string]int{
		"queued-empty":          0,
		"official-active-empty": 1,
		"official-job":          1,
		"nonempty-baseline":     1,
		"internal-active-empty": 0,
	}
	rows, err := db.Query(`SELECT task_id, official_submission_baseline_saved FROM video_tasks`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var taskID string
		var saved int
		if err := rows.Scan(&taskID, &saved); err != nil {
			t.Fatal(err)
		}
		if saved != want[taskID] {
			t.Errorf("task %q baseline saved=%d want=%d", taskID, saved, want[taskID])
		}
		delete(want, taskID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(want) != 0 {
		t.Fatalf("missing tasks after migration: %v", want)
	}
}
