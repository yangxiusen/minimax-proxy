package v2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/inputspool"
	"minimax-h3-tc/internal/store/sqlite"
)

func TestOfficialRoutesUseAuthenticationAndOwnerIsolation(t *testing.T) {
	store := apiStore(t, 0)
	handler := NewHandler(Dependencies{Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}, {ID: "owner-b", Key: "key-b", Enabled: true}}, Profiles: profiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	unauthorized := request(t, handler, http.MethodPost, "/v2/video_generation", validCreateJSON(), "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	var errorBody map[string]any
	decode(t, unauthorized, &errorBody)
	detail, _ := errorBody["error"].(map[string]any)
	if detail["type"] != "authorized_error" || detail["message"] == nil || detail["http_code"] != "401" {
		t.Fatalf("error body = %+v", errorBody)
	}

	created := request(t, handler, http.MethodPost, "/v2/video_generation", validCreateJSON(), "key-a")
	if created.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var createBody struct {
		TaskID string `json:"task_id"`
	}
	decode(t, created, &createBody)
	if createBody.TaskID == "" {
		t.Fatal("empty task_id")
	}

	get := request(t, handler, http.MethodGet, "/v2/query/video_generation/"+createBody.TaskID, nil, "key-a")
	if get.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}
	var getBody struct {
		Task TaskResponse `json:"task"`
	}
	decode(t, get, &getBody)
	if getBody.Task.Status != "queued" || getBody.Task.ID != createBody.TaskID {
		t.Fatalf("task = %+v", getBody.Task)
	}

	crossOwner := request(t, handler, http.MethodGet, "/v2/query/video_generation/"+createBody.TaskID, nil, "key-b")
	if crossOwner.Code != http.StatusBadRequest {
		t.Fatalf("cross owner status = %d", crossOwner.Code)
	}

	list := request(t, handler, http.MethodGet, "/v2/query/video_generation?page_num=1&page_size=20&filter.status=queued", nil, "key-a")
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var listBody struct {
		Items []TaskResponse `json:"items"`
		Total int            `json:"total"`
	}
	decode(t, list, &listBody)
	if listBody.Total != 1 || len(listBody.Items) != 1 {
		t.Fatalf("list = %+v", listBody)
	}

	unknown := request(t, handler, http.MethodGet, "/health", nil, "key-a")
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("health status = %d", unknown.Code)
	}
}

func TestCreateIsStrictAndReturnsSucceededURL(t *testing.T) {
	store := apiStore(t, 0)
	handler := NewHandler(Dependencies{Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}}, Profiles: profiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	invalid := request(t, handler, http.MethodPost, "/v2/video_generation", []byte(`{"model":"MiniMax-H3","unknown":true}`), "key-a")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d", invalid.Code)
	}

	created := request(t, handler, http.MethodPost, "/v2/video_generation", validCreateJSON(), "key-a")
	var body struct {
		TaskID string `json:"task_id"`
	}
	decode(t, created, &body)
	claimed, err := store.ClaimNext(context.Background(), "gpu-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSucceeded(context.Background(), claimed.TaskID, "gpu-1", "http://private/output.mp4", "https://public.example/output.mp4", "16:9"); err != nil {
		t.Fatal(err)
	}
	get := request(t, handler, http.MethodGet, "/v2/query/video_generation/"+body.TaskID, nil, "key-a")
	var response struct {
		Task TaskResponse `json:"task"`
	}
	decode(t, get, &response)
	if response.Task.Status != "succeeded" || response.Task.Content == nil || response.Task.Content.URL != "https://public.example/output.mp4" {
		t.Fatalf("task = %+v", response.Task)
	}
}

func TestCreateRejectsPublicFPSWithoutCreatingTask(t *testing.T) {
	store := &createSpyStore{}
	handler := NewHandler(Dependencies{
		Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}},
		Profiles: profiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	response := request(t, handler, http.MethodPost, "/v2/video_generation", []byte(`{"model":"MiniMax-H3","content":[{"type":"text","text":"海边日落"}],"resolution":"2K","duration":5,"ratio":"16:9","fps":15}`), "key-a")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "bad_request_error") || !strings.Contains(response.Body.String(), "(2013)") {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	if store.createCalls != 0 {
		t.Fatalf("create calls=%d, want 0", store.createCalls)
	}
}

