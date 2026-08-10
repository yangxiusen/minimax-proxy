package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/httpapi/v2"
	"minimax-h3-tc/internal/monitor"
	"minimax-h3-tc/internal/store/sqlite"
	"minimax-h3-tc/internal/upstream/gradio"
)

func TestProcessOneBindsUniqueGalleryResult(t *testing.T) {
	store := workerStore(t)
	requestJSON := `{"model":"MiniMax-H3","content":[{"type":"text","text":"海边日落"}],"resolution":"2K","duration":5,"ratio":"16:9"}`
	_, err := store.Create(context.Background(), domain.NewTask{TaskID: "task-1", APIKeyID: "owner", Model: "MiniMax-H3", Scenario: "t2va", RequestJSON: requestJSON, RequestHash: "hash", Resolution: "2K", Duration: 5, Ratio: "16:9"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &scriptedClient{results: [][]any{
		{[]any{map[string]any{"video": map[string]any{"url": "http://private.local/old.mp4"}}}, "空闲", 0, "", ""},
		{"任务已提交"},
		{[]any{map[string]any{"video": map[string]any{"url": "http://private.local/old.mp4"}}, map[string]any{"video": map[string]any{"url": "http://private.local/new.mp4"}}}, "已完成", 0, "", ""},
	}}
	privateURL, _ := url.Parse("http://private.local")
	publicURL, _ := url.Parse("https://video.example.com")
	cache := monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}})
	processor := Processor{Store: store, Client: client, Cache: cache, Gate: &sync.Mutex{}, Upstream: config.UpstreamConfig{ID: "gpu-1", BaseURL: privateURL, PublicBaseURL: publicURL, SubmitAPIName: "submit_minimax_from_slots", CheckAPIName: "check_and_get_video", PollInterval: time.Millisecond}, Profiles: workerProfiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), "owner", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusSucceeded || got.ResultPublicURL != "https://video.example.com/new.mp4" {
		t.Fatalf("task = %+v", got)
	}
	if client.calls != 3 {
		t.Fatalf("calls = %d", client.calls)
	}
	node, _ := cache.Get("gpu-1")
	if node.CurrentTask != nil || node.Runtime != monitor.RuntimeIdle || node.LatestFinishedTask == nil || node.LatestFinishedTask.ID != "task-1" || node.LatestFinishedTask.Status != string(domain.V2Succeeded) {
		t.Fatalf("node snapshot = %+v", node)
	}
	if node.Health != monitor.HealthHealthy || node.PrivateQueue == nil || *node.PrivateQueue != 0 {
		t.Fatalf("node observation = %+v", node)
	}
}

func TestProcessOneClearsCurrentOnlyAfterBaselineRequeueSucceeds(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "requeue-1")
	client := &scriptedClient{results: [][]any{nil}, errors: []error{errors.New("offline")}}
	cache := monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}})
	processor := workerProcessor(store, client, cache)

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	node, _ := cache.Get("gpu-1")
	if node.CurrentTask != nil || node.Runtime != monitor.RuntimeUnknown {
		t.Fatalf("node snapshot = %+v", node)
	}
	task, err := store.Get(context.Background(), "owner", "requeue-1")
	if err != nil || (task.Status != domain.StatusQueuedOpen && task.Status != domain.StatusQueuedLocked) {
		t.Fatalf("task = %+v, err = %v", task, err)
	}
}

func TestProcessOneDoesNotClearCurrentBeforeDatabaseCompletion(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "db-fail-1")
	client := &scriptedClient{results: [][]any{
		{[]any{}, "idle", 0, "CPU: 10%"},
		{"submitted"},
		{[]any{map[string]any{"video": map[string]any{"url": "http://private.local/new.mp4"}}}, "complete", 0, "CPU: 20%"},
	}}
	cache := monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}})
	processor := workerProcessor(&failingSucceededStore{Store: store}, client, cache)

	if err := processor.ProcessOne(context.Background()); err == nil {
		t.Fatal("ProcessOne() error = nil")
	}
	node, _ := cache.Get("gpu-1")
	if node.CurrentTask == nil || node.CurrentTask.ID != "db-fail-1" || node.LatestFinishedTask != nil || node.Runtime != monitor.RuntimeRunning {
		t.Fatalf("node snapshot = %+v", node)
	}
}

