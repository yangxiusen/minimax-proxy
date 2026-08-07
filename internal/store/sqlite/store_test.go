package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

func TestCreateProtectsQueueAndClaimsFIFO(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 3, PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	for i := 1; i <= 6; i++ {
		if _, err := store.Create(ctx, task(string(rune('0'+i)), "owner-a"), "", nil); err != nil {
			t.Fatalf("Create(%d) error = %v", i, err)
		}
	}

	for i := 1; i <= 6; i++ {
		got, err := store.Get(ctx, "owner-a", string(rune('0'+i)))
		if err != nil {
			t.Fatal(err)
		}
		want := domain.StatusQueuedOpen
		if i <= 3 {
			want = domain.StatusQueuedLocked
		}
		if got.Status != want {
			t.Errorf("task %d status = %s, want %s", i, got.Status, want)
		}
	}
	if _, err := store.CancelOrDelete(ctx, "owner-a", "2"); !errors.Is(err, domain.ErrTaskNotOperable) {
		t.Fatalf("cancel locked error = %v", err)
	}
	claimed, err := store.ClaimNext(ctx, "gpu-1")
	if err != nil {
		t.Fatal(err)
	}
	if claimed.TaskID != "1" || claimed.Status != domain.StatusDispatching {
		t.Fatalf("claimed = %+v", claimed)
	}
	if _, err := store.ClaimNext(ctx, "gpu-1"); !errors.Is(err, domain.ErrUpstreamBusy) {
		t.Fatalf("second claim error = %v", err)
	}
	four, _ := store.Get(ctx, "owner-a", "4")
	if four.Status != domain.StatusQueuedLocked {
		t.Fatalf("task 4 status = %s", four.Status)
	}
}

func TestCreateIsIdempotentAndRejectsConflict(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	first, err := store.Create(ctx, task("first", "owner-a"), "idem-hash", nil)
	if err != nil {
		t.Fatal(err)
	}
	replay := task("other-id", "owner-a")
	replay.RequestHash = first.RequestHash
	second, err := store.Create(ctx, replay, "idem-hash", nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.TaskID != first.TaskID {
		t.Fatalf("replay task_id = %s, want %s", second.TaskID, first.TaskID)
	}
	conflict := task("conflict", "owner-a")
	conflict.RequestHash = "different"
	if _, err := store.Create(ctx, conflict, "idem-hash", nil); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestCreateChecksAvailabilityOnlyAfterIdempotencyResolution(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	available := true
	availabilityCalls := 0
	checkAvailable := func() bool {
		availabilityCalls++
		return available
	}
	firstInput := task("first", "owner-a")
	first, err := store.Create(ctx, firstInput, "idem-hash", checkAvailable)
	if err != nil {
		t.Fatal(err)
	}

	available = false
	replayInput := task("replay-id", "owner-a")
	replayInput.RequestHash = firstInput.RequestHash
	replay, err := store.Create(ctx, replayInput, "idem-hash", checkAvailable)
	if err != nil || replay.TaskID != first.TaskID {
		t.Fatalf("replay = %+v, error = %v", replay, err)
	}
	conflict := task("conflict", "owner-a")
	conflict.RequestHash = "different"
	if _, err := store.Create(ctx, conflict, "idem-hash", checkAvailable); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	if _, err := store.Create(ctx, task("unavailable", "owner-a"), "new-key", checkAvailable); !errors.Is(err, domain.ErrResourceUnavailable) {
		t.Fatalf("unavailable error = %v", err)
	}
	if availabilityCalls != 2 {
		t.Fatalf("availability calls = %d, want 2", availabilityCalls)
	}
	if _, err := store.Get(ctx, "owner-a", "unavailable"); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Fatalf("unavailable task was persisted: %v", err)
	}
	items, total, err := store.List(ctx, "owner-a", domain.TaskFilter{PageNum: 1, PageSize: 20})
	if err != nil || total != 1 || len(items) != 1 || items[0].TaskID != first.TaskID {
		t.Fatalf("persisted tasks = %+v total=%d error=%v", items, total, err)
	}
}

func TestCreateEnforcesPerKeyAndGlobalLimits(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 2, GlobalLimit: 3})
	ctx := context.Background()
	for _, item := range []domain.NewTask{task("a1", "owner-a"), task("a2", "owner-a"), task("b1", "owner-b")} {
		if _, err := store.Create(ctx, item, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Create(ctx, task("a3", "owner-a"), "", nil); !errors.Is(err, domain.ErrPerKeyLimit) {
		t.Fatalf("per-key error = %v", err)
	}
	if _, err := store.Create(ctx, task("b2", "owner-b"), "", nil); !errors.Is(err, domain.ErrGlobalLimit) {
		t.Fatalf("global error = %v", err)
	}
}

func TestOwnershipDeleteAndRetentionAreEnforced(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100, Now: func() time.Time { return now }, Retention: 7 * 24 * time.Hour})
	ctx := context.Background()
	if _, err := store.Create(ctx, task("owned", "owner-a"), "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "owner-b", "owned"); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Fatalf("cross owner error = %v", err)
	}
	if _, err := store.ClaimNext(ctx, "gpu-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSucceeded(ctx, "owned", "gpu-1", "http://internal/video.mp4", "https://public/video.mp4", "16:9"); err != nil {
		t.Fatal(err)
	}
	action, err := store.CancelOrDelete(ctx, "owner-a", "owned")
	if err != nil || action != domain.ActionDeleted {
		t.Fatalf("delete = %s, %v", action, err)
	}
	if _, err := store.Get(ctx, "owner-a", "owned"); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Fatalf("deleted get error = %v", err)
	}
}

