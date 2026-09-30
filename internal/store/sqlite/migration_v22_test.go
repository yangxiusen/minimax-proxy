package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/migrations"
)

func v22Baseline(t *testing.T, through ...int) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v21.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	last := 21
	if len(through) > 0 {
		last = through[0]
	}
	scripts := []string{migrations.Initial, migrations.ResolutionTiers, migrations.TaskLifecycleClosure, migrations.ModelServiceNodes, migrations.ProfilesAndStages, migrations.ArtifactLifecycle, migrations.SingleEndpointNodes, migrations.CallbackDeliveryPayload, migrations.ExternalAPIKeys, migrations.RequestProfileSimplification, migrations.ExternalAPIKeyPlaintext, migrations.DynamicRequestResolutions, migrations.NodeDispatchBarriers, migrations.InputSpoolAdminMaintenance, migrations.MiniMaxV2ResultDelivery, migrations.OfficialV2Base64Inputs, migrations.OSSDirectBase64Inputs, migrations.OfficialSubmissionBaselineState, migrations.UpstreamFeedback, migrations.OSSInputObjectMetadata}
	for i, script := range scripts {
		v := i + 1
		if v >= 9 {
			v++
		}
		if v > last {
			break
		}
		v22Exec(t, db, script)
		v22Exec(t, db, `INSERT OR IGNORE INTO schema_migrations VALUES (?,1)`, v)
	}
	v22Exec(t, db, fmt.Sprintf("PRAGMA user_version=%d", last))
	return db, path
}

func v22Exec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func v22Node(t *testing.T, db *sql.DB, id, protocol, alias string) {
	t.Helper()
	v22Exec(t, db, `INSERT INTO model_service_nodes(id,service_url,protocol_version,api_key_ciphertext,api_key_nonce,api_key_fingerprint,api_key_id,base_url,jobs_base_url,public_base_url,upstream_model,capabilities_json,created_at,updated_at) VALUES(?,?,?,X'0102',X'0304','fingerprint','key','old-base','old-jobs','old-public',?,'{"stages":["generation"],"scenarios":["t2v","i2v","r2v"],"ratios":["16:9","adaptive"]}',1000,1000)`, id, "https://"+id+".example", protocol, alias)
}

func v22Task(t *testing.T, db *sql.DB, id, status string) {
	t.Helper()
	v22Exec(t, db, `INSERT INTO video_tasks(task_id,api_key_id,scenario,request_json,request_hash,status,resolution,duration,ratio_requested,created_at,updated_at,expires_at) VALUES(?,'owner','t2va','{"model":"MiniMax-H3","content":[{"type":"text","text":"prompt"}],"resolution":"768P","duration":5,"ratio":"16:9"}','original-hash',?,'768P',5,'16:9',1,1,9999999999)`, id, status)
}