func TestCreateStoresDataURIInInputSpoolInsteadOfRequestJSON(t *testing.T) {
	store := apiStore(t, 0)
	spooler := inputspool.New(filepath.Join(t.TempDir(), "temp-inputs"))
	handler := NewHandler(Dependencies{
		Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}},
		Profiles: profiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), InputSpooler: spooler,
	})
	payload := `{"model":"MiniMax-H3","content":[{"type":"text","text":"海边日落"},{"type":"image_url","role":"first_frame","image_url":{"url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="}}],"resolution":"768P","duration":5}`
	created := request(t, handler, http.MethodPost, "/v2/video_generation", []byte(payload), "key-a")
	if created.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var body struct {
		TaskID string `json:"task_id"`
	}
	decode(t, created, &body)
	task, err := store.Get(context.Background(), "owner-a", body.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(task.RequestJSON, ";base64,") {
		t.Fatalf("request_json still contains base64: %s", task.RequestJSON)
	}
	if !strings.Contains(task.RequestJSON, "proxy-input://"+body.TaskID+"/input_") {
		t.Fatalf("request_json missing proxy-input ref: %s", task.RequestJSON)
	}
	files, err := store.ListInputSpoolFiles(context.Background(), body.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Extension != ".png" || files[0].RelativePath == "" {
		t.Fatalf("spool files=%+v", files)
	}
	if _, err := os.Stat(filepath.Join(spooler.Root(), filepath.FromSlash(files[0].RelativePath))); err != nil {
		t.Fatalf("spooled file missing: %v", err)
	}
}

func TestCreateStoresVideoDataURIInInputSpool(t *testing.T) {
	store := apiStore(t, 0)
	spooler := inputspool.New(filepath.Join(t.TempDir(), "temp-inputs"))
	handler := NewHandler(Dependencies{
		Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}},
		Profiles: profiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), InputSpooler: spooler,
	})
	payload := `{"model":"MiniMax-H3","content":[{"type":"text","text":"保持一致"},{"type":"video_url","role":"reference_video","video_url":{"url":"data:video/mp4;base64,AAAAFGZ0eXBpc29tAAAAAA=="}}],"resolution":"768P","duration":5,"ratio":"16:9"}`
	created := request(t, handler, http.MethodPost, "/v2/video_generation", []byte(payload), "key-a")
	if created.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var body struct {
		TaskID string `json:"task_id"`
	}
	decode(t, created, &body)
	files, err := store.ListInputSpoolFiles(context.Background(), body.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].ContentType != "video_url" || files[0].MediaType != "video/mp4" || files[0].Extension != ".mp4" {
		t.Fatalf("video spool files=%+v", files)
	}
	task, err := store.Get(context.Background(), "owner-a", body.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(task.RequestJSON, ";base64,") || !strings.Contains(task.RequestJSON, "proxy-input://"+body.TaskID+"/") {
		t.Fatalf("request JSON=%s", task.RequestJSON)
	}
}

func TestCreateSharesOneResolutionProfileAcrossAllScenarios(t *testing.T) {
	profileConfig := domain.ProfileConfig{
		Resolution: "2K",
		Generation: domain.GenerationProfile{ModelMode: "high_quality", Steps: 8, SageAttention: "auto", CacheMode: "off"},
		Ratios: map[string]domain.RatioMapping{
			"adaptive": {BaseWidth: 832, BaseHeight: 480, TargetWidth: 832, TargetHeight: 480},
			"16:9":     {BaseWidth: 832, BaseHeight: 480, TargetWidth: 832, TargetHeight: 480},
		},
		LoRAs: []domain.LoRAProfile{},
	}
	encoded, err := json.Marshal(profileConfig)
	if err != nil {
		t.Fatal(err)
	}
	profiles := &resolutionProfileStub{profile: domain.ModelRequestProfile{ID: "profile-2k", Resolution: "2K", ConfigJSON: string(encoded), ConfigHash: "sha256:test"}}
	store := &createSpyStore{}
	handler := NewHandler(Dependencies{
		Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}}, Profiles: profilesForValidation(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ActiveProfiles: profiles,
	})
	tests := []struct {
		name, body, scenario string
	}{
		{name: "text", body: `{"model":"MiniMax-H3","content":[{"type":"text","text":"x"}],"resolution":"2K","duration":5,"ratio":"16:9"}`, scenario: "t2va"},
		{name: "image", body: `{"model":"MiniMax-H3","content":[{"type":"text","text":"x"},{"type":"image_url","image_url":{"url":"https://example.com/first.png"},"role":"first_frame"}],"resolution":"2K","duration":5}`, scenario: "i2va"},
		{name: "reference", body: `{"model":"MiniMax-H3","content":[{"type":"text","text":"x"},{"type":"image_url","image_url":{"url":"https://example.com/ref.png"},"role":"reference_image"}],"resolution":"2K","duration":5}`, scenario: "r2va"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := request(t, handler, http.MethodPost, "/v2/video_generation", []byte(test.body), "key-a")
			if response.Code != http.StatusOK || store.lastTask.Scenario != test.scenario || store.lastTask.ProfileID != "" || store.lastTask.ConfigHash != "sha256:test" {
				t.Fatalf("status=%d scenario=%q profile=%q body=%s", response.Code, store.lastTask.Scenario, store.lastTask.ProfileID, response.Body.String())
			}
		})
	}
	if len(profiles.resolutions) != 3 {
		t.Fatalf("profile lookups=%v", profiles.resolutions)
	}
	for _, resolution := range profiles.resolutions {
		if resolution != "2K" {
			t.Fatalf("profile lookups=%v", profiles.resolutions)
		}
	}
}

