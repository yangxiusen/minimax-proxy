package sqlite

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

func TestStageLifecycleSynchronizesParentTaskAndCallbacks(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10})
	ctx := context.Background()
	insertNodeAPINode(t, store, "gpu-1")

	input := task("stage-parent-sync", "owner")
	input.Stages = []domain.NewTaskStage{{
		ID: "stage-generation", StageType: "generation", StageOrder: 10,
		MaxAttempts: 3, ConfigSnapshotJSON: `{}`,
	}}
	input.CallbackURLCiphertext = []byte{1}
	input.CallbackURLNonce = []byte{2}
	input.CallbackDeliveryID = "callback-queued"
	input.CallbackRequestBody = `{"task_id":"stage-parent-sync","status":"queued"}`
	input.CallbackRequestBodyHash = "queued-body-hash"
	if _, err := store.Create(ctx, input, "", nil); err != nil {
		t.Fatal(err)
	}

	stage, err := store.ClaimStage(ctx, "gpu-1", "lease-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	attempt := StageAttempt{
		ID: "attempt-1", StageID: stage.ID, AttemptNo: 1,
		OperationID: "operation-1", NodeID: "gpu-1", LeaseToken: stage.LeaseToken, Status: "dispatching",
	}
	if err := store.CreateStageAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := store.BindStageExecution(ctx, stage.ID, stage.LeaseToken, attempt.ID, "execution-1"); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(ctx, "owner", input.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusRunning || got.UpstreamID != "gpu-1" || got.ActiveStageID != stage.ID || got.StartedAt.IsZero() {
		t.Fatalf("parent task after bind = %+v", got)
	}
	active, err := store.ActiveForUpstream(ctx, "gpu-1")
	if err != nil || active.TaskID != input.TaskID {
		t.Fatalf("ActiveForUpstream() = %+v, %v", active, err)
	}
	var callbackStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT external_status FROM callback_deliveries WHERE task_id=? ORDER BY state_version DESC LIMIT 1`, input.TaskID).Scan(&callbackStatus); err != nil {
		t.Fatal(err)
	}
	if callbackStatus != "running" {
		t.Fatalf("callback external_status = %q", callbackStatus)
	}
	if err := store.CompleteStage(ctx, stage.ID, stage.LeaseToken, attempt.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx, "owner", input.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusSucceeded || got.ActiveStageID != "" {
		t.Fatalf("parent task after completion = %+v", got)
	}
	rows, err := store.db.QueryContext(ctx, `SELECT external_status FROM callback_deliveries WHERE task_id=? ORDER BY state_version`, input.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var callbackStatuses []string
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatal(err)
		}
		callbackStatuses = append(callbackStatuses, status)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(callbackStatuses) != 3 || callbackStatuses[0] != "queued" || callbackStatuses[1] != "running" || callbackStatuses[2] != "succeeded" {
		t.Fatalf("callback statuses = %v", callbackStatuses)
	}
}

func TestIntermediateCompletionAndRetryReleaseNodeAssignment(t *testing.T) {
	tests := []struct {
		name   string
		finish func(context.Context, *Store, TaskStage, StageAttempt) error
	}{
		{
			name: "intermediate completion",
			finish: func(ctx context.Context, store *Store, stage TaskStage, attempt StageAttempt) error {
				return store.CompleteStage(ctx, stage.ID, stage.LeaseToken, attempt.ID, "")
			},
		},
		{
			name: "retryable failure",
			finish: func(ctx context.Context, store *Store, stage TaskStage, attempt StageAttempt) error {
				return store.FailStage(ctx, stage.ID, stage.LeaseToken, attempt.ID, "node_execution_unavailable", "retry", time.Now().Add(time.Second), false)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10})
			ctx := context.Background()
			insertNodeAPINode(t, store, "gpu-1")
			input := task("release-node", "owner")
			input.Stages = []domain.NewTaskStage{
				{ID: "stage-1", StageType: "generation", StageOrder: 10, MaxAttempts: 3, ConfigSnapshotJSON: `{}`},
				{ID: "stage-2", StageType: "restoration", StageOrder: 20, MaxAttempts: 3, ConfigSnapshotJSON: `{}`},
			}
			if _, err := store.Create(ctx, input, "", nil); err != nil {
				t.Fatal(err)
			}
			stage, err := store.ClaimStage(ctx, "gpu-1", "lease-1", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			attempt := StageAttempt{ID: "attempt-1", StageID: stage.ID, AttemptNo: 1, OperationID: "operation-1", NodeID: "gpu-1", LeaseToken: stage.LeaseToken, Status: "dispatching"}
			if err := store.CreateStageAttempt(ctx, attempt); err != nil {
				t.Fatal(err)
			}
			if err := store.BindStageExecution(ctx, stage.ID, stage.LeaseToken, attempt.ID, "execution-1"); err != nil {
				t.Fatal(err)
			}
			if err := test.finish(ctx, store, stage, attempt); err != nil {
				t.Fatal(err)
			}
			got, err := store.Get(ctx, "owner", input.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != domain.StatusRunning || got.UpstreamID != "" || got.ActiveStageID != "" {
				t.Fatalf("parent task after release = %+v", got)
			}
			if _, err := store.ActiveForUpstream(ctx, "gpu-1"); !errors.Is(err, domain.ErrTaskNotFound) {
				t.Fatalf("ActiveForUpstream() error = %v", err)
			}
		})
	}
}

func TestAdminCancelImmediatelyFinishesUnassignedStageTask(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10})
	ctx := context.Background()
	insertNodeAPINode(t, store, "gpu-1")
	input := task("cancel-between-stages", "owner")
	input.Stages = []domain.NewTaskStage{
		{ID: "cancel-stage-1", StageType: "generation", StageOrder: 10, MaxAttempts: 3, ConfigSnapshotJSON: `{}`},
		{ID: "cancel-stage-2", StageType: "restoration", StageOrder: 20, MaxAttempts: 3, ConfigSnapshotJSON: `{}`},
	}
	if _, err := store.Create(ctx, input, "", nil); err != nil {
		t.Fatal(err)
	}
	stage, err := store.ClaimStage(ctx, "gpu-1", "lease-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	attempt := StageAttempt{ID: "cancel-attempt-1", StageID: stage.ID, AttemptNo: 1, OperationID: "cancel-operation-1", NodeID: "gpu-1", LeaseToken: stage.LeaseToken, Status: "dispatching"}
	if err := store.CreateStageAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := store.BindStageExecution(ctx, stage.ID, stage.LeaseToken, attempt.ID, "cancel-execution-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteStage(ctx, stage.ID, stage.LeaseToken, attempt.ID, ""); err != nil {
		t.Fatal(err)
	}

	if err := store.RequestAdminCancel(ctx, input.TaskID); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "owner", input.TaskID)
	if err != nil || got.Status != domain.StatusCancelled || got.FinishedAt.IsZero() {
		t.Fatalf("cancelled task = %+v, %v", got, err)
	}
	if _, err := store.ClaimStage(ctx, "gpu-1", "lease-after-cancel", time.Minute); !errors.Is(err, ErrNoClaimableStage) {
		t.Fatalf("ClaimStage() after cancel error = %v", err)
	}
}

func TestAdminCancelImmediatelyFinishesBoundStageTask(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10})
	ctx := context.Background()
	insertNodeAPINode(t, store, "gpu-1")
	input := task("cancel-bound-stage", "owner")
	input.Stages = []domain.NewTaskStage{
		{ID: "bound-stage-1", StageType: "generation", StageOrder: 10, MaxAttempts: 3, ConfigSnapshotJSON: `{}`},
		{ID: "bound-stage-2", StageType: "restoration", StageOrder: 20, MaxAttempts: 3, ConfigSnapshotJSON: `{}`},
	}
	if _, err := store.Create(ctx, input, "", nil); err != nil {
		t.Fatal(err)
	}
	stage, err := store.ClaimStage(ctx, "gpu-1", "bound-lease", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	attempt := StageAttempt{ID: "bound-attempt", StageID: stage.ID, AttemptNo: 1, OperationID: "bound-operation", NodeID: "gpu-1", LeaseToken: stage.LeaseToken, Status: "dispatching"}
	if err := store.CreateStageAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := store.BindStageExecution(ctx, stage.ID, stage.LeaseToken, attempt.ID, "bound-execution"); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestAdminCancel(ctx, input.TaskID); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "owner", input.TaskID)
	if err != nil || got.Status != domain.StatusCancelled || got.UpstreamID != "" || got.ActiveStageID != "" {
		t.Fatalf("cancelled task = %+v, %v", got, err)
	}
	var unfinished int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_stages WHERE task_id=? AND status!='cancelled'`, input.TaskID).Scan(&unfinished); err != nil {
		t.Fatal(err)
	}
	if unfinished != 0 {
		t.Fatalf("unfinished stages = %d", unfinished)
	}
	if _, err := store.CompleteStageWithOutput(ctx, input.TaskID, stage.ID, stage.LeaseToken, attempt.ID, "gpu-1", "late-node-artifact", 123, strings.Repeat("a", 64), `{}`); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("late CompleteStageWithOutput() error = %v", err)
	}
	var activeArtifacts int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_artifacts WHERE stage_id=? AND state='active'`, stage.ID).Scan(&activeArtifacts); err != nil {
		t.Fatal(err)
	}
	if activeArtifacts != 0 {
		t.Fatalf("late active artifacts = %d", activeArtifacts)
	}
}

func TestCreateStageAttemptRejectsLeaseAfterTaskCancellation(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10})
	ctx := context.Background()
	insertNodeAPINode(t, store, "gpu-1")
	input := task("cancel-before-attempt", "owner")
	input.Stages = []domain.NewTaskStage{{ID: "cancel-before-attempt-stage", StageType: "generation", StageOrder: 10, MaxAttempts: 1, ConfigSnapshotJSON: `{}`}}
	if _, err := store.Create(ctx, input, "", nil); err != nil {
		t.Fatal(err)
	}
	stage, err := store.ClaimStage(ctx, "gpu-1", "stale-lease", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RequestAdminCancel(ctx, input.TaskID); err != nil {
		t.Fatal(err)
	}
	attempt := StageAttempt{
		ID: "late-attempt", StageID: stage.ID, AttemptNo: 1, OperationID: "late-operation",
		NodeID: "gpu-1", LeaseToken: stage.LeaseToken, Status: "dispatching", RequestSnapshotJSON: `{}`,
	}
	if err := store.CreateStageAttempt(ctx, attempt); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("CreateStageAttempt() error = %v", err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stage_attempts WHERE id=?`, attempt.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("late attempt count = %d", count)
	}
}