func TestMigrationV22PreservesHistoricalTasks(t *testing.T) {
	db, path := v22Baseline(t)
	v22Node(t, db, "official", "minimax-v2", "legacy-alias")
	v22Task(t, db, "old", "succeeded")
	v22Exec(t, db, `UPDATE video_tasks SET upstream_id='official',result_internal_url='https://origin.example/video',callback_url_ciphertext=X'010203',callback_url_nonce=X'040506' WHERE task_id='old'`)
	v22Exec(t, db, `INSERT INTO task_stages(id,task_id,stage_order,stage_type,max_attempts,config_snapshot_json,created_at,updated_at) VALUES('stage','old',0,'generation',1,'{}',1000,1000)`)
	v22Exec(t, db, `INSERT INTO callback_deliveries(id,task_id,external_status,state_version,request_body_hash,created_at,updated_at) VALUES('callback','old','succeeded',1,'callback-hash',1000,1000)`)
	v22Exec(t, db, `INSERT INTO result_upload_jobs(id,task_id,object_key,status,created_at,updated_at) VALUES('upload','old','object','succeeded',1000,1000)`)
	v22Exec(t, db, `INSERT INTO idempotency_keys(api_key_id,key_hash,request_hash,task_id,created_at,expires_at) VALUES('owner','key','original-hash','old',1,9999999999)`)
	v22Exec(t, db, `CREATE INDEX custom_task_index ON video_tasks(request_hash); CREATE TRIGGER custom_task_trigger BEFORE UPDATE OF request_hash ON video_tasks BEGIN SELECT RAISE(ABORT,'hash protected'); END; UPDATE sqlite_sequence SET seq=500 WHERE name='video_tasks'`)
	s, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version, seq, compat, fk int
	var hash, cipher, original, instance string
	if err := s.db.QueryRow(`SELECT request_hash,hex(callback_url_ciphertext),result_internal_url FROM video_tasks WHERE task_id='old'`).Scan(&hash, &cipher, &original); err != nil {
		t.Fatal(err)
	}
	if hash != "original-hash" || cipher != "010203" || original != "https://origin.example/video" {
		t.Fatalf("history changed: %s %s %s", hash, cipher, original)
	}
	for _, table := range []string{"routing_state", "node_model_catalogs", "node_models", "model_routes", "task_remote_runs", "task_node_assets"} {
		assertTableExists(t, s.db, table)
	}
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 22 {
		t.Fatalf("version=%d want 22", version)
	}
	if err := s.db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='video_tasks'`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if seq != 500 {
		t.Fatalf("sequence=%d", seq)
	}
	if err := s.db.QueryRow(`SELECT legacy_model_compat FROM model_service_nodes WHERE id='official'`).Scan(&compat); err != nil {
		t.Fatal(err)
	}
	if compat != 1 {
		t.Fatalf("compat=%d", compat)
	}
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys=%d err=%v", fk, err)
	}
	for _, query := range []string{`UPDATE video_tasks SET request_hash='changed'`, `UPDATE video_tasks SET result_internal_url=NULL`} {
		if _, err := s.db.Exec(query); err == nil {
			t.Fatalf("protection missing: %s", query)
		}
	}
	if err := s.db.QueryRow(`SELECT instance_id FROM routing_state`).Scan(&instance); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var again string
	if err := s.db.QueryRow(`SELECT instance_id FROM routing_state`).Scan(&again); err != nil || again != instance {
		t.Fatalf("identity changed %q %v", again, err)
	}
	for _, table := range []string{"task_stages", "callback_deliveries", "result_upload_jobs", "idempotency_keys"} {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	var indexCount int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='custom_task_index'`).Scan(&indexCount); err != nil || indexCount != 1 {
		t.Fatalf("custom index=%d err=%v", indexCount, err)
	}
	var alias, nodeCipher, dispatchAlias string
	if err := s.db.QueryRow(`SELECT upstream_model,hex(api_key_ciphertext) FROM model_service_nodes WHERE id='official'`).Scan(&alias, &nodeCipher); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT json_extract(dispatch_snapshot_json,'$.upstream_model') FROM video_tasks WHERE task_id='old'`).Scan(&dispatchAlias); err != nil {
		t.Fatal(err)
	}
	if alias != "legacy-alias" || dispatchAlias != alias || nodeCipher != "0102" {
		t.Fatalf("alias/cipher changed: %s %s %s", alias, dispatchAlias, nodeCipher)
	}
	v22Task(t, s.db, "after-migration", "queued_open")
	if err := s.db.QueryRow(`SELECT queue_seq FROM video_tasks WHERE task_id='after-migration'`).Scan(&seq); err != nil || seq <= 500 {
		t.Fatalf("new sequence=%d err=%v", seq, err)
	}
}

func TestMigrationV22RejectsFutureSchema(t *testing.T) {
	for _, source := range []string{"pragma", "ledger"} {
		t.Run(source, func(t *testing.T) {
			db, path := v22Baseline(t)
			if source == "pragma" {
				v22Exec(t, db, `PRAGMA user_version=23`)
			} else {
				v22Exec(t, db, `INSERT INTO schema_migrations VALUES(23,1)`)
			}
			s, err := Open(context.Background(), path, Options{})
			if err == nil {
				s.Close()
				t.Fatal("future schema accepted")
			}
			var v int
			if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
				t.Fatal(err)
			}
			if source == "pragma" && v != 23 {
				t.Fatalf("future version overwritten: %d", v)
			}
		})
	}
}

func TestMigrationV22BackfillsSubmissionEvidence(t *testing.T) {
	db, path := v22Baseline(t)
	v22Node(t, db, "official", "minimax-v2", "")
	for _, id := range []string{"new", "ambiguous", "bound", "invalid", "unknown"} {
		v22Task(t, db, id, "queued_open")
	}
	v22Exec(t, db, `UPDATE video_tasks SET upstream_job_id='existing-job' WHERE task_id='ambiguous'`)
	v22Exec(t, db, `UPDATE video_tasks SET upstream_id='official',official_submission_baseline_saved=1 WHERE task_id='bound'`)
	v22Exec(t, db, `UPDATE video_tasks SET request_json='{}' WHERE task_id='invalid'`)
	v22Exec(t, db, `UPDATE video_tasks SET upstream_id='missing-node' WHERE task_id='unknown'`)
	s, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for id, want := range map[string]string{"new": "ready", "ambiguous": "migration_blocked", "bound": "ready", "invalid": "awaiting_route", "unknown": "migration_blocked"} {
		var state string
		if err := s.db.QueryRow(`SELECT route_state FROM video_tasks WHERE task_id=?`, id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != want {
			t.Errorf("%s=%s want %s", id, state, want)
		}
	}
}

func TestMigrationV22RollsBackAndRestoresForeignKeys(t *testing.T) {
	db, path := v22Baseline(t)
	v22Task(t, db, "old", "succeeded")
	v22Exec(t, db, `CREATE TRIGGER reject_backfill BEFORE UPDATE ON video_tasks BEGIN SELECT RAISE(ABORT,'test interruption'); END`)
	s, err := Open(context.Background(), path, Options{})
	if err == nil {
		s.Close()
		t.Fatal("interrupted migration succeeded")
	}
	var v, columns int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('video_tasks') WHERE name='route_state'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if v != 21 || columns != 0 {
		t.Fatalf("partial migration: version=%d columns=%d", v, columns)
	}
	v22Exec(t, db, `DROP TRIGGER reject_backfill`)
	s, err = Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestMigrationV22SchemaGuards(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "new.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v22Node(t, s.db, "remote", "tk2sd-v1", "")
	v22Task(t, s.db, "direct", "queued_open")
	v22Exec(t, s.db, `UPDATE video_tasks SET resolution='',duration=30 WHERE task_id='direct'`)
	v22Exec(t, s.db, `UPDATE video_tasks SET resolution='1080p' WHERE task_id='direct'`)
	v22Exec(t, s.db, `UPDATE video_tasks SET protocol_version='tk2sd-v1',route_state='ready',routing_snapshot_json='{"protocol_version":"tk2sd-v1"}' WHERE task_id='direct'`)
	for _, query := range []string{`UPDATE video_tasks SET protocol_version='minimax-v2'`, `UPDATE video_tasks SET routing_snapshot_json='{}'`, `UPDATE video_tasks SET route_state='awaiting_route'`, `INSERT INTO model_routes(model_id,selection_mode,created_at,updated_at) VALUES('x','auto',1,1)`} {
		if _, err := s.db.Exec(query); err == nil {
			t.Errorf("invalid mutation accepted: %s", query)
		}
	}
	v22Exec(t, s.db, `INSERT INTO task_remote_runs(task_id,node_id,phase,submission_key,created_at,updated_at) VALUES('direct','remote','preparing','key',1,1)`)
	if _, err := s.db.Exec(`UPDATE task_remote_runs SET phase='submitted'`); err == nil {
		t.Fatal("submitted without evidence accepted")
	}
	v22Exec(t, s.db, `UPDATE task_remote_runs SET phase='submit_intent',request_body_json='{}',request_body_hash=?`, strings.Repeat("a", 64))
	if _, err := s.db.Exec(`UPDATE task_remote_runs SET request_body_json='{"model":"different"}'`); err == nil {
		t.Fatal("submission body changed")
	}
	if _, err := s.db.Exec(`UPDATE task_remote_runs SET phase='prepared'`); err == nil {
		t.Fatal("submission evidence reset")
	}
	v22Exec(t, s.db, `INSERT INTO task_node_assets(task_id,node_id,content_index,asset_id,content_type,role,source_sha256,size_bytes,metadata_json,created_at,updated_at) VALUES('direct','remote',0,'asset','image_url','reference_image',?,10,'{}',1,1)`, strings.Repeat("a", 64))
	if _, err := s.db.Exec(`DELETE FROM model_service_nodes WHERE id='remote'`); err == nil {
		t.Fatal("deleted node owning remote records")
	}
	v22Exec(t, s.db, `DELETE FROM video_tasks WHERE task_id='direct'`)
	for _, table := range []string{"task_remote_runs", "task_node_assets"} {
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("cascade %s count=%d err=%v", table, count, err)
		}
	}
}

func TestMigrationV22ConnectionRestoresForeignKeysOnFailure(t *testing.T) {
	db, _ := v22Baseline(t)
	v22Task(t, db, "old", "queued_open")
	v22Exec(t, db, `PRAGMA foreign_keys=ON; CREATE TRIGGER reject_backfill BEFORE UPDATE ON video_tasks BEGIN SELECT RAISE(ABORT,'test interruption'); END`)
	s := &Store{db: db, options: Options{Now: time.Now}}
	if err := s.migrateV22(context.Background()); err == nil {
		t.Fatal("migration did not fail")
	}
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys=%d err=%v", fk, err)
	}
	v22Exec(t, db, `DROP TRIGGER reject_backfill`)
	if err := s.migrateV22(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationV22ConflictingOwnershipAndTerminalUnknown(t *testing.T) {
	db, path := v22Baseline(t)
	v22Node(t, db, "h3", "h3-node-v1", "")
	v22Node(t, db, "official", "minimax-v2", "")
	v22Task(t, db, "conflict", "running")
	v22Task(t, db, "terminal", "succeeded")
	v22Task(t, db, "multiple", "queued_open")
	v22Exec(t, db, `UPDATE video_tasks SET upstream_id='official' WHERE task_id='conflict'; UPDATE video_tasks SET upstream_id='removed-node',upstream_job_id='old-job' WHERE task_id='terminal'; UPDATE video_tasks SET config_snapshot_json='{}' WHERE task_id='multiple'`)
	v22Exec(t, db, `INSERT INTO task_stages(id,task_id,stage_order,stage_type,max_attempts,config_snapshot_json,created_at,updated_at) VALUES('stage','conflict',0,'generation',1,'{}',1,1)`)
	v22Exec(t, db, `INSERT INTO stage_attempts(id,stage_id,attempt_no,operation_id,node_id,status,started_at) VALUES('attempt','stage',1,'operation','h3','running',1)`)
	s, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for id, want := range map[string]string{"conflict": "migration_blocked", "terminal": "legacy_unknown", "multiple": "awaiting_route"} {
		var state string
		if err := s.db.QueryRow(`SELECT route_state FROM video_tasks WHERE task_id=?`, id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != want {
			t.Errorf("%s=%s want %s", id, state, want)
		}
	}
}

func TestMigrationV22CapabilitiesAndSnapshotContract(t *testing.T) {
	db, path := v22Baseline(t)
	v22Node(t, db, "official", "minimax-v2", "")
	v22Task(t, db, "new", "queued_open")
	v22Task(t, db, "unsupported-resolution", "queued_open")
	v22Exec(t, db, `UPDATE video_tasks SET resolution='480P',request_json=replace(request_json,'768P','480P') WHERE task_id='unsupported-resolution'`)
	s, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var resolution string
	if err := s.db.QueryRow(`SELECT json_extract(capabilities_json,'$.resolutions[0]') FROM node_models`).Scan(&resolution); err != nil {
		t.Fatal(err)
	}
	if resolution != "768P" {
		t.Fatalf("official resolution=%s", resolution)
	}
	var fingerprint string
	if err := s.db.QueryRow(`SELECT connection_fingerprint FROM node_model_catalogs`).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	wantFingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte("minimax-v2\x00https://official.example\x00fingerprint")))
	if fingerprint != wantFingerprint {
		t.Fatalf("fingerprint=%s want %s", fingerprint, wantFingerprint)
	}
	var state string
	if err := s.db.QueryRow(`SELECT route_state FROM video_tasks WHERE task_id='unsupported-resolution'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "awaiting_route" {
		t.Fatalf("unsupported official task=%s", state)
	}
}

