package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"minimax-h3-tc/internal/domain"
	"testing"
	"time"
)

func TestClaimNextNeverCrossesFrozenProtocol(t *testing.T) {
	s := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	in := task("tk", "owner")
	in.ProtocolVersion = domain.ProtocolTK2SD
	in.RequestNormalizer = domain.ProtocolTK2SD
	snapshot, _ := json.Marshal(domain.RouteSnapshot{SchemaVersion: 1, Model: in.Model, ProtocolVersion: in.ProtocolVersion, Requirements: domain.TaskRequirements{Scenario: "t2va", Duration: 5}})
	in.RoutingSnapshotJSON = string(snapshot)
	if _, err := s.Create(ctx, in, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNext(ctx, "gpu-1"); !errors.Is(err, domain.ErrQueueEmpty) {
		t.Fatalf("legacy stole other protocol: %v", err)
	}
}

func TestProtectedSlotsExcludeUnresolvedRoutes(t *testing.T) {
	s := newStore(t, Options{ProtectedSlots: 1, PerKeyLimit: 20, GlobalLimit: 20})
	ctx := context.Background()
	v22Task(t, s.db, "unresolved", "queued_locked")
	if _, err := s.Create(ctx, task("ready", "owner"), "", nil); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]domain.InternalStatus{"unresolved": domain.StatusQueuedOpen, "ready": domain.StatusQueuedLocked} {
		got, err := s.GetTaskForExecution(ctx, id)
		if err != nil || got.Status != want {
			t.Fatalf("%s state=%s err=%v", id, got.Status, err)
		}
	}
	if _, err := s.CancelOrDelete(ctx, "owner", "unresolved"); err != nil {
		t.Fatal(err)
	}
}

func TestStageClaimChecksCatalogBeyondFirstHundredTasks(t *testing.T) {
	s := newStore(t, Options{PerKeyLimit: 200, GlobalLimit: 200})
	ctx := context.Background()
	insertNodeAPINode(t, s, "h3")
	for i := 0; i < 102; i++ {
		in := task(fmt.Sprintf("stage-%03d", i), "owner")
		in.ProtocolVersion = domain.ProtocolH3
		if i < 101 {
			in.Model = "unsupported"
		}
		in.Stages = []domain.NewTaskStage{{ID: in.TaskID, StageType: "generation", MaxAttempts: 2, ConfigSnapshotJSON: `{}`}}
		if _, err := s.Create(ctx, in, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ClaimStage(ctx, "h3", "lease", time.Minute)
	if err != nil || got.TaskID != "stage-101" {
		t.Fatalf("claimed=%s err=%v", got.TaskID, err)
	}
}

func TestStageRecoveryKeepsOriginalNodeWithDisabledCatalog(t *testing.T) {
	s := newStore(t, Options{PerKeyLimit: 20, GlobalLimit: 20})
	ctx := context.Background()
	insertNodeAPINode(t, s, "h3-one")
	insertNodeAPINode(t, s, "h3-two")
	in := task("recover", "owner")
	in.ProtocolVersion = domain.ProtocolH3
	in.Stages = []domain.NewTaskStage{{ID: "recover-stage", StageType: "generation", MaxAttempts: 2, ConfigSnapshotJSON: `{}`}}
	if _, err := s.Create(ctx, in, "", nil); err != nil {
		t.Fatal(err)
	}
	stage, err := s.ClaimStage(ctx, "h3-one", "lease", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateStageAttempt(ctx, StageAttempt{ID: "attempt", StageID: stage.ID, AttemptNo: 1, OperationID: "operation", NodeID: "h3-one", LeaseToken: stage.LeaseToken, Status: "dispatching"}); err != nil {
		t.Fatal(err)
	}
	if active, err := s.ActiveForUpstream(ctx, "h3-one"); err != nil || active.TaskID != in.TaskID {
		t.Fatalf("unbound recovery ownership=%s err=%v", active.TaskID, err)
	}
	v22Exec(t, s.db, `UPDATE task_stages SET lease_expires_at=0; UPDATE model_service_nodes SET enabled=0 WHERE id='h3-one'; UPDATE node_models SET enabled=0 WHERE node_id='h3-one'`)
	if got, err := s.ClaimStage(ctx, "h3-two", "wrong", time.Minute); !errors.Is(err, ErrNoClaimableStage) {
		t.Fatalf("wrong node claimed %s: %v", got.ID, err)
	}
	if got, err := s.ClaimStage(ctx, "h3-one", "recovery", time.Minute); err != nil || got.ID != stage.ID {
		t.Fatalf("original recovery %s: %v", got.ID, err)
	}
}

func TestStageClaimRejectsDisabledNodeAndOfficialStages(t *testing.T) {
	for _, mode := range []string{"disabled", "official"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 10})
			ctx := context.Background()
			insertNodeAPINode(t, s, "node")
			in := task("task", "owner")
			in.ProtocolVersion = domain.ProtocolH3
			if mode == "official" {
				in.ProtocolVersion = domain.ProtocolOfficial
				v22Exec(t, s.db, `UPDATE model_service_nodes SET protocol_version='minimax-v2' WHERE id='node'`)
			}
			in.Stages = []domain.NewTaskStage{{ID: "stage", StageType: "generation", MaxAttempts: 1, ConfigSnapshotJSON: `{}`}}
			if _, err := s.Create(ctx, in, "", nil); err != nil {
				t.Fatal(err)
			}
			if mode == "disabled" {
				v22Exec(t, s.db, `UPDATE model_service_nodes SET enabled=0 WHERE id='node'`)
			}
			if _, err := s.ClaimStage(ctx, "node", "lease", time.Minute); !errors.Is(err, ErrNoClaimableStage) {
				t.Fatalf("claim=%v", err)
			}
		})
	}
}