func TestCreateResolvesDynamicResolutionBeforePersisting(t *testing.T) {
	profileConfig := domain.ProfileConfig{
		Resolution: "1080P",
		Generation: domain.GenerationProfile{ModelMode: "high_quality", Steps: 8, SageAttention: "auto", CacheMode: "off"},
		Ratios: map[string]domain.RatioMapping{
			"16:9": {BaseWidth: 1920, BaseHeight: 1088, TargetWidth: 1920, TargetHeight: 1088},
		},
		LoRAs: []domain.LoRAProfile{},
	}
	encoded, err := json.Marshal(profileConfig)
	if err != nil {
		t.Fatal(err)
	}
	profiles := &resolutionProfileStub{profile: domain.ModelRequestProfile{ID: "profile-1080p", Resolution: "1080P", ResolutionKey: "1080p", ConfigJSON: string(encoded), ConfigHash: "sha256:dynamic"}}
	store := &createSpyStore{}
	handler := NewHandler(Dependencies{
		Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ActiveProfiles: profiles,
	})
	response := request(t, handler, http.MethodPost, "/v2/video_generation", []byte(`{"model":"MiniMax-H3","content":[{"type":"text","text":"x"}],"resolution":" 1080p ","duration":5,"ratio":"16:9"}`), "key-a")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if store.lastTask.Resolution != "1080P" || store.lastTask.ConfigHash != "sha256:dynamic" || len(store.lastTask.Stages) == 0 {
		t.Fatalf("task=%+v", store.lastTask)
	}
	var persisted CreateRequest
	if err := json.Unmarshal([]byte(store.lastTask.RequestJSON), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Resolution != "1080P" {
		t.Fatalf("persisted resolution=%q", persisted.Resolution)
	}
	var stage struct {
		Parameters map[string]any `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(store.lastTask.Stages[0].ConfigSnapshotJSON), &stage); err != nil {
		t.Fatal(err)
	}
	if stage.Parameters["width"] != float64(1920) || stage.Parameters["height"] != float64(1088) {
		t.Fatalf("parameters=%+v", stage.Parameters)
	}
}

func TestCreateDynamicResolutionCaseVariantsShareIdempotencyHash(t *testing.T) {
	profileConfig := domain.ProfileConfig{
		Resolution: "1080P",
		Generation: domain.GenerationProfile{ModelMode: "high_quality", Steps: 8, SageAttention: "auto", CacheMode: "off"},
		Ratios: map[string]domain.RatioMapping{
			"16:9": {BaseWidth: 1920, BaseHeight: 1088, TargetWidth: 1920, TargetHeight: 1088},
		},
		LoRAs: []domain.LoRAProfile{},
	}
	encoded, err := json.Marshal(profileConfig)
	if err != nil {
		t.Fatal(err)
	}
	profiles := &resolutionProfileStub{profile: domain.ModelRequestProfile{ID: "profile-1080p", Resolution: "1080P", ResolutionKey: "1080p", ConfigJSON: string(encoded), ConfigHash: "sha256:dynamic"}}
	store := &idempotentCreateSpyStore{}
	handler := NewHandler(Dependencies{
		Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ActiveProfiles: profiles,
	})
	create := func(resolution string) *httptest.ResponseRecorder {
		body := []byte(fmt.Sprintf(`{"model":"MiniMax-H3","content":[{"type":"text","text":"x"}],"resolution":%q,"duration":5,"ratio":"16:9"}`, resolution))
		req := httptest.NewRequest(http.MethodPost, "/v2/video_generation", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer key-a")
		req.Header.Set("Idempotency-Key", "dynamic-resolution-case")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	first := create("1080P")
	replay := create(" 1080p ")
	if first.Code != http.StatusOK || replay.Code != http.StatusOK {
		t.Fatalf("first=%d %s replay=%d %s", first.Code, first.Body.String(), replay.Code, replay.Body.String())
	}
	var firstBody, replayBody struct {
		TaskID string `json:"task_id"`
	}
	decode(t, first, &firstBody)
	decode(t, replay, &replayBody)
	if firstBody.TaskID == "" || replayBody.TaskID != firstBody.TaskID {
		t.Fatalf("first=%q replay=%q", firstBody.TaskID, replayBody.TaskID)
	}
}

func TestCreateRejectsUnknownDynamicResolutionBeforeStore(t *testing.T) {
	profiles := &resolutionProfileStub{err: domain.ErrProfileNotFound}
	store := &createSpyStore{}
	handler := NewHandler(Dependencies{
		Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ActiveProfiles: profiles,
	})
	response := request(t, handler, http.MethodPost, "/v2/video_generation", []byte(`{"model":"MiniMax-H3","content":[{"type":"text","text":"x"}],"resolution":"missing","duration":5,"ratio":"16:9"}`), "key-a")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "请求分辨率不存在或未配置") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if store.createCalls != 0 {
		t.Fatalf("create calls=%d", store.createCalls)
	}
}

type resolutionProfileStub struct {
	profile     domain.ModelRequestProfile
	resolutions []string
	err         error
}

func (s *resolutionProfileStub) GetProfileByResolution(_ context.Context, resolution string) (domain.ModelRequestProfile, error) {
	s.resolutions = append(s.resolutions, resolution)
	return s.profile, s.err
}

func profilesForValidation() map[string]config.GenerationProfile { return profiles() }

func TestCreateRejectsNullAndWhitespacePrompt(t *testing.T) {
	store := apiStore(t, 0)
	handler := NewHandler(Dependencies{Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}}, Profiles: profiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	tests := []struct {
		name string
		text string
	}{
		{name: "null", text: "null"},
		{name: "empty", text: `""`},
		{name: "whitespace", text: `" \t\r\n"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"MiniMax-H3","content":[{"type":"text","text":` + tt.text + `}],"resolution":"2K","duration":4,"ratio":"16:9"}`)
			response := request(t, handler, http.MethodPost, "/v2/video_generation", body, "key-a")
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var payload map[string]any
			decode(t, response, &payload)
			detail, _ := payload["error"].(map[string]any)
			if detail["type"] != "bad_request_error" || !strings.Contains(detail["message"].(string), "非空") {
				t.Fatalf("error=%+v", payload)
			}
		})
	}
}

func TestSucceededArtifactUsesOwnerBoundSignedURL(t *testing.T) {
	signer := &signerSpy{url: "https://proxy.example/v2/files/artifact-1/content?expires=1&signature=signed"}
	h := &handler{artifactURLs: signer}
	response, err := h.mapTask(context.Background(), domain.Task{TaskID: "task-1", APIKeyID: "owner-a", Model: "MiniMax-H3", Status: domain.StatusSucceeded, ResultArtifactID: "artifact-1", CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if response.Content == nil || response.Content.URL != signer.url || signer.artifactID != "artifact-1" || signer.ownerID != "owner-a" {
		t.Fatalf("response=%+v signer=%+v", response, signer)
	}
}

func TestSucceededLegacyURLDoesNotExposePrivateNodeAddress(t *testing.T) {
	h := &handler{}
	unsafe, err := h.mapTask(context.Background(), domain.Task{
		TaskID: "task-private", Model: "MiniMax-H3", Status: domain.StatusSucceeded,
		ResultPublicURL: "http://127.0.0.1:7860/gradio_api/file=video.mp4",
		CreatedAt:       time.Unix(1, 0), UpdatedAt: time.Unix(2, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if unsafe.Content != nil {
		t.Fatalf("private legacy URL exposed: %+v", unsafe.Content)
	}

	safe, err := h.mapTask(context.Background(), domain.Task{
		TaskID: "task-public", Model: "MiniMax-H3", Status: domain.StatusSucceeded,
		ResultPublicURL: "https://cdn.example/video.mp4?token=legacy",
		CreatedAt:       time.Unix(1, 0), UpdatedAt: time.Unix(2, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if safe.Content == nil || safe.Content.URL != "https://cdn.example/video.mp4?token=legacy" {
		t.Fatalf("safe legacy URL missing: %+v", safe.Content)
	}
}

func TestSucceededArtifactSigningFailureDoesNotFallBackToLegacyURL(t *testing.T) {
	signerErr := errors.New("signing unavailable")
	h := &handler{artifactURLs: &signerSpy{err: signerErr}}
	response, err := h.mapTask(context.Background(), domain.Task{
		TaskID: "task-1", APIKeyID: "owner-a", Model: "MiniMax-H3", Status: domain.StatusSucceeded,
		ResultArtifactID: "artifact-1", ResultPublicURL: "https://cdn.example/legacy.mp4",
		CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0),
	})
	if !errors.Is(err, signerErr) || response.Content != nil {
		t.Fatalf("mapTask() = %+v, %v; want signing error without fallback", response, err)
	}
}

func TestFailedTaskUsesLocalizedOfficialFeedback(t *testing.T) {
	h := &handler{}
	response, err := h.mapTask(context.Background(), domain.Task{
		TaskID: "failed-sensitive", Model: "MiniMax-H3", Status: domain.StatusFailed,
		ErrorCode: "official_submit_failed", ErrorMessage: "官方任务提交失败",
		UpstreamFeedback: &domain.UpstreamFeedback{Code: "1027", Message: "text content contains sensitive content (1027)"},
		CreatedAt:        time.Unix(1, 0), UpdatedAt: time.Unix(2, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != "1027" || response.Error.Message != "模型生成内容触发安全审核，需要修改输入后重新生成" {
		t.Fatalf("error=%+v", response.Error)
	}
}

func TestFailedTaskKeepsStableErrorForUnknownOfficialFeedback(t *testing.T) {
	h := &handler{}
	response, err := h.mapTask(context.Background(), domain.Task{
		TaskID: "failed-unknown", Model: "MiniMax-H3", Status: domain.StatusFailed,
		ErrorCode: "official_submit_failed", ErrorMessage: "官方任务提交失败",
		UpstreamFeedback: &domain.UpstreamFeedback{Code: "9999", Message: "unreviewed upstream message"},
		CreatedAt:        time.Unix(1, 0), UpdatedAt: time.Unix(2, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != "official_submit_failed" || response.Error.Message != "官方任务提交失败" {
		t.Fatalf("error=%+v", response.Error)
	}
}

func TestSucceededArtifactURLIgnoresUntrustedForwardingHeaders(t *testing.T) {
	store := &fixedTaskStore{task: domain.Task{
		TaskID: "task-1", APIKeyID: "owner-a", Model: "MiniMax-H3", Status: domain.StatusSucceeded,
		ResultArtifactID: "artifact-1", CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0),
	}}
	signer := &signerSpy{url: "https://proxy.example/v2/files/artifact-1/content?expires=1&signature=signed"}
	handler := NewHandler(Dependencies{
		Store: store, ArtifactURLs: signer,
		APIKeys:  []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}},
		Profiles: profiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	req := httptest.NewRequest(http.MethodGet, "/v2/query/video_generation/task-1", nil)
	req.Host = "attacker.example"
	req.Header.Set("Authorization", "Bearer key-a")
	req.Header.Set("Forwarded", "host=attacker.example;proto=http")
	req.Header.Set("X-Forwarded-Host", "attacker.example")
	req.Header.Set("X-Forwarded-Proto", "http")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Task TaskResponse `json:"task"`
	}
	decode(t, response, &body)
	if body.Task.Content == nil || body.Task.Content.URL != signer.url || strings.Contains(body.Task.Content.URL, "attacker.example") {
		t.Fatalf("content=%+v", body.Task.Content)
	}
}

func TestListAddsAbsoluteArtifactURLOnlyToSucceededTasks(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	store := &fixedTaskStore{items: []domain.Task{
		{TaskID: "succeeded", APIKeyID: "owner-a", Model: "MiniMax-H3", Status: domain.StatusSucceeded, ResultArtifactID: "artifact-1", CreatedAt: now, UpdatedAt: now},
		{TaskID: "running", APIKeyID: "owner-a", Model: "MiniMax-H3", Status: domain.StatusRunning, CreatedAt: now, UpdatedAt: now},
		{TaskID: "failed", APIKeyID: "owner-a", Model: "MiniMax-H3", Status: domain.StatusFailed, ErrorCode: "official_submit_failed", ErrorMessage: "官方任务提交失败", UpstreamFeedback: &domain.UpstreamFeedback{Code: "1027"}, CreatedAt: now, UpdatedAt: now},
	}}
	signer := &signerSpy{url: "https://proxy.example/v2/files/artifact-1/content?expires=1&signature=signed"}
	handler := NewHandler(Dependencies{
		Store: store, ArtifactURLs: signer,
		APIKeys:  []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}},
		Profiles: profiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	response := request(t, handler, http.MethodGet, "/v2/query/video_generation?page_num=1&page_size=20", nil, "key-a")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Items []TaskResponse `json:"items"`
		Total int            `json:"total"`
	}
	decode(t, response, &body)
	if body.Total != 3 || len(body.Items) != 3 {
		t.Fatalf("body=%+v", body)
	}
	if body.Items[0].Content == nil || body.Items[0].Content.URL != signer.url || body.Items[1].Content != nil || body.Items[2].Content != nil {
		t.Fatalf("items=%+v", body.Items)
	}
	if body.Items[2].Error == nil || body.Items[2].Error.Code != "1027" || body.Items[2].Error.Message != "模型生成内容触发安全审核，需要修改输入后重新生成" {
		t.Fatalf("failed error=%+v", body.Items[2].Error)
	}
}

type signerSpy struct {
	url, artifactID, ownerID string
	err                      error
}

type fixedTaskStore struct {
	task  domain.Task
	items []domain.Task
}

func (s *fixedTaskStore) Create(context.Context, domain.NewTask, string, func() bool) (domain.Task, error) {
	return domain.Task{}, errors.New("unexpected Create call")
}
func (s *fixedTaskStore) Get(context.Context, string, string) (domain.Task, error) {
	return s.task, nil
}
func (s *fixedTaskStore) List(context.Context, string, domain.TaskFilter) ([]domain.Task, int, error) {
	if s.items != nil {
		return s.items, len(s.items), nil
	}
	return []domain.Task{s.task}, 1, nil
}
func (s *fixedTaskStore) CancelOrDelete(context.Context, string, string) (domain.Action, error) {
	return "", errors.New("unexpected CancelOrDelete call")
}

func (s *signerSpy) SignURL(_ context.Context, artifactID, ownerID string) (string, error) {
	s.artifactID, s.ownerID = artifactID, ownerID
	return s.url, s.err
}

func TestFreezeStagesBuildsNodeExecutionContracts(t *testing.T) {
	config := domain.ProfileConfig{
		Resolution: "2K",
		Generation: domain.GenerationProfile{ModelMode: "high_quality", Steps: 8, SageAttention: "auto", CacheMode: "te_speed"},
		Ratios: map[string]domain.RatioMapping{
			"16:9": {BaseWidth: 832, BaseHeight: 480, TargetWidth: 2496, TargetHeight: 1440},
		},
		LoRAs:         []domain.LoRAProfile{{Name: "style.safetensors", Strength: 0.75}},
		Interpolation: domain.InterpolationProfile{Enabled: true, Engine: "rife", Scale: 2},
		Restoration:   domain.RestorationProfile{Enabled: true, Engine: "seedvr2", Scale: 3},
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	watermark := true
	validated := ValidatedRequest{CreateRequest: CreateRequest{Model: "MiniMax-H3", Resolution: "2K", Duration: 5, Ratio: "16:9", AIGCWatermark: &watermark}, Scenario: "t2va", Prompt: "海边日落"}
	stages, err := freezeStages("task-1", validated, string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 4 {
		t.Fatalf("stage count=%d, want 4", len(stages))
	}
	wantTypes := []string{"generation", "interpolation", "restoration", "watermark"}
	for index, stage := range stages {
		if stage.StageType != wantTypes[index] {
			t.Fatalf("stage[%d].type=%q", index, stage.StageType)
		}
		var snapshot struct {
			StageType     string         `json:"stage_type"`
			Parameters    map[string]any `json:"parameters"`
			ExpectedMedia map[string]any `json:"expected_media"`
		}
		if err := json.Unmarshal([]byte(stage.ConfigSnapshotJSON), &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.StageType != stage.StageType || len(snapshot.Parameters) == 0 || snapshot.ExpectedMedia["preserve_audio"] != true {
			t.Fatalf("stage[%d] snapshot=%+v", index, snapshot)
		}
	}
	var generation struct {
		Parameters map[string]any `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(stages[0].ConfigSnapshotJSON), &generation); err != nil {
		t.Fatal(err)
	}
	if generation.Parameters["scenario"] != "t2va" || generation.Parameters["width"] != float64(832) || generation.Parameters["height"] != float64(480) || generation.Parameters["fps"] != float64(24) || generation.Parameters["fl2va_model"] != "__follow_model_mode__" || generation.Parameters["ref2va_model"] != "__follow_model_mode__" || generation.Parameters["te_speed_enabled"] != true || generation.Parameters["easycache_enabled"] != false {
		t.Fatalf("generation parameters=%+v", generation.Parameters)
	}
	var restoration struct {
		Parameters map[string]any `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(stages[2].ConfigSnapshotJSON), &restoration); err != nil {
		t.Fatal(err)
	}
	if restoration.Parameters["target_width"] != float64(2496) || restoration.Parameters["target_height"] != float64(1440) {
		t.Fatalf("restoration parameters=%+v", restoration.Parameters)
	}
	for _, index := range []int{1, 2} {
		var postprocess struct {
			Parameters map[string]any `json:"parameters"`
		}
		if err := json.Unmarshal([]byte(stages[index].ConfigSnapshotJSON), &postprocess); err != nil {
			t.Fatal(err)
		}
		if _, exists := postprocess.Parameters["av_sync_tolerance_ms"]; exists {
			t.Fatalf("stage[%d] still contains av_sync_tolerance_ms: %+v", index, postprocess.Parameters)
		}
	}
}

func TestFreezeStagesDefaultsToNoWatermark(t *testing.T) {
	profileConfig := domain.ProfileConfig{
		Generation: domain.GenerationProfile{Steps: 8},
		Ratios:     map[string]domain.RatioMapping{"16:9": {BaseWidth: 832, BaseHeight: 480}},
	}
	encoded, err := json.Marshal(profileConfig)
	if err != nil {
		t.Fatal(err)
	}
	stages, err := freezeStages("task-legacy", ValidatedRequest{CreateRequest: CreateRequest{Duration: 5, Ratio: "16:9"}, Scenario: "t2va", Prompt: "legacy"}, string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Parameters map[string]any `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(stages[0].ConfigSnapshotJSON), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Parameters["fps"] != float64(24) {
		t.Fatalf("legacy fps=%v, want 24", snapshot.Parameters["fps"])
	}
	if len(stages) != 1 {
		t.Fatalf("stages=%v, watermark should be omitted by default", stages)
	}
}

func TestCreateChecksAvailabilityAfterRequestValidationWithoutSideEffects(t *testing.T) {
	store := &createSpyStore{}
	wakeCalls := 0
	handler := NewHandler(Dependencies{
		Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}},
		Profiles: profiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Available: func() bool { return false }, Wake: func() { wakeCalls++ },
	})

	tests := []struct {
		name        string
		body        []byte
		configure   func(*http.Request)
		wantStatus  int
		wantErrType string
	}{
		{name: "valid request", body: validCreateJSON(), wantStatus: http.StatusServiceUnavailable, wantErrType: "resource_unavailable_error"},
		{name: "invalid json", body: []byte(`{"model":`), wantStatus: http.StatusBadRequest, wantErrType: "bad_request_error"},
		{name: "invalid business field", body: []byte(`{"model":"MiniMax-H3","content":[],"resolution":"2K","duration":5,"ratio":"16:9"}`), wantStatus: http.StatusBadRequest, wantErrType: "bad_request_error"},
		{name: "invalid idempotency key", body: validCreateJSON(), configure: func(r *http.Request) { r.Header.Set("Idempotency-Key", "\n") }, wantStatus: http.StatusBadRequest, wantErrType: "bad_request_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v2/video_generation", bytes.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer key-a")
			if test.configure != nil {
				test.configure(req)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			var body ErrorResponse
			decode(t, response, &body)
			if body.Type != "error" || body.Error.Type != test.wantErrType {
				t.Fatalf("error = %+v, want type %q", body, test.wantErrType)
			}
			if test.wantStatus == http.StatusServiceUnavailable && (body.Error.Message != "资源不足，请稍后重试" || body.Error.HTTPCode != "503") {
				t.Fatalf("unavailable error = %+v", body.Error)
			}
		})
	}
	if store.createCalls != 0 || wakeCalls != 0 {
		t.Fatalf("side effects: Create=%d Wake=%d", store.createCalls, wakeCalls)
	}
}