func TestProcessOnePollFailureMarksUnhealthyAndKeepsCurrent(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "poll-fail-1")
	ctx, cancel := context.WithCancel(context.Background())
	client := &cancelAfterPollFailureClient{cancel: cancel}
	cache := monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}})
	processor := workerProcessor(store, client, cache)
	processor.Upstream.PollInterval = time.Millisecond

	if err := processor.ProcessOne(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessOne() error = %v", err)
	}
	node, _ := cache.Get("gpu-1")
	if node.Health != monitor.HealthUnhealthy || node.LastError == nil || node.LastError.Code != "upstream_poll_error" || node.CurrentTask == nil || node.CurrentTask.ID != "poll-fail-1" {
		t.Fatalf("node snapshot = %+v", node)
	}
	task, err := store.Get(context.Background(), "owner", "poll-fail-1")
	if err != nil || task.Status != domain.StatusReconciling {
		t.Fatalf("task = %+v, err = %v", task, err)
	}
}

func TestProcessOneHoldsGateForEntireOperation(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "gate-1")
	createWorkerTask(t, store, "gate-2")
	observedStore := &signalingStore{Store: store, activeCalls: make(chan struct{}, 16)}
	client := &blockingSuccessClient{entered: make(chan struct{}), release: make(chan struct{})}
	cache := monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}})
	processor := workerProcessor(observedStore, client, cache)
	results := make(chan error, 2)
	go func() { results <- processor.ProcessOne(context.Background()) }()
	<-observedStore.activeCalls
	<-client.entered
	go func() { results <- processor.ProcessOne(context.Background()) }()
	select {
	case <-observedStore.activeCalls:
		t.Fatal("second ProcessOne entered Store while first still held the gate")
	case <-time.After(30 * time.Millisecond):
	}
	close(client.release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestProcessOneResumesActiveTaskWithoutResubmitting(t *testing.T) {
	store := workerStore(t)
	requestJSON := `{"model":"MiniMax-H3","content":[{"type":"text","text":"海边日落"}],"resolution":"2K","duration":5,"ratio":"16:9"}`
	_, err := store.Create(context.Background(), domain.NewTask{TaskID: "resume-1", APIKeyID: "owner", Model: "MiniMax-H3", Scenario: "t2va", RequestJSON: requestJSON, RequestHash: "hash", Resolution: "2K", Duration: 5, Ratio: "16:9"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNext(context.Background(), "gpu-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBaseline(context.Background(), claimed.TaskID, "gpu-1", []string{"http://private.local/old.mp4"}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(context.Background(), claimed.TaskID, "gpu-1"); err != nil {
		t.Fatal(err)
	}
	client := &scriptedClient{results: [][]any{{[]any{map[string]any{"video": map[string]any{"url": "http://private.local/old.mp4"}}, map[string]any{"video": map[string]any{"url": "http://private.local/resumed.mp4"}}}, "已完成", "", "", ""}}}
	privateURL, _ := url.Parse("http://private.local")
	publicURL, _ := url.Parse("https://video.example.com")
	processor := Processor{Store: store, Client: client, Upstream: config.UpstreamConfig{ID: "gpu-1", BaseURL: privateURL, PublicBaseURL: publicURL, SubmitAPIName: "submit_minimax_from_slots", CheckAPIName: "check_and_get_video", PollInterval: time.Millisecond}, Profiles: workerProfiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), "owner", "resume-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusSucceeded || client.calls != 1 {
		t.Fatalf("task=%+v calls=%d", got, client.calls)
	}
}

func TestProcessOneRecoveryUsesPersistedStartedAtForDuration(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "duration.db"), sqlite.Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100, Retention: 7 * 24 * time.Hour, IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	createWorkerTask(t, store, "duration-1")
	claimed, err := store.ClaimNext(context.Background(), "gpu-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBaseline(context.Background(), claimed.TaskID, "gpu-1", []string{}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(context.Background(), claimed.TaskID, "gpu-1"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(17 * time.Second)
	client := &scriptedClient{results: [][]any{{[]any{map[string]any{"video": map[string]any{"url": "http://private.local/recovered.mp4"}}}, "complete", 0, ""}}}
	cache := monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}})
	processor := workerProcessor(store, client, cache)

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	node, _ := cache.Get("gpu-1")
	if node.LatestFinishedTask == nil || node.LatestFinishedTask.DurationSeconds != 17 || node.LatestFinishedTask.ID != "duration-1" {
		t.Fatalf("node snapshot = %+v", node)
	}
}

