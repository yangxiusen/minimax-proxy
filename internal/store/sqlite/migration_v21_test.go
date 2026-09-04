package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/migrations"
)

func TestOSSInputObjectMetadataMigration(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "migration-v21.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version, columnCount int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 21 {
		t.Fatalf("user_version=%d want=21", version)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('task_input_spool_files') WHERE name='object_url' AND type='TEXT' AND "notnull"=0`).Scan(&columnCount); err != nil {
		t.Fatal(err)
	}
	if columnCount != 1 {
		t.Fatalf("object_url column count=%d", columnCount)
	}
}

func TestOSSInputObjectMetadataMigrationRejectsNonHTTPS(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration-v21-constraint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE task_input_spool_files (id TEXT PRIMARY KEY); INSERT INTO task_input_spool_files(id) VALUES ('existing');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations.OSSInputObjectMetadata); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE task_input_spool_files SET object_url='http://cdn.example/input.png' WHERE id='existing'`); err == nil {
		t.Fatal("non-HTTPS object URL was accepted")
	}
}

func TestObjectBackedInputMetadataRoundTrips(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "object-input.db"), Options{PerKeyLimit: 10, GlobalLimit: 10, Retention: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	file := domain.InputSpoolFile{
		ID: "input_object", TaskID: "object-task", ContentIndex: 0, ContentType: "image_url", Role: "reference_image",
		SourceKind: "data_uri", DeclaredMIME: "image/png", DetectedMIME: "image/png", MediaType: "image/png", Extension: ".png",
		RelativePath: "MiniMax-H3/inputs/request/0-deadbeef.png", ObjectURL: "https://cdn.example/MiniMax-H3/inputs/request/0-deadbeef.png",
		SizeBytes: 12, SHA256: strings.Repeat("a", 64),
	}
	_, err = store.Create(context.Background(), domain.NewTask{
		TaskID: "object-task", APIKeyID: "owner", Model: "MiniMax-H3", Scenario: "i2va", RequestJSON: `{}`, RequestHash: strings.Repeat("b", 64),
		Resolution: "768P", Duration: 5, Ratio: "16:9", InputSpoolFiles: []domain.InputSpoolFile{file},
	}, "", func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetInputSpoolFile(context.Background(), "object-task", "input_object")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ObjectURL != file.ObjectURL || stored.RelativePath != file.RelativePath || stored.SizeBytes != file.SizeBytes {
		t.Fatalf("stored object metadata=%+v", stored)
	}
}
