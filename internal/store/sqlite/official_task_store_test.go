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

func TestClaimNextOfficialIgnoresInternalConfiguration(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 100, GlobalLimit: 100})
	ctx := context.Background()
	node, err := store.CreateModelNode(ctx, officialNodeInput("official", 3, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, officialTask("with-internal-config", true, true), "", nil); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNextOfficial(ctx, node.ID, node.Version, node.MaxConcurrency)
	if err != nil || claimed.TaskID != "with-internal-config" {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if err := store.BindOfficialTask(ctx, claimed.TaskID, node.ID, "upstream-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkOfficialGenerated(ctx, claimed.TaskID, node.ID, "https://result.example.com/video.mp4", "16:9", nil); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.QueryContext(ctx, `SELECT stage_type,status FROM task_stages WHERE task_id=?`, claimed.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	statuses := map[string]string{}
	for rows.Next() {
		var stageType, status string
		if err := rows.Scan(&stageType, &status); err != nil {
			t.Fatal(err)
		}
		statuses[stageType] = status
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if statuses["generation"] != "succeeded" || statuses["interpolation"] != "skipped" || statuses["restoration"] != "skipped" {
		t.Fatalf("stage statuses=%v", statuses)
	}
}

func TestClaimNextOfficialAcceptsSupportedOfficialMediaSources(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 100, GlobalLimit: 100})
	ctx := context.Background()
	node, err := store.CreateModelNode(ctx, officialNodeInput("official", 3, false))
	if err != nil {
		t.Fatal(err)
	}
	tasks := []domain.NewTask{
		officialTaskWithContent("data-url", `[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]`),
		officialTaskWithContent("proxy-input", `[{"type":"video_url","video_url":{"url":"proxy-input://video-1"}}]`),
		officialTaskWithContent("ftp-url", `[{"type":"audio_url","audio_url":{"url":"ftp://files.example.com/audio.mp3"}}]`),
		officialTaskWithContent("empty-url", `[{"type":"image_url","image_url":{"url":""}}]`),
		officialTaskWithContent("lookalike-scheme", `[{"type":"video_url","video_url":{"url":"mmXfile://video-1"}}]`),
		officialTaskWithContent("public-url", `[{"type":"image_url","image_url":{"url":"HTTPS://files.example.com/image.png"}}]`),
		officialTaskWithContent("mm-file", `[{"type":"video_url","video_url":{"url":"mm_file://video-1"}}]`),
	}
	for _, task := range tasks {
		if _, err := store.Create(ctx, task, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	for index, wantID := range []string{"data-url", "public-url", "mm-file"} {
		claimed, err := store.ClaimNextOfficial(ctx, node.ID, node.Version, node.MaxConcurrency)
		if err != nil || claimed.TaskID != wantID {
			t.Fatalf("claim %d=%+v err=%v want=%s", index+1, claimed, err, wantID)
		}
	}
}

func TestClaimNextOfficialAcceptsOnlyMatchingProxyInputMetadata(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 100, GlobalLimit: 100})
	ctx := context.Background()
	node, err := store.CreateModelNode(ctx, officialNodeInput("official", 2, false))
	if err != nil {
		t.Fatal(err)
	}
	valid := officialTaskWithContent("valid-spool", `[{"type":"image_url","role":"reference_image","image_url":{"url":"proxy-input://valid-spool/input-valid"}}]`)
	valid.InputSpoolFiles = []domain.InputSpoolFile{{
		ID: "input-valid", TaskID: valid.TaskID, ContentIndex: 0, ContentType: "image_url", Role: "reference_image",
		SourceKind: "data_uri", MediaType: "image/png", Extension: ".png", RelativePath: "valid-spool/input-valid.png",
		SizeBytes: 12, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}
	invalid := officialTaskWithContent("invalid-spool", `[{"type":"image_url","role":"reference_image","image_url":{"url":"proxy-input://invalid-spool/missing"}}]`)
	for _, task := range []domain.NewTask{invalid, valid} {
		if _, err := store.Create(ctx, task, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	claimed, err := store.ClaimNextOfficial(ctx, node.ID, node.Version, node.MaxConcurrency)
	if err != nil || claimed.TaskID != valid.TaskID {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
}

func TestClaimNextInternalSkipsMMFileMedia(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 100, GlobalLimit: 100})
	ctx := context.Background()
	for _, task := range []domain.NewTask{
		officialTaskWithContent("official-only", `[{"type":"video_url","video_url":{"url":"mm_file://video-1"}}]`),
		officialTask("internal-compatible", false, false),
	} {
		if _, err := store.Create(ctx, task, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	claimed, err := store.ClaimNext(ctx, "internal-node")
	if err != nil || claimed.TaskID != "internal-compatible" {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
}

func TestClaimStageInternalSkipsMMFileMedia(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 100, GlobalLimit: 100})
	ctx := context.Background()
	insertNodeAPINode(t, store, "internal-node")
	for _, task := range []domain.NewTask{
		officialTaskWithContent("official-stage-only", `[{"type":"video_url","video_url":{"url":"mm_file://video-1"}}]`),
		officialTask("internal-stage-compatible", false, false),
	} {
		if _, err := store.Create(ctx, task, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	stage, err := store.ClaimStage(ctx, "internal-node", "lease-1", time.Minute)
	if err != nil || stage.TaskID != "internal-stage-compatible" {
		t.Fatalf("stage=%+v err=%v", stage, err)
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
		stages = append(stages,
			domain.NewTaskStage{ID: "interpolation-" + id, StageType: "interpolation", StageOrder: 20, MaxAttempts: 3, ConfigSnapshotJSON: `{}`},
			domain.NewTaskStage{ID: "restoration-" + id, StageType: "restoration", StageOrder: 30, MaxAttempts: 3, ConfigSnapshotJSON: `{}`},
		)
	}
	return domain.NewTask{
		TaskID: id, APIKeyID: "owner", Model: "MiniMax-H3", Scenario: "t2va",
		RequestJSON: `{"model":"MiniMax-H3","content":[{"type":"text","text":"hello"}],"resolution":"2K","duration":5,"ratio":"16:9"}`,
		RequestHash: id, Resolution: "2K", Duration: 5, Ratio: "16:9", Stages: stages,
		ConfigSnapshotJSON: `{}`, ConfigHash: "hash-" + id,
	}
}

func officialTaskWithContent(id, content string) domain.NewTask {
	task := officialTask(id, false, false)
	task.RequestJSON = fmt.Sprintf(`{"model":"MiniMax-H3","content":%s,"resolution":"2K","duration":5,"ratio":"16:9"}`, content)
	return task
}
