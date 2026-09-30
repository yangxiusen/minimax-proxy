package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

func remoteFixture(t *testing.T) (*Store, domain.Task) {
	t.Helper()
	s := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 20})
	v22Node(t, s.db, "remote-node", domain.ProtocolTK2SD, "")
	v22Exec(t, s.db, `INSERT INTO node_model_catalogs(node_id,source,connection_fingerprint,last_status,last_success_at,valid_until,created_at,updated_at) VALUES('remote-node','discovered',?,'ready',1,9999999999,1,1)`, CatalogFingerprint(domain.ModelNodeInput{ProtocolVersion: domain.ProtocolTK2SD, ServiceURL: "https://remote-node.example", APIKeyFingerprint: "fingerprint"}))
	v22Exec(t, s.db, `INSERT INTO node_models(node_id,model_id,enabled,present,capabilities_json,verified,created_at,updated_at) VALUES('remote-node','seed-model',1,1,'{"schema_version":1,"modes":[{"scenario":"t2va","durations":[5]}],"max_media":12}',1,1,1)`)
	v22Exec(t, s.db, `INSERT INTO video_tasks(task_id,api_key_id,model,scenario,request_json,request_hash,status,resolution,duration,protocol_version,route_state,routing_snapshot_json,created_at,updated_at,expires_at) VALUES('remote-task','owner','seed-model','t2va','{"model":"seed-model","content":[{"type":"text","text":"test"}],"duration":5}','hash','queued_open','',5,'tk2sd-v1','ready','{"schema_version":1,"model":"seed-model","protocol_version":"tk2sd-v1","requirements":{"scenario":"t2va","duration":5}}',1,1,9999999999)`)
	task, err := s.ClaimNextRemote(context.Background(), "remote-node", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return s, task
}

func TestRemoteLeaseAndImmutableSubmitRecovery(t *testing.T) {
	s, task := remoteFixture(t)
	ctx := context.Background()
	run, err := s.AcquireRemoteLease(ctx, task.TaskID, "remote-node", "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcquireRemoteLease(ctx, task.TaskID, "remote-node", "worker-b", time.Minute); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("second lease: %v", err)
	}
	body := `{"model":"seed-model","content":[{"type":"text","text":"test"}],"duration":5}`
	if err = s.PrepareRemoteRequest(ctx, run, body); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginRemoteSubmission(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err = s.PrepareRemoteRequest(ctx, run, `{"model":"changed"}`); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("mutated intent: %v", err)
	}
	if err = s.ReleaseRemoteLease(ctx, run, 0, "submit_unknown", false); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.AcquireRemoteLease(ctx, task.TaskID, "remote-node", "worker-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.SubmissionKey != run.SubmissionKey || recovered.RequestBodyJSON != body || recovered.Phase != domain.RemoteSubmitIntent {
		t.Fatalf("lost evidence: %+v", recovered)
	}
	if err = s.BindRemoteTask(ctx, run, "stale"); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("stale write: %v", err)
	}
	if err = s.BeginRemoteSubmission(ctx, recovered); err != nil {
		t.Fatal(err)
	}
	if err = s.BindRemoteTask(ctx, recovered, "upstream-task"); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginRemoteSubmission(ctx, recovered); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("resubmit after bind: %v", err)
	}
}

func TestRemoteCancelWinsBeforeSubmitIntent(t *testing.T) {
	s, task := remoteFixture(t)
	ctx := context.Background()
	run, err := s.AcquireRemoteLease(ctx, task.TaskID, "remote-node", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PrepareRemoteRequest(ctx, run, `{"model":"seed-model"}`); err != nil {
		t.Fatal(err)
	}
	if err = s.RequestRemoteCancel(ctx, task.TaskID); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginRemoteSubmission(ctx, run); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("submit beat persisted cancel: %v", err)
	}
	if err = s.CompleteRemoteRun(ctx, run, domain.RemoteCompletion{Status: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "owner", task.TaskID)
	if err != nil || got.Status != domain.StatusCancelled || got.UpstreamSlotActive {
		t.Fatalf("cancelled task: %+v %v", got, err)
	}
	if err = s.CompleteRemoteRun(ctx, run, domain.RemoteCompletion{Status: "failed"}); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("second terminal: %v", err)
	}
}

func TestRemoteClaimRejectsExpiredCatalog(t *testing.T) {
	s, _ := remoteFixture(t)
	v22Exec(t, s.db, `UPDATE model_service_nodes SET max_concurrency=2 WHERE id='remote-node'`)
	remoteQueued(t, s, "waiting", "seed-model", "queued_open", 5)
	v22Exec(t, s.db, `UPDATE node_model_catalogs SET valid_until=1`)
	_, err := s.ClaimNextRemote(context.Background(), "remote-node", 1, 2)
	if !errors.Is(err, domain.ErrQueueEmpty) {
		t.Fatalf("expired catalog: %v", err)
	}
}