func TestCreateAvailabilityPreservesIdempotencySemantics(t *testing.T) {
	store := apiStore(t, 0)
	available := true
	wakeCalls := 0
	handler := NewHandler(Dependencies{
		Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}},
		Profiles: profiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Available: func() bool { return available }, Wake: func() { wakeCalls++ },
	})
	create := func(key string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v2/video_generation", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer key-a")
		req.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}

	first := create("same-key", validCreateJSON())
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var firstBody struct {
		TaskID string `json:"task_id"`
	}
	decode(t, first, &firstBody)
	available = false
	replay := create("same-key", validCreateJSON())
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayBody struct {
		TaskID string `json:"task_id"`
	}
	decode(t, replay, &replayBody)
	if replayBody.TaskID != firstBody.TaskID {
		t.Fatalf("replay task_id=%q want=%q", replayBody.TaskID, firstBody.TaskID)
	}
	conflictBody := bytes.Replace(validCreateJSON(), []byte("海边日落"), []byte("山间日出"), 1)
	conflict := create("same-key", conflictBody)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	wakesBeforeUnavailable := wakeCalls
	unavailable := create("new-key", validCreateJSON())
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d body=%s", unavailable.Code, unavailable.Body.String())
	}
	if wakeCalls != wakesBeforeUnavailable {
		t.Fatalf("unavailable request woke scheduler: before=%d after=%d", wakesBeforeUnavailable, wakeCalls)
	}
	items, total, err := store.List(context.Background(), "owner-a", domain.TaskFilter{PageNum: 1, PageSize: 20})
	if err != nil || total != 1 || len(items) != 1 || items[0].TaskID != firstBody.TaskID {
		t.Fatalf("persisted tasks=%+v total=%d error=%v", items, total, err)
	}
}