func TestProcessOneReconcilesUnknownSubmitResult(t *testing.T) {
	store := workerStore(t)
	requestJSON := `{"model":"MiniMax-H3","content":[{"type":"text","text":"海边日落"}],"resolution":"2K","duration":5,"ratio":"16:9"}`
	_, err := store.Create(context.Background(), domain.NewTask{TaskID: "unknown-1", APIKeyID: "owner", Model: "MiniMax-H3", Scenario: "t2va", RequestJSON: requestJSON, RequestHash: "hash", Resolution: "2K", Duration: 5, Ratio: "16:9"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &scriptedClient{results: [][]any{{[]any{}, "空闲", "", "", ""}, nil, {[]any{map[string]any{"video": map[string]any{"url": "http://private.local/reconciled.mp4"}}}, "已完成", "", "", ""}}, errors: []error{nil, errors.New("connection reset"), nil}}
	privateURL, _ := url.Parse("http://private.local")
	publicURL, _ := url.Parse("https://video.example.com")
	processor := Processor{Store: store, Client: client, Upstream: config.UpstreamConfig{ID: "gpu-1", BaseURL: privateURL, PublicBaseURL: publicURL, SubmitAPIName: "submit_minimax_from_slots", CheckAPIName: "check_and_get_video", PollInterval: time.Millisecond}, Profiles: workerProfiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), "owner", "unknown-1")
	if err != nil || got.Status != domain.StatusSucceeded {
		t.Fatalf("task=%+v error=%v", got, err)
	}
}

func TestProcessOneFailsTaskWhenUpstreamExplicitlyRejectsSubmit(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "rejected-1")
	client := &scriptedClient{
		results: [][]any{{[]any{}, "空闲", "", "", ""}, nil},
		errors:  []error{nil, fmt.Errorf("%w: invalid mode", gradio.ErrRequestRejected)},
	}
	processor := workerProcessor(store, client, monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}}))

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), "owner", "rejected-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusFailed || got.ErrorCode != "upstream_rejected" {
		t.Fatalf("task = %+v", got)
	}
}

func TestProcessOneFailsTaskWhenAudioUploadPreparationFails(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "upload-failed-1")
	client := &prepareErrorClient{}
	processor := workerProcessor(store, client, monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}}))

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	task, err := store.Get(context.Background(), "owner", "upload-failed-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusFailed || task.ErrorCode != "upstream_media_upload_failed" || client.calls != 1 {
		t.Fatalf("task=%+v calls=%d", task, client.calls)
	}
}

func TestProcessOneRetriesLostPrivateJobOnce(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "lost-retry-1")
	firstJob := "11111111-1111-1111-1111-111111111111"
	secondJob := "22222222-2222-2222-2222-222222222222"
	client := &lifecycleClient{
		preparePaths: []string{`C:\gradio-cache\first.wav`, `C:\gradio-cache\retry.wav`},
		callResults: [][]any{
			{[]any{}, "idle", 0, ""},
			{"submitted"},
			{[]any{}, "idle", 0, ""},
			{"retried"},
			{[]any{map[string]any{"video": map[string]any{"url": "http://private.local/retried.mp4"}}}, "complete", 0, ""},
		},
		jobLists: [][]gradio.Job{
			{},
			{{ID: firstJob, Status: gradio.JobInProgress}},
			{},
			{{ID: secondJob, Status: gradio.JobInProgress}},
		},
		getErrors:    []error{gradio.ErrJobNotFound},
		cancelErrors: []error{gradio.ErrJobNotFound},
	}
	processor := workerProcessor(store, client, monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}}))

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	task, err := store.Get(context.Background(), "owner", "lost-retry-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusSucceeded || task.RetryCount != 1 || task.ResultPublicURL != "https://video.example.com/retried.mp4" {
		t.Fatalf("task = %+v", task)
	}
	if client.submitCalls != 2 || client.cancelCalls != 1 || client.prepareCalls != 2 {
		t.Fatalf("submit calls = %d, cancel calls = %d, prepare calls = %d", client.submitCalls, client.cancelCalls, client.prepareCalls)
	}
	if len(client.submittedAudioPaths) != 2 || client.submittedAudioPaths[0] != `C:\gradio-cache\first.wav` || client.submittedAudioPaths[1] != `C:\gradio-cache\retry.wav` {
		t.Fatalf("submitted audio paths = %#v", client.submittedAudioPaths)
	}
}

