package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

func TestClaimNextRequiresCurrentEnabledNodeVersion(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	if _, err := store.CreateModelNode(ctx, modelNodeInput("gpu-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, task("first", "owner"), "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNext(ctx, "gpu-1", 1); err != nil {
		t.Fatalf("current claim error = %v", err)
	}
	if err := store.MarkFailed(ctx, "first", "gpu-1", "test", "test"); err != nil {
		t.Fatal(err)
	}
	disabled := modelNodeInput("gpu-1")
	disabled.Enabled = false
	if _, err := store.UpdateModelNode(ctx, "gpu-1", 1, disabled); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, task("second", "owner"), "", nil); err != nil {
		t.Fatal(err)
	}
	for _, version := range []int64{1, 2} {
		if _, err := store.ClaimNext(ctx, "gpu-1", version); !errors.Is(err, domain.ErrNodeConfigStale) {
			t.Fatalf("version %d claim error = %v", version, err)
		}
	}
}

func TestClaimAndNodeUpdateHaveSingleWinner(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 100, GlobalLimit: 100})
	ctx := context.Background()
	for iteration := 0; iteration < 20; iteration++ {
		nodeID := fmt.Sprintf("gpu-%02d", iteration)
		taskID := fmt.Sprintf("claim-config-%02d", iteration)
		if _, err := store.CreateModelNode(ctx, modelNodeInput(nodeID)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Create(ctx, task(taskID, "owner"), "", nil); err != nil {
			t.Fatal(err)
		}
		changed := modelNodeInput(nodeID)
		changed.BaseURL = "http://127.0.0.2:7860"
		start := make(chan struct{})
		claimResult := make(chan error, 1)
		updateResult := make(chan error, 1)
		go func() {
			<-start
			_, err := store.ClaimNext(ctx, nodeID, 1)
			claimResult <- err
		}()
		go func() {
			<-start
			_, err := store.UpdateModelNode(ctx, nodeID, 1, changed)
			updateResult <- err
		}()
		close(start)
		claimErr, updateErr := <-claimResult, <-updateResult
		if (claimErr == nil) == (updateErr == nil) {
			t.Fatalf("iteration %d claim=%v update=%v", iteration, claimErr, updateErr)
		}
		if claimErr == nil {
			if !errors.Is(updateErr, domain.ErrNodeHasActiveTask) {
				t.Fatalf("iteration %d update error = %v", iteration, updateErr)
			}
			if err := store.MarkFailed(ctx, taskID, nodeID, "test", "test"); err != nil {
				t.Fatal(err)
			}
		} else {
			if !errors.Is(claimErr, domain.ErrNodeConfigStale) {
				t.Fatalf("iteration %d claim error = %v", iteration, claimErr)
			}
			if _, err := store.CancelOrDelete(ctx, "owner", taskID); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestModelNodeCRUDEnforcesVersionActivityAndPermanentID(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100, Now: func() time.Time { return now }})
	ctx := context.Background()

	created, err := store.CreateModelNode(ctx, modelNodeInput("gpu-1"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || !created.Enabled || !created.CreatedAt.Equal(now) {
		t.Fatalf("created = %+v", created)
	}
	if _, err := store.CreateModelNode(ctx, modelNodeInput("gpu-1")); !errors.Is(err, domain.ErrNodeIDConflict) {
		t.Fatalf("duplicate error = %v", err)
	}

	updatedInput := modelNodeInput("gpu-1")
	updatedInput.RequestTimeout = 45 * time.Second
	now = now.Add(time.Second)
	updated, err := store.UpdateModelNode(ctx, "gpu-1", 1, updatedInput)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.RequestTimeout != 45*time.Second || !updated.UpdatedAt.Equal(now) {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := store.UpdateModelNode(ctx, "gpu-1", 1, updatedInput); !errors.Is(err, domain.ErrNodeVersionConflict) {
		t.Fatalf("stale update error = %v", err)
	}

	if _, err := store.Create(ctx, task("active", "owner"), "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET status='running',upstream_id='gpu-1' WHERE task_id='active'`); err != nil {
		t.Fatal(err)
	}
	changed := updatedInput
	changed.BaseURL = "http://127.0.0.2:7860"
	if _, err := store.UpdateModelNode(ctx, "gpu-1", 2, changed); !errors.Is(err, domain.ErrNodeHasActiveTask) {
		t.Fatalf("active connection update error = %v", err)
	}
	disabled := updatedInput
	disabled.Enabled = false
	disabledNode, err := store.UpdateModelNode(ctx, "gpu-1", 2, disabled)
	if err != nil || disabledNode.Enabled || disabledNode.Version != 3 {
		t.Fatalf("disable = %+v, %v", disabledNode, err)
	}
	if err := store.DeleteModelNode(ctx, "gpu-1", 3); !errors.Is(err, domain.ErrNodeHasActiveTask) {
		t.Fatalf("active delete error = %v", err)
	}
	if err := store.MarkFailed(ctx, "active", "gpu-1", "test", "test"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteModelNode(ctx, "gpu-1", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetModelNode(ctx, "gpu-1"); !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("deleted get error = %v", err)
	}
	if _, err := store.CreateModelNode(ctx, modelNodeInput("gpu-1")); !errors.Is(err, domain.ErrNodeIDConflict) {
		t.Fatalf("reused id error = %v", err)
	}
}

func TestUpdateNodeAPINodeAllowsDisableWithActiveTask(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	node := domain.ModelNodeInput{
		ID: "node-1", ServiceURL: "http://127.0.0.1:7860", ProtocolVersion: "h3-node-v1",
		APIKeyCiphertext: []byte("ciphertext"), APIKeyNonce: []byte("nonce"), APIKeyFingerprint: "sha256:test",
		PollInterval: 3 * time.Second, RequestTimeout: 30 * time.Second, Enabled: true,
	}
	if _, err := store.CreateModelNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, task("active", "owner"), "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET status='running',upstream_id='node-1' WHERE task_id='active'`); err != nil {
		t.Fatal(err)
	}

	node.Enabled = false
	disabled, err := store.UpdateModelNode(ctx, "node-1", 1, node)
	if err != nil || disabled.Enabled || disabled.Version != 2 {
		t.Fatalf("disable = %+v, %v", disabled, err)
	}
}

func TestImportLegacyNodesIsAtomicAndRunsOnlyOnce(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()

	pending, err := store.LegacyNodeImportPending(ctx)
	if err != nil || !pending {
		t.Fatalf("pending = %v, %v", pending, err)
	}
	count, imported, err := store.ImportLegacyNodes(ctx, []domain.ModelNodeInput{modelNodeInput("gpu-1"), modelNodeInput("gpu-2")})
	if err != nil || !imported || count != 2 {
		t.Fatalf("import = %d, %v, %v", count, imported, err)
	}
	pending, err = store.LegacyNodeImportPending(ctx)
	if err != nil || pending {
		t.Fatalf("pending after import = %v, %v", pending, err)
	}
	count, imported, err = store.ImportLegacyNodes(ctx, []domain.ModelNodeInput{modelNodeInput("gpu-3")})
	if err != nil || imported || count != 2 {
		t.Fatalf("reimport = %d, %v, %v", count, imported, err)
	}
	nodes, err := store.ListModelNodes(ctx)
	if err != nil || len(nodes) != 2 || nodes[0].ID != "gpu-1" || nodes[1].ID != "gpu-2" {
		t.Fatalf("nodes = %+v, %v", nodes, err)
	}
}

func TestImportLegacyNodesDoesNotMergeIntoExistingDatabase(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	if _, err := store.CreateModelNode(ctx, modelNodeInput("managed")); err != nil {
		t.Fatal(err)
	}

	count, imported, err := store.ImportLegacyNodes(ctx, []domain.ModelNodeInput{modelNodeInput("legacy")})
	if err != nil || imported || count != 0 {
		t.Fatalf("import = %d, %v, %v", count, imported, err)
	}
	nodes, err := store.ListModelNodes(ctx)
	if err != nil || len(nodes) != 1 || nodes[0].ID != "managed" {
		t.Fatalf("nodes = %+v, %v", nodes, err)
	}
}

func TestH3NodeUsesEmptyKeyIDCompatibilityValue(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	input := domain.ModelNodeInput{
		ID: "node-1", ServiceURL: "http://127.0.0.1:7860", ProtocolVersion: "h3-node-v1",
		APIKeyCiphertext: []byte("ciphertext"), APIKeyNonce: []byte("nonce"), APIKeyFingerprint: "sha256:test",
		PollInterval: 3 * time.Second, RequestTimeout: 30 * time.Second, Enabled: true,
	}

	if _, err := store.CreateModelNode(ctx, input); err != nil {
		t.Fatal(err)
	}
	var compatibilityValue string
	if err := store.db.QueryRowContext(ctx, `SELECT api_key_id FROM model_service_nodes WHERE id='node-1'`).Scan(&compatibilityValue); err != nil {
		t.Fatal(err)
	}
	if compatibilityValue != "" {
		t.Fatalf("api_key_id=%q, want empty compatibility value", compatibilityValue)
	}
	if _, ok := reflect.TypeOf(domain.ModelNodeInput{}).FieldByName("APIKeyID"); ok {
		t.Fatal("ModelNodeInput must not expose APIKeyID")
	}
}

func TestHistoricalH3KeyIDIsIgnoredWhenReadingNode(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	input := domain.ModelNodeInput{
		ID: "node-1", ServiceURL: "http://127.0.0.1:7860", ProtocolVersion: "h3-node-v1",
		APIKeyCiphertext: []byte("ciphertext"), APIKeyNonce: []byte("nonce"), APIKeyFingerprint: "sha256:test",
		PollInterval: 3 * time.Second, RequestTimeout: 30 * time.Second, Enabled: true,
	}
	if _, err := store.CreateModelNode(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE model_service_nodes SET api_key_id='historical-id' WHERE id='node-1'`); err != nil {
		t.Fatal(err)
	}

	node, err := store.GetModelNode(ctx, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	if node.APIKeyFingerprint != "sha256:test" || string(node.APIKeyCiphertext) != "ciphertext" {
		t.Fatalf("node credentials changed: %+v", node)
	}
}

func modelNodeInput(id string) domain.ModelNodeInput {
	return domain.ModelNodeInput{
		ID: id, BaseURL: "http://127.0.0.1:7860", JobsBaseURL: "http://127.0.0.1:8188",
		PublicBaseURL: "https://video.example.com", HealthPath: "/",
		SubmitAPIName: "submit_minimax_from_slots", CheckAPIName: "check_and_get_video",
		PollInterval: 3 * time.Second, RequestTimeout: 30 * time.Second, Enabled: true,
	}
}