func TestMigrationV22RejectsForeignKeyDamage(t *testing.T) {
	db, _ := v22Baseline(t)
	v22Exec(t, db, `INSERT INTO task_stages(id,task_id,stage_order,stage_type,max_attempts,config_snapshot_json,created_at,updated_at) VALUES('orphan','missing',0,'generation',1,'{}',1,1); PRAGMA foreign_keys=ON`)
	s := &Store{db: db, options: Options{Now: time.Now}}
	if err := s.migrateV22(context.Background()); err == nil {
		t.Fatal("migration accepted broken foreign key")
	}
	var version, fk int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if version != 21 || fk != 1 {
		t.Fatalf("version=%d fk=%d", version, fk)
	}
}

func TestMigrationV22UpgradeEarlyVersions(t *testing.T) {
	for _, version := range []int{1, 3, 7} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			db, path := v22Baseline(t, version)
			v22Task(t, db, "old", "succeeded")
			v22Exec(t, db, `UPDATE video_tasks SET result_internal_url='http://original.example/video'`)
			s, err := Open(context.Background(), path, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var got int
			if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&got); err != nil || got != 22 {
				t.Fatalf("version=%d err=%v", got, err)
			}
			var hash, url string
			if err := s.db.QueryRow(`SELECT request_hash,result_internal_url FROM video_tasks`).Scan(&hash, &url); err != nil {
				t.Fatal(err)
			}
			if hash != "original-hash" || url != "http://original.example/video" {
				t.Fatal("historical task changed")
			}
		})
	}
}