func TestProcessOneFailsWhenRetriedPrivateJobIsLostAgain(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "lost-twice-1")
	firstJob := "55555555-5555-5555-5555-555555555555"
	secondJob := "66666666-6666-6666-6666-666666666666"
	client := &lifecycleClient{
		callResults: [][]any{
			{[]any{}, "idle", 0, ""}, {"submitted"}, {[]any{}, "idle", 0, ""},
			{"retried"}, {[]any{}, "idle", 0, ""},
		},
		jobLists: [][]gradio.Job{
			{}, {{ID: firstJob, Status: gradio.JobInProgress}}, {}, {{ID: secondJob, Status: gradio.JobInProgress}},
		},
		getErrors:    []error{gradio.ErrJobNotFound, gradio.ErrJobNotFound},
		cancelErrors: []error{gradio.ErrJobNotFound},
	}
	processor := workerProcessor(store, client, monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}}))

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	task, err := store.Get(context.Background(), "owner", "lost-twice-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusFailed || task.RetryCount != 1 || task.ErrorCode != "upstream_job_lost" || client.submitCalls != 2 {
		t.Fatalf("task = %+v, submit calls = %d", task, client.submitCalls)
	}
}

func TestProcessOneCancelsExactJobBeforeExecutionTimeoutFailure(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "timeout-1")
	jobID := "77777777-7777-7777-7777-777777777777"
	client := &lifecycleClient{
		callResults: [][]any{{[]any{}, "idle", 0, ""}, {"submitted"}, {[]any{}, "running", 1, ""}},
		jobLists:    [][]gradio.Job{{}, {{ID: jobID, Status: gradio.JobInProgress}}},
		getJobs:     []gradio.Job{{ID: jobID, Status: gradio.JobInProgress}},
	}
	processor := workerProcessor(store, client, monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}}))
	processor.ExecutionTimeout = 10 * time.Minute
	processor.Now = func() time.Time { return time.Now().UTC().Add(11 * time.Minute) }

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	task, err := store.Get(context.Background(), "owner", "timeout-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusFailed || task.ErrorCode != "execution_timeout" || client.cancelCalls != 1 || task.RetryCount != 0 {
		t.Fatalf("task = %+v, cancel calls = %d", task, client.cancelCalls)
	}
}

func TestProcessOneExecutionTimeoutStillFinishesWhenCancelFails(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "timeout-cancel-error-1")
	jobID := "88888888-8888-8888-8888-888888888888"
	client := &lifecycleClient{
		callResults:  [][]any{{[]any{}, "idle", 0, ""}, {"submitted"}, {[]any{}, "running", 1, ""}},
		jobLists:     [][]gradio.Job{{}, {{ID: jobID, Status: gradio.JobInProgress}}},
		getJobs:      []gradio.Job{{ID: jobID, Status: gradio.JobInProgress}},
		cancelErrors: []error{errors.New("cancel unavailable")},
	}
	processor := workerProcessor(store, client, monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}}))
	processor.Now = func() time.Time { return time.Now().UTC().Add(11 * time.Minute) }

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	task, err := store.Get(context.Background(), "owner", "timeout-cancel-error-1")
	if err != nil || task.Status != domain.StatusFailed || task.ErrorCode != "execution_timeout" {
		t.Fatalf("task = %+v, err = %v", task, err)
	}
	node, _ := processor.Cache.Get("gpu-1")
	if !node.SchedulingBlocked || node.Health != monitor.HealthUnhealthy || node.Runtime != monitor.RuntimeUnknown {
		t.Fatalf("node = %+v", node)
	}
}

func TestProcessOneUnboundExecutionTimeoutBlocksScheduling(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "timeout-unbound-1")
	client := &lifecycleClient{
		callResults: [][]any{{[]any{}, "idle", 0, ""}, {"submitted"}, {[]any{}, "running", 1, ""}},
		jobLists:    [][]gradio.Job{{}, {}, {}},
	}
	cache := monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}})
	processor := workerProcessor(store, client, cache)
	processor.Now = func() time.Time { return time.Now().UTC().Add(11 * time.Minute) }

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	task, err := store.Get(context.Background(), "owner", "timeout-unbound-1")
	if err != nil || task.Status != domain.StatusFailed || task.ErrorCode != "execution_timeout" {
		t.Fatalf("task = %+v, err = %v", task, err)
	}
	node, _ := cache.Get("gpu-1")
	if !node.SchedulingBlocked || node.Health != monitor.HealthUnhealthy {
		t.Fatalf("node = %+v", node)
	}
}

