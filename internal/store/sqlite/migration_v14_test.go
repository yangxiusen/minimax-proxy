package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenMigratesVersionThirteenToNodeDispatchBarriers(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "proxy.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 14 {
		t.Fatalf("user_version = %d, want 14", version)
	}

	var tableCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='node_dispatch_barriers'`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 1 {
		t.Fatalf("node_dispatch_barriers table count = %d", tableCount)
	}

	var snapshotColumns int
	rows, err := store.db.Query(`PRAGMA table_info(stage_attempts)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "request_snapshot_json" {
			snapshotColumns++
		}
	}
	if snapshotColumns != 1 {
		t.Fatalf("request_snapshot_json columns = %d", snapshotColumns)
	}
}