func TestMigrationV22CancelledTransactionRollsBack(t *testing.T) {
	db, _ := v22Baseline(t)
	v22Exec(t, db, `PRAGMA foreign_keys=ON`)
	s := &Store{db: db, options: Options{Now: time.Now}}
	ctx, cancel := context.WithCancel(context.Background())
	conn, finish, err := s.migrationConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, `CREATE TABLE must_rollback(id TEXT)`); err != nil {
		t.Fatal(err)
	}
	cancel()
	finish(&err)
	if err == nil {
		t.Fatal("cancelled transaction was committed")
	}
	var count, fk int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='must_rollback'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if count != 0 || fk != 1 {
		t.Fatalf("table=%d foreign_keys=%d", count, fk)
	}
}

func TestMigrationV22DetectsSensitiveDataChanges(t *testing.T) {
	db, _ := v22Baseline(t)
	v22Node(t, db, "official", "minimax-v2", "old-alias")
	v22Exec(t, db, `CREATE TRIGGER damage_node AFTER UPDATE ON model_service_nodes BEGIN UPDATE model_service_nodes SET api_key_ciphertext=X'99' WHERE id=NEW.id; END; PRAGMA foreign_keys=ON`)
	s := &Store{db: db, options: Options{Now: time.Now}}
	if err := s.migrateV22(context.Background()); err == nil {
		t.Fatal("migration committed changed node ciphertext")
	}
	var cipher string
	if err := db.QueryRow(`SELECT hex(api_key_ciphertext) FROM model_service_nodes`).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if cipher != "0102" {
		t.Fatalf("ciphertext changed: %s", cipher)
	}
}