func TestClaimStageCompletesEarlierTaskBeforeStartingLaterTask(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10})
	ctx := context.Background()
	insertNodeAPINode(t, store, "gpu-1")

	earlier := task("earlier-task", "owner")
	earlier.Stages = []domain.NewTaskStage{
		{ID: "earlier-generation", StageType: "generation", StageOrder: 10, MaxAttempts: 3, ConfigSnapshotJSON: `{}`},
		{ID: "earlier-restoration", StageType: "restoration", StageOrder: 20, MaxAttempts: 3, ConfigSnapshotJSON: `{}`},
	}
	if _, err := store.Create(ctx, earlier, "", nil); err != nil {
		t.Fatal(err)
	}
	later := task("later-task", "owner")
	later.Stages = []domain.NewTaskStage{
		{ID: "later-generation", StageType: "generation", StageOrder: 10, MaxAttempts: 3, ConfigSnapshotJSON: `{}`},
	}
	if _, err := store.Create(ctx, later, "", nil); err != nil {
		t.Fatal(err)
	}

	claimed, err := store.ClaimStage(ctx, "gpu-1", "lease-generation", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != "earlier-generation" {
		t.Fatalf("first ClaimStage() id = %q, want earlier-generation", claimed.ID)
	}
	attempt := StageAttempt{
		ID: "attempt-generation", StageID: claimed.ID, AttemptNo: 1,
		OperationID: "operation-generation", NodeID: "gpu-1", LeaseToken: claimed.LeaseToken, Status: "dispatching",
	}
	if err := store.CreateStageAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := store.BindStageExecution(ctx, claimed.ID, claimed.LeaseToken, attempt.ID, "execution-generation"); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteStage(ctx, claimed.ID, claimed.LeaseToken, attempt.ID, ""); err != nil {
		t.Fatal(err)
	}

	claimed, err = store.ClaimStage(ctx, "gpu-1", "lease-next", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != "earlier-restoration" {
		t.Fatalf("second ClaimStage() id = %q, want earlier-restoration", claimed.ID)
	}
}

func TestClaimStageSkipsEarlierTaskThatCurrentNodeCannotExecute(t *testing.T) {
	tests := []struct {
		name  string
		block func(*testing.T, *Store)
	}{
		{
			name: "preferred node differs",
			block: func(t *testing.T, store *Store) {
				if _, err := store.db.Exec(`UPDATE task_stages SET preferred_node_id='gpu-2' WHERE id='earlier-generation'`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "retry time has not arrived",
			block: func(t *testing.T, store *Store) {
				if _, err := store.db.Exec(`UPDATE task_stages SET next_attempt_at=? WHERE id='earlier-generation'`, time.Now().Add(time.Hour).UnixMilli()); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10})
			ctx := context.Background()
			insertNodeAPINode(t, store, "gpu-1")
			insertNodeAPINode(t, store, "gpu-2")

			earlier := task("earlier-task", "owner")
			earlier.Stages = []domain.NewTaskStage{{ID: "earlier-generation", StageType: "generation", StageOrder: 10, MaxAttempts: 3, ConfigSnapshotJSON: `{}`}}
			if _, err := store.Create(ctx, earlier, "", nil); err != nil {
				t.Fatal(err)
			}
			later := task("later-task", "owner")
			later.Stages = []domain.NewTaskStage{{ID: "later-generation", StageType: "generation", StageOrder: 10, MaxAttempts: 3, ConfigSnapshotJSON: `{}`}}
			if _, err := store.Create(ctx, later, "", nil); err != nil {
				t.Fatal(err)
			}
			test.block(t, store)

			claimed, err := store.ClaimStage(ctx, "gpu-1", "lease", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if claimed.ID != "later-generation" {
				t.Fatalf("ClaimStage() id = %q, want later-generation", claimed.ID)
			}
		})
	}
}

func TestClaimStageSkipsCancelledOrDeletedParentTask(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(context.Context, *Store, string) error
	}{
		{
			name: "cancelled",
			prepare: func(ctx context.Context, store *Store, taskID string) error {
				return store.RequestAdminCancel(ctx, taskID)
			},
		},
		{
			name: "cancelling",
			prepare: func(ctx context.Context, store *Store, taskID string) error {
				_, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET status='cancelling' WHERE task_id=?`, taskID)
				return err
			},
		},
		{
			name: "soft deleted",
			prepare: func(ctx context.Context, store *Store, taskID string) error {
				if err := store.RequestAdminCancel(ctx, taskID); err != nil {
					return err
				}
				return store.AdminDelete(ctx, taskID)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10})
			ctx := context.Background()
			insertNodeAPINode(t, store, "gpu-1")
			input := task("inactive-parent", "owner")
			input.Stages = []domain.NewTaskStage{{
				ID: "inactive-generation", StageType: "generation", StageOrder: 10,
				MaxAttempts: 3, ConfigSnapshotJSON: `{}`,
			}}
			if _, err := store.Create(ctx, input, "", nil); err != nil {
				t.Fatal(err)
			}
			if err := test.prepare(ctx, store, input.TaskID); err != nil {
				t.Fatal(err)
			}

			if claimed, err := store.ClaimStage(ctx, "gpu-1", "lease", time.Minute); !errors.Is(err, ErrNoClaimableStage) {
				t.Fatalf("ClaimStage() = %+v, %v; want ErrNoClaimableStage", claimed, err)
			}
		})
	}
}

func TestClaimStageAllowsTwoNodesToClaimDistinctTasks(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10})
	ctx := context.Background()
	insertNodeAPINode(t, store, "gpu-1")
	insertNodeAPINode(t, store, "gpu-2")
	for _, id := range []string{"earlier", "later"} {
		input := task(id, "owner")
		input.Stages = []domain.NewTaskStage{{
			ID: id + "-generation", StageType: "generation", StageOrder: 10,
			MaxAttempts: 3, ConfigSnapshotJSON: `{}`,
		}}
		if _, err := store.Create(ctx, input, "", nil); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	results := make(chan TaskStage, 2)
	errorsFound := make(chan error, 2)
	var workers sync.WaitGroup
	for _, nodeID := range []string{"gpu-1", "gpu-2"} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			stage, err := store.ClaimStage(ctx, nodeID, "lease-"+nodeID, time.Minute)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- stage
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	claimed := map[string]string{}
	for stage := range results {
		if previousNode, duplicate := claimed[stage.ID]; duplicate {
			t.Fatalf("stage %q claimed twice by %q and %q", stage.ID, previousNode, stage.CurrentNodeID)
		}
		claimed[stage.ID] = stage.CurrentNodeID
	}
	if len(claimed) != 2 || claimed["earlier-generation"] == "" || claimed["later-generation"] == "" {
		t.Fatalf("claimed stages = %+v", claimed)
	}
}

func TestClaimStageDoesNotSkipUnfinishedPredecessorWithinTask(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10})
	ctx := context.Background()
	insertNodeAPINode(t, store, "gpu-1")
	insertNodeAPINode(t, store, "gpu-2")
	input := task("ordered-task", "owner")
	input.Stages = []domain.NewTaskStage{
		{ID: "ordered-generation", StageType: "generation", StageOrder: 10, MaxAttempts: 3, ConfigSnapshotJSON: `{}`},
		{ID: "ordered-restoration", StageType: "restoration", StageOrder: 20, MaxAttempts: 3, ConfigSnapshotJSON: `{}`},
	}
	if _, err := store.Create(ctx, input, "", nil); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimStage(ctx, "gpu-1", "lease-first", time.Minute)
	if err != nil || first.ID != "ordered-generation" {
		t.Fatalf("first ClaimStage() = %+v, %v", first, err)
	}
	if claimed, err := store.ClaimStage(ctx, "gpu-2", "lease-second", time.Minute); !errors.Is(err, ErrNoClaimableStage) {
		t.Fatalf("second ClaimStage() = %+v, %v; want predecessor block", claimed, err)
	}
}

func TestClaimStageRestoresFIFOWhenRetryTimeArrives(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10})
	ctx := context.Background()
	insertNodeAPINode(t, store, "gpu-1")
	insertNodeAPINode(t, store, "gpu-2")
	for _, id := range []string{"earlier", "later"} {
		input := task(id, "owner")
		input.Stages = []domain.NewTaskStage{{
			ID: id + "-generation", StageType: "generation", StageOrder: 10,
			MaxAttempts: 3, ConfigSnapshotJSON: `{}`,
		}}
		if _, err := store.Create(ctx, input, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE task_stages SET next_attempt_at=? WHERE id='earlier-generation'`, time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	later, err := store.ClaimStage(ctx, "gpu-1", "lease-later", time.Minute)
	if err != nil || later.ID != "later-generation" {
		t.Fatalf("blocked retry ClaimStage() = %+v, %v", later, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE task_stages SET next_attempt_at=? WHERE id='earlier-generation'`, time.Now().Add(-time.Second).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	earlier, err := store.ClaimStage(ctx, "gpu-2", "lease-earlier", time.Minute)
	if err != nil || earlier.ID != "earlier-generation" {
		t.Fatalf("arrived retry ClaimStage() = %+v, %v", earlier, err)
	}
}

func TestClaimStageRecoversActiveTaskOnlyOnCurrentNode(t *testing.T) {
	for _, parentStatus := range []domain.InternalStatus{domain.StatusRunning, domain.StatusReconciling} {
		t.Run(string(parentStatus), func(t *testing.T) {
			store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10})
			ctx := context.Background()
			insertNodeAPINode(t, store, "gpu-1")
			insertNodeAPINode(t, store, "gpu-2")
			input := task("active-parent", "owner")
			input.Stages = []domain.NewTaskStage{{
				ID: "running-generation", StageType: "generation", StageOrder: 10,
				MaxAttempts: 3, ConfigSnapshotJSON: `{}`,
			}}
			if _, err := store.Create(ctx, input, "", nil); err != nil {
				t.Fatal(err)
			}
			now := time.Now().Add(-time.Minute).UnixMilli()
			if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET status=? WHERE task_id=?`, parentStatus, input.TaskID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE task_stages SET status='running',attempt_count=1,current_node_id='gpu-1',lease_token='expired',lease_expires_at=? WHERE id=?`, now, input.Stages[0].ID); err != nil {
				t.Fatal(err)
			}

			if claimed, err := store.ClaimStage(ctx, "gpu-2", "wrong-node", time.Minute); !errors.Is(err, ErrNoClaimableStage) {
				t.Fatalf("other node ClaimStage() = %+v, %v; want ErrNoClaimableStage", claimed, err)
			}
			claimed, err := store.ClaimStage(ctx, "gpu-1", "recovery", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if claimed.ID != input.Stages[0].ID || claimed.CurrentNodeID != "gpu-1" {
				t.Fatalf("recovered stage = %+v", claimed)
			}
		})
	}
}

func TestClaimStageQueryPlanUsesClaimAndPredecessorIndexes(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10})
	rows, err := store.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+claimStageCandidateSelect, int64(1), int64(1), "gpu-1", "gpu-1")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(details, "\n")
	t.Logf("ClaimStage query plan:\n%s", plan)
	if !strings.Contains(plan, "idx_stages_claim") {
		t.Fatalf("claim index missing from query plan:\n%s", plan)
	}
	if !strings.Contains(plan, "idx_stages_task") {
		t.Fatalf("predecessor index missing from query plan:\n%s", plan)
	}
	if strings.Contains(plan, "SCAN earlier") || strings.Contains(plan, "SCAN task") {
		t.Fatalf("parent or predecessor table scan in query plan:\n%s", plan)
	}
}