func TestListFiltersOwnerStatusAndPaginates(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	for _, item := range []domain.NewTask{task("a1", "owner-a"), task("a2", "owner-a"), task("b1", "owner-b")} {
		if _, err := store.Create(ctx, item, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ClaimNext(ctx, "gpu-1"); err != nil {
		t.Fatal(err)
	}

	items, total, err := store.List(ctx, "owner-a", domain.TaskFilter{Status: domain.V2Queued, PageNum: 1, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].TaskID != "a2" {
		t.Fatalf("queued list = %+v, total=%d", items, total)
	}
	running, total, err := store.List(ctx, "owner-a", domain.TaskFilter{Status: domain.V2Running, PageNum: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(running) != 1 || running[0].TaskID != "a1" {
		t.Fatalf("running list = %+v, total=%d", running, total)
	}
}

func TestCleanupLogicallyDeletesExpiredTasksAndIdempotency(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100, Now: func() time.Time { return now }, Retention: 7 * 24 * time.Hour, IdempotencyTTL: 24 * time.Hour})
	ctx := context.Background()
	if _, err := store.Create(ctx, task("old", "owner-a"), "idem", nil); err != nil {
		t.Fatal(err)
	}
	now = now.Add(8 * 24 * time.Hour)
	tasks, keys, err := store.CleanupExpired(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || keys != 1 {
		t.Fatalf("cleanup tasks=%d keys=%d", tasks, keys)
	}
	if _, err := store.Get(ctx, "owner-a", "old"); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Fatalf("get expired error = %v", err)
	}
	newTask := task("new", "owner-a")
	newTask.RequestHash = "hash-old"
	got, err := store.Create(ctx, newTask, "idem", nil)
	if err != nil || got.TaskID != "new" {
		t.Fatalf("recreate = %+v, %v", got, err)
	}
}

func TestClaimAndCancelHaveSingleWinner(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("race-%03d", i)
		if _, err := store.Create(ctx, task(id, "owner-a"), "", nil); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		claimResult := make(chan error, 1)
		cancelResult := make(chan error, 1)
		go func() { <-start; _, err := store.ClaimNext(ctx, "gpu-1"); claimResult <- err }()
		go func() { <-start; _, err := store.CancelOrDelete(ctx, "owner-a", id); cancelResult <- err }()
		close(start)
		claimErr, cancelErr := <-claimResult, <-cancelResult
		if (claimErr == nil) == (cancelErr == nil) {
			t.Fatalf("iteration %d claim=%v cancel=%v", i, claimErr, cancelErr)
		}
		if claimErr == nil {
			if err := store.MarkFailed(ctx, id, "gpu-1", "test", "test"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func newStore(t *testing.T, options Options) *Store {
	t.Helper()
	if options.Retention == 0 {
		options.Retention = 7 * 24 * time.Hour
	}
	if options.IdempotencyTTL == 0 {
		options.IdempotencyTTL = 24 * time.Hour
	}
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func task(id, owner string) domain.NewTask {
	return domain.NewTask{
		TaskID: id, APIKeyID: owner, Model: "MiniMax-H3", Scenario: "t2va",
		RequestJSON: `{"model":"MiniMax-H3"}`, RequestHash: "hash-" + id,
		Resolution: "2K", Duration: 5, Ratio: "16:9",
	}
}
