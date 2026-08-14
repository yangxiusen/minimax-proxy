package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

func TestNodeDispatchBarrierPersistsAndUsesRowVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "proxy.db")
	options := Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10, Retention: 7 * 24 * time.Hour, IdempotencyTTL: 24 * time.Hour}
	store, err := Open(ctx, path, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	insertNodeAPINode(t, store, "gpu-1")
	insertNodeAPINode(t, store, "gpu-2")
	input := task("barrier-task", "owner")
	input.Stages = []domain.NewTaskStage{{ID: "barrier-stage", StageType: "generation", StageOrder: 10, MaxAttempts: 1, ConfigSnapshotJSON: `{}`}}
	input.CallbackURLCiphertext = []byte{1}
	input.CallbackURLNonce = []byte{2}
	input.CallbackDeliveryID = "barrier-callback-queued"
	input.CallbackRequestBody = `{"task_id":"barrier-task","status":"queued"}`
	input.CallbackRequestBodyHash = "barrier-callback-hash"
	if _, err := store.Create(ctx, input, "", nil); err != nil {
		t.Fatal(err)
	}
	next := task("barrier-next-task", "owner")
	next.Stages = []domain.NewTaskStage{{ID: "barrier-next-stage", StageType: "generation", StageOrder: 10, MaxAttempts: 1, ConfigSnapshotJSON: `{}`}}
	if _, err := store.Create(ctx, next, "", nil); err != nil {
		t.Fatal(err)
	}
	stage, err := store.ClaimStage(ctx, "gpu-1", "lease-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	attempt := StageAttempt{ID: "barrier-attempt", StageID: stage.ID, AttemptNo: 1, OperationID: "stage-submit-barrier", NodeID: "gpu-1", LeaseToken: stage.LeaseToken, Status: "dispatching", RequestSnapshotJSON: `{"operation_id":"stage-submit-barrier"}`}
	if err := store.CreateStageAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestAdminCancel(ctx, input.TaskID); err != nil {
		t.Fatal(err)
	}
	var taskStatus, stageStatus, attemptStatus, callbackStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM video_tasks WHERE task_id=?`, input.TaskID).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM task_stages WHERE id=?`, stage.ID).Scan(&stageStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM stage_attempts WHERE id=?`, attempt.ID).Scan(&attemptStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT external_status FROM callback_deliveries WHERE task_id=? ORDER BY state_version DESC LIMIT 1`, input.TaskID).Scan(&callbackStatus); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "cancelled" || stageStatus != "cancelled" || attemptStatus != "failed" || callbackStatus != "cancelled" {
		t.Fatalf("cancel transaction states task=%q stage=%q attempt=%q callback=%q", taskStatus, stageStatus, attemptStatus, callbackStatus)
	}
	if _, err := store.ClaimStage(ctx, "gpu-1", "must-not-claim", time.Minute); !errors.Is(err, ErrNoClaimableStage) {
		t.Fatalf("ClaimStage() with barrier error = %v", err)
	}
	queued, err := store.Get(ctx, "owner", next.TaskID)
	if err != nil || queued.Status != domain.StatusQueuedOpen {
		t.Fatalf("next task = %+v, %v", queued, err)
	}
	claimedByOtherNode, err := store.ClaimStage(ctx, "gpu-2", "other-node-lease", time.Minute)
	if err != nil || claimedByOtherNode.TaskID != next.TaskID {
		t.Fatalf("other node claim = %+v, %v", claimedByOtherNode, err)
	}

	barrier, err := store.GetNodeDispatchBarrier(ctx, "gpu-1")
	if err != nil {
		t.Fatal(err)
	}
	if barrier.AttemptID != attempt.ID || barrier.OperationID != attempt.OperationID || barrier.ExecutionID != "" {
		t.Fatalf("barrier = %+v", barrier)
	}
	if err := store.BindBarrierExecution(ctx, "gpu-1", barrier.RowVersion+1, "execution-wrong-version"); !errors.Is(err, ErrNodeDispatchBarrierConflict) {
		t.Fatalf("wrong version bind error = %v", err)
	}
	if err := store.BindBarrierExecution(ctx, "gpu-1", barrier.RowVersion, "execution-1"); err != nil {
		t.Fatal(err)
	}
	barrier, err = store.GetNodeDispatchBarrier(ctx, "gpu-1")
	if err != nil || barrier.ExecutionID != "execution-1" {
		t.Fatalf("bound barrier = %+v, %v", barrier, err)
	}
	retryAt := time.UnixMilli(123456)
	if err := store.DeferNodeDispatchBarrier(ctx, "gpu-1", barrier.RowVersion, "node_cancel_unavailable", retryAt); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path, options)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err = store.GetNodeDispatchBarrier(ctx, "gpu-1")
	if err != nil || barrier.RetryCount != 1 || barrier.NextRetryAt != retryAt.UnixMilli() {
		t.Fatalf("reopened barrier = %+v, %v", barrier, err)
	}
	if err := store.ResolveNodeDispatchBarrier(ctx, "gpu-1", barrier.RowVersion); err != nil {
		t.Fatal(err)
	}
	hasBarrier, err := store.HasNodeDispatchBarrier(ctx, "gpu-1")
	if err != nil || hasBarrier {
		t.Fatalf("HasNodeDispatchBarrier() = %v, %v", hasBarrier, err)
	}
}