func TestDeleteRespectsProtectedQueue(t *testing.T) {
	store := apiStore(t, 1)
	handler := NewHandler(Dependencies{Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}}, Profiles: profiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	first := createViaAPI(t, handler)
	second := createViaAPI(t, handler)
	locked := request(t, handler, http.MethodDelete, "/v2/video_generation/"+first, nil, "key-a")
	if locked.Code != http.StatusBadRequest {
		t.Fatalf("locked delete status=%d body=%s", locked.Code, locked.Body.String())
	}
	open := request(t, handler, http.MethodDelete, "/v2/video_generation/"+second, nil, "key-a")
	if open.Code != http.StatusOK {
		t.Fatalf("open delete status=%d body=%s", open.Code, open.Body.String())
	}
	var result map[string]string
	decode(t, open, &result)
	if result["action"] != "cancelled" {
		t.Fatalf("delete result = %+v", result)
	}
}

func TestInternalErrorsDoNotLeakSensitiveDetailsToLogs(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := NewHandler(Dependencies{Store: failingStore{err: errors.New("dial http://private.local?token=secret failed")}, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}}, Profiles: profiles(), Logger: logger})
	response := request(t, handler, http.MethodPost, "/v2/video_generation", validCreateJSON(), "key-a")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	if output := logs.String(); strings.Contains(output, "private.local") || strings.Contains(output, "secret") {
		t.Fatalf("sensitive log = %s", output)
	}
}