func TestProcessOneCompletedWithoutGalleryFailsWithoutRetry(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "completed-no-gallery-1")
	jobID := "99999999-9999-9999-9999-999999999999"
	client := &lifecycleClient{
		callResults: [][]any{
			{[]any{}, "idle", 0, ""}, {"submitted"},
			{[]any{}, "complete", 0, ""}, {[]any{}, "complete", 0, ""}, {[]any{}, "complete", 0, ""},
		},
		jobLists: [][]gradio.Job{{}, {{ID: jobID, Status: gradio.JobCompleted}}},
		getJobs:  []gradio.Job{{ID: jobID, Status: gradio.JobCompleted}, {ID: jobID, Status: gradio.JobCompleted}, {ID: jobID, Status: gradio.JobCompleted}},
	}
	processor := workerProcessor(store, client, monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}}))

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	task, err := store.Get(context.Background(), "owner", "completed-no-gallery-1")
	if err != nil || task.Status != domain.StatusFailed || task.ErrorCode != "upstream_result_missing" || task.RetryCount != 0 || client.submitCalls != 1 {
		t.Fatalf("task = %+v, submits = %d, err = %v", task, client.submitCalls, err)
	}
}

func TestProcessOneClearsCurrentWhenLatestFinishedWasDeleted(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "deleted-after-finish-1")
	client := &scriptedClient{results: [][]any{
		{[]any{}, "idle", 0, ""}, {"submitted"},
		{[]any{map[string]any{"video": map[string]any{"url": "http://private.local/result.mp4"}}}, "complete", 0, ""},
	}}
	cache := monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}})
	processor := workerProcessor(&missingLatestStore{Store: store}, client, cache)

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	node, _ := cache.Get("gpu-1")
	if node.CurrentTask != nil || node.Runtime != monitor.RuntimeIdle {
		t.Fatalf("node = %+v", node)
	}
}

func TestUnknownPrivateQueueIsNotEmpty(t *testing.T) {
	if queueEmpty(gradio.Observation{}) {
		t.Fatal("unknown queue was treated as empty")
	}
}

func TestProcessOneCancelsRunningPrivateJob(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "cancel-running-1")
	task, err := store.ClaimNext(context.Background(), "gpu-1")
	if err != nil {
		t.Fatal(err)
	}
	jobID := "33333333-3333-3333-3333-333333333333"
	if err := store.SaveSubmissionContext(context.Background(), task.TaskID, "gpu-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBaseline(context.Background(), task.TaskID, "gpu-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.BindUpstreamJob(context.Background(), task.TaskID, "gpu-1", jobID); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestAdminCancel(context.Background(), task.TaskID); err != nil {
		t.Fatal(err)
	}
	client := &lifecycleClient{}
	processor := workerProcessor(store, client, monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}}))

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), "owner", task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusCancelled || client.cancelCalls != 1 {
		t.Fatalf("task = %+v, cancel calls = %d", got, client.cancelCalls)
	}
}

func TestProcessOneCancelFailureFinishesLocallyAndBlocksScheduling(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "cancel-failure-1")
	task, err := store.ClaimNext(context.Background(), "gpu-1")
	if err != nil {
		t.Fatal(err)
	}
	jobID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	if err := store.SaveSubmissionContext(context.Background(), task.TaskID, "gpu-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBaseline(context.Background(), task.TaskID, "gpu-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.BindUpstreamJob(context.Background(), task.TaskID, "gpu-1", jobID); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestAdminCancel(context.Background(), task.TaskID); err != nil {
		t.Fatal(err)
	}
	cache := monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}})
	client := &lifecycleClient{cancelErrors: []error{errors.New("cancel unavailable")}}
	processor := workerProcessor(store, client, cache)
	processor.Now = func() time.Time { return time.Now().UTC().Add(11 * time.Minute) }

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), "owner", task.TaskID)
	if err != nil || got.Status != domain.StatusCancelled {
		t.Fatalf("task = %+v, err = %v", got, err)
	}
	node, _ := cache.Get("gpu-1")
	if !node.SchedulingBlocked || node.Health != monitor.HealthUnhealthy || node.Runtime != monitor.RuntimeUnknown {
		t.Fatalf("node = %+v", node)
	}
}