func TestOfficialRecoveryIsFrozenAndIgnoresCatalogClosure(t *testing.T) {
	s := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 10})
	ctx := context.Background()
	n, err := s.CreateModelNode(ctx, officialNodeInput("official", 2, false))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{domain.ProtocolOfficial, domain.ProtocolTK2SD} {
		in := task(p, "owner")
		in.ProtocolVersion = p
		if _, err := s.Create(ctx, in, "", nil); err != nil {
			t.Fatal(err)
		}
		v22Exec(t, s.db, `UPDATE video_tasks SET upstream_id=?,upstream_slot_active=1,status='running' WHERE task_id=?`, n.ID, p)
	}
	v22Exec(t, s.db, `UPDATE model_service_nodes SET enabled=0; UPDATE node_models SET enabled=0`)
	got, err := s.ListActiveOfficialTasks(ctx, n.ID)
	if err != nil || len(got) != 1 || got[0].TaskID != domain.ProtocolOfficial {
		t.Fatalf("recovery=%+v err=%v", got, err)
	}
	if err := s.MarkOfficialFailed(ctx, domain.ProtocolTK2SD, n.ID, "wrong", "wrong", nil); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("cross-protocol terminal update=%v", err)
	}
}

func TestStageAttemptRechecksEligibilityAfterLease(t *testing.T) {
	s := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 10})
	ctx := context.Background()
	insertNodeAPINode(t, s, "h3")
	in := task("task", "owner")
	in.ProtocolVersion = domain.ProtocolH3
	in.Stages = []domain.NewTaskStage{{ID: "stage", StageType: "generation", MaxAttempts: 2, ConfigSnapshotJSON: `{}`}}
	if _, err := s.Create(ctx, in, "", nil); err != nil {
		t.Fatal(err)
	}
	stage, err := s.ClaimStage(ctx, "h3", "lease", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	v22Exec(t, s.db, `UPDATE node_models SET enabled=0`)
	err = s.CreateStageAttempt(ctx, StageAttempt{ID: "attempt", StageID: stage.ID, NodeID: "h3", LeaseToken: stage.LeaseToken, AttemptNo: 1, OperationID: "operation", Status: "dispatching"})
	if !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("attempt after catalog closure=%v", err)
	}
}

func TestStageRetryPreservesProtocolAndDoesNotEnterOfficialQueue(t *testing.T) {
	s := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 10})
	ctx := context.Background()
	insertNodeAPINode(t, s, "h3")
	n, err := s.CreateModelNode(ctx, officialNodeInput("official", 1, false))
	if err != nil {
		t.Fatal(err)
	}
	in := task("task", "owner")
	in.ProtocolVersion = domain.ProtocolH3
	in.Stages = []domain.NewTaskStage{{ID: "stage", StageType: "generation", MaxAttempts: 2, ConfigSnapshotJSON: `{}`}}
	before, err := s.Create(ctx, in, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := s.ClaimStage(ctx, "h3", "lease", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateStageAttempt(ctx, StageAttempt{ID: "attempt", StageID: stage.ID, NodeID: "h3", LeaseToken: stage.LeaseToken, AttemptNo: 1, OperationID: "operation", Status: "dispatching"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FailStage(ctx, stage.ID, stage.LeaseToken, "attempt", "temporary", "temporary", time.Now().Add(-time.Second), false); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTaskForExecution(ctx, in.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProtocolVersion != domain.ProtocolH3 || got.RoutingSnapshotJSON != before.RoutingSnapshotJSON {
		t.Fatal("retry changed frozen route")
	}
	if _, err := s.ClaimNextOfficial(ctx, n.ID, n.Version, 1); !errors.Is(err, domain.ErrQueueEmpty) {
		t.Fatalf("official stole retry=%v", err)
	}
	if _, err := s.ClaimStage(ctx, "h3", "retry", time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyRequeuePreservesFrozenProtocolAfterNewRouteAppears(t *testing.T) {
	s := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 10})
	ctx := context.Background()
	in := task("legacy-retry", "owner")
	in.ProtocolVersion = domain.ProtocolLegacy
	before, err := s.Create(ctx, in, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNext(ctx, "legacy-node"); err != nil {
		t.Fatal(err)
	}
	n, err := s.CreateModelNode(ctx, officialNodeInput("official", 1, false))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Requeue(ctx, in.TaskID, "legacy-node"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTaskForExecution(ctx, in.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProtocolVersion != domain.ProtocolLegacy || got.RoutingSnapshotJSON != before.RoutingSnapshotJSON {
		t.Fatal("requeue changed frozen protocol")
	}
	if _, err := s.ClaimNextOfficial(ctx, n.ID, n.Version, 1); !errors.Is(err, domain.ErrQueueEmpty) {
		t.Fatalf("official stole requeued legacy task: %v", err)
	}
	if _, err := s.ClaimNext(ctx, "legacy-node"); err != nil {
		t.Fatal(err)
	}
}