type failingStore struct{ err error }

type createSpyStore struct {
	createCalls int
	lastTask    domain.NewTask
}

type idempotentCreateSpyStore struct {
	task        domain.Task
	keyHash     string
	requestHash string
}

func (s *idempotentCreateSpyStore) Create(_ context.Context, input domain.NewTask, keyHash string, _ func() bool) (domain.Task, error) {
	if s.keyHash != "" {
		if s.keyHash != keyHash || s.requestHash != input.RequestHash {
			return domain.Task{}, domain.ErrIdempotencyConflict
		}
		return s.task, nil
	}
	s.keyHash, s.requestHash = keyHash, input.RequestHash
	s.task = domain.Task{TaskID: input.TaskID}
	return s.task, nil
}

func (s *idempotentCreateSpyStore) Get(context.Context, string, string) (domain.Task, error) {
	return domain.Task{}, domain.ErrTaskNotFound
}

func (s *idempotentCreateSpyStore) List(context.Context, string, domain.TaskFilter) ([]domain.Task, int, error) {
	return nil, 0, nil
}

func (s *idempotentCreateSpyStore) CancelOrDelete(context.Context, string, string) (domain.Action, error) {
	return "", domain.ErrTaskNotFound
}

func (s *createSpyStore) Create(_ context.Context, task domain.NewTask, _ string, available func() bool) (domain.Task, error) {
	if available != nil && !available() {
		return domain.Task{}, domain.ErrResourceUnavailable
	}
	s.createCalls++
	s.lastTask = task
	return domain.Task{}, nil
}
func (s *createSpyStore) Get(context.Context, string, string) (domain.Task, error) {
	return domain.Task{}, domain.ErrTaskNotFound
}
func (s *createSpyStore) List(context.Context, string, domain.TaskFilter) ([]domain.Task, int, error) {
	return nil, 0, nil
}
func (s *createSpyStore) CancelOrDelete(context.Context, string, string) (domain.Action, error) {
	return "", domain.ErrTaskNotFound
}