func TestMigrationV22ManagedInputBackfill(t *testing.T) {
	db, path := v22Baseline(t)
	v22Node(t, db, "official", "minimax-v2", "")
	for _, id := range []string{"valid", "missing", "wrong-role"} {
		v22Task(t, db, id, "queued_open")
		v22Exec(t, db, `UPDATE video_tasks SET scenario='i2va',ratio_requested='adaptive',request_json=json_set(request_json,'$.content[1]',json_object('type','image_url','role','first_frame','image_url',json_object('url',?))) WHERE task_id=?`, "proxy-input://"+id+"/input-"+id, id)
		if id != "missing" {
			role := "first_frame"
			if id == "wrong-role" {
				role = "reference_image"
			}
			v22Exec(t, db, `INSERT INTO task_input_spool_files(id,task_id,content_index,content_type,role,source_kind,media_type,extension,relative_path,size_bytes,sha256,created_at,updated_at) VALUES(?,?,1,'image_url',?,'data_uri','image/png','.png',?,10,?,1,1)`, "input-"+id, id, role, id+".png", strings.Repeat("a", 64))
		}
	}
	s, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for id, want := range map[string]string{"valid": "ready", "missing": "awaiting_route", "wrong-role": "awaiting_route"} {
		var state string
		if err := s.db.QueryRow(`SELECT route_state FROM video_tasks WHERE task_id=?`, id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != want {
			t.Errorf("%s=%s want %s", id, state, want)
		}
	}
	var count int
	if err := s.db.QueryRow(`SELECT json_extract(routing_snapshot_json,'$.requirements.media_count') FROM video_tasks WHERE task_id='valid'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("media_count=%d", count)
	}
}
