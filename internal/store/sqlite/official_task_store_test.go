package sqlite

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

func TestClaimNextOfficialHonorsConfiguredCapacity(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 100, GlobalLimit: 100})
	ctx := context.Background()
	node, err := store.CreateModelNode(ctx, officialNodeInput("official", 3, false))
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 4; index++ {
		if _, err := store.Create(ctx, officialTask(fmt.Sprintf("official-%d", index), false, false), "", nil); err != nil {
			t.Fatal(err)
		}
	}
	for index := 1; index <= 3; index++ {
		claimed, err := store.ClaimNextOfficial(ctx, node.ID, node.Version, node.MaxConcurrency)
		if err != nil || !claimed.UpstreamSlotActive || claimed.UpstreamNodeVersion != node.Version {
			t.Fatalf("claim %d=%+v err=%v", index, claimed, err)
		}
	}
	if _, err := store.ClaimNextOfficial(ctx, node.ID, node.Version, node.MaxConcurrency); !errors.Is(err, domain.ErrUpstreamBusy) {
		t.Fatalf("fourth claim error=%v", err)
	}
	remaining, err := store.Get(ctx, "owner", "official-4")
	if err != nil || remaining.Status != domain.StatusQueuedOpen {
		t.Fatalf("remaining=%+v err=%v", remaining, err)
	}
}

func TestClaimNextOfficialSkipsLocalOnlyTasks(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 100, GlobalLimit: 100})
	ctx := context.Background()
	node, err := store.CreateModelNode(ctx, officialNodeInput("official", 3, false))
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []domain.NewTask{
		officialTask("with-lora", true, false),
		officialTask("with-restoration", false, true),
		func() domain.NewTask {
			value := officialTask("unsupported-resolution", false, false)
			value.Resolution = "480P"
			return value
		}(),
		officialTask("compatible", false, false),
	} {
		if _, err := store.Create(ctx, input, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	claimed, err := store.ClaimNextOfficial(ctx, node.ID, node.Version, node.MaxConcurrency)
	if err != nil || claimed.TaskID != "compatible" {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
}

func TestMarkOfficialGeneratedPreservesOriginalAndReleasesSlot(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	node, _ := store.CreateModelNode(ctx, officialNodeInput("official", 1, true))
	if _, err := store.Create(ctx, officialTask("delivery", false, false), "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNextOfficial(ctx, node.ID, node.Version, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.BindOfficialTask(ctx, "delivery", node.ID, "upstream-1"); err != nil {
		t.Fatal(err)
	}
	job := domain.ResultUploadJob{ID: "upload-delivery", TaskID: "delivery", ObjectKey: "MiniMax-H3/2033-05-18/delivery.mp4"}
	if err := store.MarkOfficialGenerated(ctx, "delivery", node.ID, "https://origin.example/result.mp4", "16:9", &job); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "owner", "delivery")
	if err != nil || got.Status != domain.StatusReconciling || got.ResultInternalURL == "" || got.ResultPublicURL != "" || got.UpstreamSlotActive {
		t.Fatalf("task=%+v err=%v", got, err)
	}
}

func TestOfficialRunningAndDeliveryTasksCannotBeCancelled(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100})
	ctx := context.Background()
	node, _ := store.CreateModelNode(ctx, officialNodeInput("official", 1, true))
	if _, err := store.Create(ctx, officialTask("not-cancellable", false, false), "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNextOfficial(ctx, node.ID, node.Version, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.BindOfficialTask(ctx, "not-cancellable", node.ID, "upstream-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestAdminCancel(ctx, "not-cancellable"); !errors.Is(err, domain.ErrOfficialRunningNotCancellable) {
		t.Fatalf("running cancel err=%v", err)
	}
	job := domain.ResultUploadJob{ID: "upload-not-cancellable", TaskID: "not-cancellable", ObjectKey: "MiniMax-H3/2033-05-18/not-cancellable.mp4"}
	if err := store.MarkOfficialGenerated(ctx, "not-cancellable", node.ID, "https://origin.example/result.mp4", "16:9", &job); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestAdminCancel(ctx, "not-cancellable"); !errors.Is(err, domain.ErrOfficialRunningNotCancellable) {
		t.Fatalf("delivery cancel err=%v", err)
	}
}

func officialNodeInput(id string, capacity int, replace bool) domain.ModelNodeInput {
	return domain.ModelNodeInput{
		ID: id, ServiceURL: "https://api.example.com", ProtocolVersion: "minimax-v2",
		APIKeyCiphertext: []byte("cipher"), APIKeyNonce: []byte("nonce"), APIKeyFingerprint: "fingerprint",
		UpstreamModel: "MiniMax-H3", MaxConcurrency: capacity, ReplaceResultURL: replace,
		PollInterval: time.Second, RequestTimeout: time.Second, Enabled: true,
	}
}

func officialTask(id string, withLoRA, withRestoration bool) domain.NewTask {
	loras := "[]"
	if withLoRA {
		loras = `[{"name":"style","strength":1}]`
	}
	stages := []domain.NewTaskStage{{
		ID: "generation-" + id, StageType: "generation", StageOrder: 10, MaxAttempts: 3,
		ConfigSnapshotJSON: `{"stage_type":"generation","parameters":{"loras":` + loras + `}}`,
	}}
	if withRestoration {
		stages = append(stages, domain.NewTaskStage{ID: "restoration-" + id, StageType: "restoration", StageOrder: 20, MaxAttempts: 3, ConfigSnapshotJSON: `{}`})
	}
	return domain.NewTask{
		TaskID: id, APIKeyID: "owner", Model: "MiniMax-H3", Scenario: "t2va",
		RequestJSON: `{"model":"MiniMax-H3","content":[{"type":"text","text":"hello"}],"resolution":"2K","duration":5,"ratio":"16:9"}`,
		RequestHash: id, Resolution: "2K", Duration: 5, Ratio: "16:9", Stages: stages,
		ConfigSnapshotJSON: `{}`, ConfigHash: "hash-" + id,
	}
}
