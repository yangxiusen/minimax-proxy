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
	v23Node(t, s.db, "remote-node", domain.ProtocolTK2SD, "")
	v23Exec(t, s.db, `INSERT INTO node_model_catalogs(node_id,source,connection_fingerprint,last_status,last_success_at,valid_until,created_at,updated_at) VALUES('remote-node','discovered',?,'ready',1,9999999999,1,1)`, CatalogFingerprint(domain.ModelNodeInput{ProtocolVersion: domain.ProtocolTK2SD, ServiceURL: "https://remote-node.example", APIKeyFingerprint: "fingerprint"}))
	v23Exec(t, s.db, `INSERT INTO node_models(node_id,model_id,enabled,present,capabilities_json,verified,created_at,updated_at) VALUES('remote-node','seed-model',1,1,'{"schema_version":1,"modes":[{"scenario":"t2va","durations":[5]}],"max_media":12}',1,1,1)`)
	v23Exec(t, s.db, `INSERT INTO video_tasks(task_id,api_key_id,model,scenario,request_json,request_hash,status,resolution,duration,protocol_version,route_state,routing_snapshot_json,created_at,updated_at,expires_at) VALUES('remote-task','owner','seed-model','t2va','{"model":"seed-model","content":[{"type":"text","text":"test"}],"duration":5}','hash','queued_open','',5,'tk2sd-v1','ready','{"schema_version":1,"model":"seed-model","protocol_version":"tk2sd-v1","requirements":{"scenario":"t2va","duration":5}}',1,1,9999999999)`)
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
	v23Exec(t, s.db, `UPDATE model_service_nodes SET max_concurrency=2 WHERE id='remote-node'`)
	remoteQueued(t, s, "waiting", "seed-model", "queued_open", 5)
	v23Exec(t, s.db, `UPDATE node_model_catalogs SET valid_until=1`)
	_, err := s.ClaimNextRemote(context.Background(), "remote-node", 1, 2)
	if !errors.Is(err, domain.ErrQueueEmpty) {
		t.Fatalf("expired catalog: %v", err)
	}
}

func remoteQueued(t *testing.T, s *Store, id, model, status string, duration int) {
	t.Helper()
	route, _ := json.Marshal(domain.RouteSnapshot{SchemaVersion: 1, Model: model, ProtocolVersion: domain.ProtocolTK2SD, Requirements: domain.TaskRequirements{Scenario: "t2va", Duration: duration}})
	body, _ := json.Marshal(domain.GenerationRequest{Model: model, Duration: duration, Content: []domain.GenerationContent{{Type: "text", Text: "test"}}})
	v23Exec(t, s.db, `INSERT INTO video_tasks(task_id,api_key_id,model,scenario,request_json,request_hash,status,resolution,duration,protocol_version,route_state,routing_snapshot_json,created_at,updated_at,expires_at) VALUES(?,'owner',?,'t2va',?,'hash',?,'',?,'tk2sd-v1','ready',?,1,1,9999999999)`, id, model, string(body), status, duration, string(route))
}