func TestProcessOneUnboundCancelBlocksSchedulingWhenNoJobIsVisible(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "cancel-unbound-1")
	task, err := store.ClaimNext(context.Background(), "gpu-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSubmissionContext(context.Background(), task.TaskID, "gpu-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBaseline(context.Background(), task.TaskID, "gpu-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkReconciling(context.Background(), task.TaskID, "gpu-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestAdminCancel(context.Background(), task.TaskID); err != nil {
		t.Fatal(err)
	}
	cache := monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}})
	processor := workerProcessor(store, &lifecycleClient{jobLists: [][]gradio.Job{{}}}, cache)

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), "owner", task.TaskID)
	if err != nil || got.Status != domain.StatusCancelled {
		t.Fatalf("task = %+v, err = %v", got, err)
	}
	node, _ := cache.Get("gpu-1")
	if !node.SchedulingBlocked || node.Health != monitor.HealthUnhealthy {
		t.Fatalf("node = %+v", node)
	}
}

func TestProcessOneFindsAndCancelsJobWhenBindingWasInterrupted(t *testing.T) {
	store := workerStore(t)
	createWorkerTask(t, store, "cancel-unbound-1")
	task, err := store.ClaimNext(context.Background(), "gpu-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSubmissionContext(context.Background(), task.TaskID, "gpu-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBaseline(context.Background(), task.TaskID, "gpu-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkReconciling(context.Background(), task.TaskID, "gpu-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestAdminCancel(context.Background(), task.TaskID); err != nil {
		t.Fatal(err)
	}
	jobID := "44444444-4444-4444-4444-444444444444"
	client := &lifecycleClient{jobLists: [][]gradio.Job{{{ID: jobID, Status: gradio.JobInProgress}}}}
	processor := workerProcessor(store, client, monitor.NewCache([]monitor.NodeSnapshot{{ID: "gpu-1"}}))

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), "owner", task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusCancelled || client.cancelCalls != 1 {
		t.Fatalf("task = %+v, cancel calls = %d", got, client.cancelCalls)
	}
}

type scriptedClient struct {
	results [][]any
	errors  []error
	calls   int
}

type lifecycleClient struct {
	callResults         [][]any
	callErrors          []error
	jobLists            [][]gradio.Job
	listErrors          []error
	getJobs             []gradio.Job
	getErrors           []error
	cancelErrors        []error
	callIndex           int
	listIndex           int
	getIndex            int
	cancelCalls         int
	submitCalls         int
	prepareCalls        int
	preparePaths        []string
	submittedAudioPaths []string
}

type prepareErrorClient struct {
	calls int
}

func (c *prepareErrorClient) Call(context.Context, string, []any) ([]any, error) {
	c.calls++
	return []any{[]any{}, "idle", 0, ""}, nil
}

func (c *prepareErrorClient) PrepareArguments(context.Context, v2.ValidatedRequest, config.GenerationProfile) ([]any, error) {
	return nil, errors.New("upload failed")
}

func (c *lifecycleClient) PrepareArguments(_ context.Context, request v2.ValidatedRequest, profile config.GenerationProfile) ([]any, error) {
	index := c.prepareCalls
	c.prepareCalls++
	arguments, err := gradio.BuildArguments(request, profile)
	if err == nil && index < len(c.preparePaths) {
		arguments[29] = gradio.FileData{Path: c.preparePaths[index], Meta: gradio.FileMeta{Type: "gradio.FileData"}}
	}
	return arguments, err
}

func (c *lifecycleClient) Call(_ context.Context, apiName string, arguments []any) ([]any, error) {
	if apiName == "submit" || apiName == "submit_minimax_from_slots" {
		c.submitCalls++
		if file, ok := arguments[29].(gradio.FileData); ok {
			c.submittedAudioPaths = append(c.submittedAudioPaths, file.Path)
		}
	}
	index := c.callIndex
	c.callIndex++
	var result []any
	if index < len(c.callResults) {
		result = c.callResults[index]
	}
	var err error
	if index < len(c.callErrors) {
		err = c.callErrors[index]
	}
	return result, err
}

func (c *lifecycleClient) ListJobs(context.Context) ([]gradio.Job, error) {
	index := c.listIndex
	c.listIndex++
	var result []gradio.Job
	if index < len(c.jobLists) {
		result = c.jobLists[index]
	}
	var err error
	if index < len(c.listErrors) {
		err = c.listErrors[index]
	}
	return result, err
}

func (c *lifecycleClient) GetJob(context.Context, string) (gradio.Job, error) {
	index := c.getIndex
	c.getIndex++
	var result gradio.Job
	if index < len(c.getJobs) {
		result = c.getJobs[index]
	}
	var err error
	if index < len(c.getErrors) {
		err = c.getErrors[index]
	}
	return result, err
}

func (c *lifecycleClient) CancelJob(context.Context, string) (bool, error) {
	index := c.cancelCalls
	c.cancelCalls++
	var err error
	if index < len(c.cancelErrors) {
		err = c.cancelErrors[index]
	}
	return err == nil, err
}

type failingSucceededStore struct{ *sqlite.Store }

type missingLatestStore struct{ Store }

func (s *missingLatestStore) LatestFinishedForUpstream(context.Context, string) (domain.AdminTaskSummary, error) {
	return domain.AdminTaskSummary{}, domain.ErrTaskNotFound
}

func (s *failingSucceededStore) MarkSucceeded(context.Context, string, string, string, string, string) error {
	return errors.New("database write failed")
}

type signalingStore struct {
	*sqlite.Store
	activeCalls chan struct{}
}

func (s *signalingStore) ActiveForUpstream(ctx context.Context, id string) (domain.Task, error) {
	s.activeCalls <- struct{}{}
	return s.Store.ActiveForUpstream(ctx, id)
}

type blockingSuccessClient struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (c *blockingSuccessClient) Call(context.Context, string, []any) ([]any, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	if call == 1 {
		close(c.entered)
		<-c.release
	}
	switch (call - 1) % 3 {
	case 0:
		return []any{[]any{}, "idle", 0, ""}, nil
	case 1:
		return []any{"submitted"}, nil
	default:
		return []any{[]any{map[string]any{"video": map[string]any{"url": "http://private.local/result.mp4"}}}, "complete", 0, ""}, nil
	}
}

type cancelAfterPollFailureClient struct {
	calls  int
	cancel context.CancelFunc
}

func (c *cancelAfterPollFailureClient) Call(ctx context.Context, _ string, _ []any) ([]any, error) {
	c.calls++
	switch c.calls {
	case 1:
		return []any{[]any{}, "idle", 0, "CPU: 55%"}, nil
	case 2:
		return []any{"submitted"}, nil
	default:
		c.cancel()
		return nil, errors.New("poll failed")
	}
}

func (c *scriptedClient) Call(_ context.Context, _ string, _ []any) ([]any, error) {
	result := c.results[c.calls]
	var err error
	if len(c.errors) > c.calls {
		err = c.errors[c.calls]
	}
	c.calls++
	return result, err
}

func workerStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "worker.db"), sqlite.Options{ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100, Retention: 7 * 24 * time.Hour, IdempotencyTTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func workerProfiles() map[string]config.GenerationProfile {
	dimensions := map[string]config.Dimension{"16:9": {Width: 1920, Height: 1080}}
	return map[string]config.GenerationProfile{"2K": {ModelMode: "custom", Steps: 20, Dimensions: dimensions}}
}

func createWorkerTask(t *testing.T, store *sqlite.Store, id string) {
	t.Helper()
	requestJSON := `{"model":"MiniMax-H3","content":[{"type":"text","text":"海边日落"}],"resolution":"2K","duration":5,"ratio":"16:9"}`
	_, err := store.Create(context.Background(), domain.NewTask{TaskID: id, APIKeyID: "owner", Model: "MiniMax-H3", Scenario: "t2va", RequestJSON: requestJSON, RequestHash: "hash-" + id, Resolution: "2K", Duration: 5, Ratio: "16:9"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
}

func workerProcessor(store Store, client GradioClient, cache *monitor.Cache) Processor {
	privateURL, _ := url.Parse("http://private.local")
	publicURL, _ := url.Parse("https://video.example.com")
	return Processor{Store: store, Client: client, Cache: cache, Gate: &sync.Mutex{}, Upstream: config.UpstreamConfig{ID: "gpu-1", BaseURL: privateURL, PublicBaseURL: publicURL, SubmitAPIName: "submit", CheckAPIName: "check", PollInterval: time.Millisecond}, Profiles: workerProfiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}