func remoteQueued(t *testing.T, s *Store, id, model, status string, duration int) {
	t.Helper()
	route, _ := json.Marshal(domain.RouteSnapshot{SchemaVersion: 1, Model: model, ProtocolVersion: domain.ProtocolTK2SD, Requirements: domain.TaskRequirements{Scenario: "t2va", Duration: duration}})
	body, _ := json.Marshal(domain.GenerationRequest{Model: model, Duration: duration, Content: []domain.GenerationContent{{Type: "text", Text: "test"}}})
	v22Exec(t, s.db, `INSERT INTO video_tasks(task_id,api_key_id,model,scenario,request_json,request_hash,status,resolution,duration,protocol_version,route_state,routing_snapshot_json,created_at,updated_at,expires_at) VALUES(?,'owner',?,'t2va',?,'hash',?,'',?,'tk2sd-v1','ready',?,1,1,9999999999)`, id, model, string(body), status, duration, string(route))
}

func TestRemoteClaimScansBothQueueStatesAndWholeCapability(t *testing.T) {
	s, _ := remoteFixture(t)
	ctx := context.Background()
	v22Exec(t, s.db, `UPDATE model_service_nodes SET max_concurrency=3 WHERE id='remote-node'`)
	remoteQueued(t, s, "unknown-model", "seed-model-unknown", "queued_locked", 5)
	remoteQueued(t, s, "wrong-case", "Seed-model", "queued_open", 5)
	remoteQueued(t, s, "unsupported-duration", "seed-model", "queued_locked", 6)
	remoteQueued(t, s, "eligible-locked", "seed-model", "queued_locked", 5)
	remoteQueued(t, s, "eligible-open", "seed-model", "queued_open", 5)
	for _, want := range []string{"eligible-locked", "eligible-open"} {
		got, err := s.ClaimNextRemote(ctx, "remote-node", 1, 3)
		if err != nil || got.TaskID != want {
			t.Fatal(got.TaskID, err, want)
		}
	}
	if _, err := s.ClaimNextRemote(ctx, "remote-node", 1, 99); !errors.Is(err, domain.ErrUpstreamBusy) {
		t.Fatal("capacity exceeded", err)
	}
}

func TestRemoteClaimRejectsChangedConnectionButRecoveryDoesNot(t *testing.T) {
	s, task := remoteFixture(t)
	ctx := context.Background()
	v22Exec(t, s.db, `UPDATE model_service_nodes SET max_concurrency=2,api_key_fingerprint='changed' WHERE id='remote-node'`)
	remoteQueued(t, s, "waiting", "seed-model", "queued_open", 5)
	if _, err := s.ClaimNextRemote(ctx, "remote-node", 1, 2); !errors.Is(err, domain.ErrQueueEmpty) {
		t.Fatal("stale discovered catalog used", err)
	}
	v22Exec(t, s.db, `UPDATE model_service_nodes SET enabled=0 WHERE id='remote-node'`)
	run, err := s.AcquireRemoteLease(ctx, task.TaskID, "remote-node", "recover", time.Minute)
	if err != nil {
		t.Fatal("disabled node cannot resume", err)
	}
	if run.NodeID != "remote-node" {
		t.Fatal(run.NodeID)
	}
}

func TestRemoteExpiredLeaseAndAssetScope(t *testing.T) {
	s, task := remoteFixture(t)
	ctx := context.Background()
	now := time.Now()
	s.options.Now = func() time.Time { return now }
	run, err := s.AcquireRemoteLease(ctx, task.TaskID, "remote-node", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	asset := domain.NodeAsset{TaskID: task.TaskID, NodeID: "remote-node", ContentIndex: 1, AssetID: "asset-id", ContentType: "image_url", Role: "reference_image", SourceSHA256: strings.Repeat("a", 64), SizeBytes: 12, MetadataJSON: `{"kind":"image"}`}
	if err = s.SaveRemoteAsset(ctx, run, asset); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveRemoteAsset(ctx, run, asset); err != nil {
		t.Fatal("identical asset save is not idempotent", err)
	}
	asset.NodeID = "other-node"
	if err = s.SaveRemoteAsset(ctx, run, asset); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatal("cross-node asset accepted", err)
	}
	asset.NodeID = "remote-node"
	asset.AssetID = "different"
	if err = s.SaveRemoteAsset(ctx, run, asset); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatal("changed asset accepted", err)
	}
	now = now.Add(61 * time.Second)
	if err = s.PrepareRemoteRequest(ctx, run, `{"model":"seed-model"}`); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatal("expired write accepted", err)
	}
	if err = s.CompleteRemoteRun(ctx, run, domain.RemoteCompletion{Status: "failed"}); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatal("expired terminal accepted", err)
	}
	got, _ := s.Get(ctx, "owner", task.TaskID)
	if !got.UpstreamSlotActive {
		t.Fatal("expired lease released capacity")
	}
	if _, err = s.AcquireRemoteLease(ctx, task.TaskID, "remote-node", "new-worker", time.Minute); err != nil {
		t.Fatal(err)
	}
}