func TestRemoteClaimScansBothQueueStatesAndWholeCapability(t *testing.T) {
	s, _ := remoteFixture(t)
	ctx := context.Background()
	v23Exec(t, s.db, `UPDATE model_service_nodes SET max_concurrency=3 WHERE id='remote-node'`)
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

func TestTK2SDCapacityCanIncreaseWithActiveTaskButCannotDecrease(t *testing.T) {
	s, _ := remoteFixture(t)
	ctx := context.Background()
	node, err := s.GetModelNode(ctx, "remote-node")
	if err != nil {
		t.Fatal(err)
	}
	input := node.ModelNodeInput
	input.MaxConcurrency = 3
	updated, err := s.UpdateModelNode(ctx, node.ID, node.Version, input)
	if err != nil || updated.MaxConcurrency != 3 {
		t.Fatalf("increase with active task: %+v %v", updated, err)
	}
	remoteQueued(t, s, "parallel-2", "seed-model", "queued_open", 5)
	remoteQueued(t, s, "parallel-3", "seed-model", "queued_open", 5)
	remoteQueued(t, s, "waiting", "seed-model", "queued_open", 5)
	for _, want := range []string{"parallel-2", "parallel-3"} {
		claimed, err := s.ClaimNextRemote(ctx, node.ID, updated.Version, updated.MaxConcurrency)
		if err != nil || claimed.TaskID != want {
			t.Fatalf("claim %q: %+v %v", want, claimed, err)
		}
	}
	if _, err := s.ClaimNextRemote(ctx, node.ID, updated.Version, updated.MaxConcurrency); !errors.Is(err, domain.ErrUpstreamBusy) {
		t.Fatalf("capacity exceeded: %v", err)
	}
	input.MaxConcurrency = 2
	if _, err := s.UpdateModelNode(ctx, node.ID, updated.Version, input); !errors.Is(err, domain.ErrNodeHasActiveTask) {
		t.Fatalf("decrease with active task: %v", err)
	}
	input.MaxConcurrency = 4
	input.ServiceURL = "https://other-node.example"
	if _, err := s.UpdateModelNode(ctx, node.ID, updated.Version, input); !errors.Is(err, domain.ErrNodeHasActiveTask) {
		t.Fatalf("connection change with active task: %v", err)
	}
}

func TestTK2SDAdmissionCountsExternalAccountsAndUnaccountedProxyReservations(t *testing.T) {
	s, _ := remoteFixture(t)
	ctx := context.Background()
	v23Exec(t, s.db, `UPDATE model_service_nodes SET max_concurrency=5 WHERE id='remote-node'`)
	remoteQueued(t, s, "next", "seed-model", "queued_open", 5)
	remoteQueued(t, s, "later", "seed-model", "queued_open", 5)
	full := domain.TK2SDAdmission{Capacity: 3, Occupied: 2, CanDispatch: true, LeasedTaskIDs: []string{"external-a", "external-b"}}
	if _, err := s.ClaimNextRemote(ctx, "remote-node", 1, 5, full); !errors.Is(err, domain.ErrUpstreamBusy) {
		t.Fatalf("external occupancy plus pending Proxy task should fill accounts: %v", err)
	}
	v23Exec(t, s.db, `UPDATE task_remote_runs SET upstream_task_id='owned' WHERE task_id='remote-task'`)
	full.LeasedTaskIDs = []string{"external-a", "owned"}
	got, err := s.ClaimNextRemote(ctx, "remote-node", 1, 5, full)
	if err != nil || got.TaskID != "next" {
		t.Fatalf("one free account should claim next: %+v %v", got, err)
	}
	if _, err := s.ClaimNextRemote(ctx, "remote-node", 1, 5, full); !errors.Is(err, domain.ErrUpstreamBusy) {
		t.Fatalf("unaccounted new reservation overfilled node: %v", err)
	}
	full.Queued = 1
	full.Occupied = 0
	full.LeasedTaskIDs = nil
	if _, err := s.ClaimNextRemote(ctx, "remote-node", 1, 5, full); !errors.Is(err, domain.ErrUpstreamBusy) {
		t.Fatalf("upstream backlog should keep later task in Proxy: %v", err)
	}
	full.Queued = 0
	full.CanDispatch = false
	if _, err := s.ClaimNextRemote(ctx, "remote-node", 1, 5, full); !errors.Is(err, domain.ErrUpstreamBusy) {
		t.Fatalf("invalid login should keep later task in Proxy: %v", err)
	}
}

func TestTK2SDProxyQueueCountsOnlyUnassignedLiveTasks(t *testing.T) {
	s, _ := remoteFixture(t)
	ctx := context.Background()
	remoteQueued(t, s, "waiting", "seed-model", "queued_open", 5)
	remoteQueued(t, s, "expired", "seed-model", "queued_locked", 5)
	remoteQueued(t, s, "deleted", "seed-model", "queued_open", 5)
	v23Exec(t, s.db, `UPDATE video_tasks SET expires_at=1 WHERE task_id='expired'`)
	v23Exec(t, s.db, `UPDATE video_tasks SET deleted_at=1 WHERE task_id='deleted'`)
	count, err := s.QueuedRemoteCount(ctx)
	if err != nil || count != 1 {
		t.Fatalf("Proxy queue count=%d err=%v, want 1", count, err)
	}
}

func TestTK2SDAdmissionExcludesCoolingAccounts(t *testing.T) {
	s, _ := remoteFixture(t)
	ctx := context.Background()
	v23Exec(t, s.db, `UPDATE model_service_nodes SET max_concurrency=5 WHERE id='remote-node'`)
	remoteQueued(t, s, "waiting", "seed-model", "queued_open", 5)
	admission := domain.TK2SDAdmission{Capacity: 3, Occupied: 1, Cooling: 1, CanDispatch: true, LeasedTaskIDs: []string{"external-a"}}
	if _, err := s.ClaimNextRemote(ctx, "remote-node", 1, 5, admission); !errors.Is(err, domain.ErrUpstreamBusy) {
		t.Fatalf("cooling account is not dispatchable: %v", err)
	}
	admission.Cooling = 0
	if got, err := s.ClaimNextRemote(ctx, "remote-node", 1, 5, admission); err != nil || got.TaskID != "waiting" {
		t.Fatalf("available account should claim waiting task: %+v %v", got, err)
	}
}

func TestRemoteClaimRejectsChangedConnectionButRecoveryDoesNot(t *testing.T) {
	s, task := remoteFixture(t)
	ctx := context.Background()
	v23Exec(t, s.db, `UPDATE model_service_nodes SET max_concurrency=2,api_key_fingerprint='changed' WHERE id='remote-node'`)
	remoteQueued(t, s, "waiting", "seed-model", "queued_open", 5)
	if _, err := s.ClaimNextRemote(ctx, "remote-node", 1, 2); !errors.Is(err, domain.ErrQueueEmpty) {
		t.Fatal("stale discovered catalog used", err)
	}
	v23Exec(t, s.db, `UPDATE model_service_nodes SET enabled=0 WHERE id='remote-node'`)
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