func (s failingStore) Create(context.Context, domain.NewTask, string, func() bool) (domain.Task, error) {
	return domain.Task{}, s.err
}
func (s failingStore) Get(context.Context, string, string) (domain.Task, error) {
	return domain.Task{}, s.err
}
func (s failingStore) List(context.Context, string, domain.TaskFilter) ([]domain.Task, int, error) {
	return nil, 0, s.err
}
func (s failingStore) CancelOrDelete(context.Context, string, string) (domain.Action, error) {
	return "", s.err
}

func apiStore(t *testing.T, protected int) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"), sqlite.Options{ProtectedSlots: protected, PerKeyLimit: 10, GlobalLimit: 100, Retention: 7 * 24 * time.Hour, IdempotencyTTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func request(t *testing.T, handler http.Handler, method, path string, body []byte, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

type mutableBearerAuthenticator struct {
	owner string
	token string
}

func (a *mutableBearerAuthenticator) Authenticate(token string) (string, bool) {
	return a.owner, a.owner != "" && token == a.token
}

func TestHandlerUsesLiveBearerAuthenticator(t *testing.T) {
	authenticator := &mutableBearerAuthenticator{owner: "owner-a", token: "key-a"}
	handler := NewHandler(Dependencies{
		Store: &createSpyStore{}, Authenticator: authenticator, Profiles: profiles(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	first := request(t, handler, http.MethodPost, "/v2/video_generation", validCreateJSON(), "key-a")
	if first.Code == http.StatusUnauthorized {
		t.Fatalf("enabled key was rejected: %s", first.Body.String())
	}
	authenticator.owner = ""
	second := request(t, handler, http.MethodPost, "/v2/video_generation", validCreateJSON(), "key-a")
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("disabled key status=%d body=%s", second.Code, second.Body.String())
	}
}

func createViaAPI(t *testing.T, handler http.Handler) string {
	t.Helper()
	response := request(t, handler, http.MethodPost, "/v2/video_generation", validCreateJSON(), "key-a")
	if response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		TaskID string `json:"task_id"`
	}
	decode(t, response, &body)
	return body.TaskID
}

func validCreateJSON() []byte {
	return []byte(`{"model":"MiniMax-H3","content":[{"type":"text","text":"海边日落"}],"resolution":"2K","duration":5,"ratio":"16:9"}`)
}
func decode(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode %s: %v", response.Body.String(), err)
	}
}
