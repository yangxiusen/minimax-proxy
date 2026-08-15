package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"minimax-h3-tc/internal/authkey"
	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	managerapi "minimax-h3-tc/internal/httpapi/manager"
	"minimax-h3-tc/internal/httpapi/v2"
	monitorcache "minimax-h3-tc/internal/monitor"
	storepkg "minimax-h3-tc/internal/store/sqlite"
)

func TestWarnDefaultAdminPasswordDependsOnlyOnPasswordAndDoesNotLogIt(t *testing.T) {
	tests := []struct {
		name     string
		admin    config.AdminConfig
		wantWarn bool
	}{
		{name: "custom username with default password", admin: config.AdminConfig{Username: "operator", Password: "123"}, wantWarn: true},
		{name: "strong password", admin: config.AdminConfig{Username: "admin", Password: "strong-password"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, nil))
			warnDefaultAdminPassword(logger, test.admin)
			warned := output.Len() > 0
			if warned != test.wantWarn {
				t.Fatalf("warned = %v, want %v; log=%s", warned, test.wantWarn, output.String())
			}
			if bytes.Contains(output.Bytes(), []byte(`"password"`)) || bytes.Contains(output.Bytes(), []byte(`"123"`)) {
				t.Fatalf("log contains password: %s", output.String())
			}
		})
	}
}

func TestNodeCacheAvailabilityUsesFreshEnabledSnapshots(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	cache := monitorcache.NewCache([]monitorcache.NodeSnapshot{{ID: "gpu-1"}, {ID: "gpu-2"}})
	available := cacheAvailability(cache, func() time.Time { return now }, 10*time.Second)
	if available() {
		t.Fatal("unknown nodes must be unavailable")
	}

	cache.Update("gpu-2", func(node *monitorcache.NodeSnapshot) {
		node.Health = monitorcache.HealthHealthy
		node.Runtime = monitorcache.RuntimeRunning
		node.CheckedAt = now.Add(-9 * time.Second)
	})
	if !available() {
		t.Fatal("one fresh healthy node must be available even when running")
	}

	cache.Update("gpu-2", func(node *monitorcache.NodeSnapshot) { node.CheckedAt = now.Add(-11 * time.Second) })
	if available() {
		t.Fatal("stale health must be unavailable")
	}
	cache.Update("gpu-2", func(node *monitorcache.NodeSnapshot) {
		node.Health = monitorcache.HealthHealthy
		node.CheckedAt = now
		node.Disabled = true
	})
	if available() {
		t.Fatal("disabled nodes must be unavailable")
	}
}

func TestAppHandlerKeepsManagerCookieAndV2BearerIsolated(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	cache := monitorcache.NewCache([]monitorcache.NodeSnapshot{{ID: "gpu-1", Health: monitorcache.HealthHealthy, CheckedAt: now}})
	store := &appStore{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	v2Handler := v2.NewHandler(v2.Dependencies{
		Store: store, APIKeys: []config.APIKeyConfig{{ID: "customer-a", Key: "api-secret", Enabled: true}},
		Profiles: map[string]config.GenerationProfile{"2K": {Dimensions: map[string]config.Dimension{"16:9": {Width: 1920, Height: 1080}}}},
		Logger:   logger, Available: cacheAvailability(cache, func() time.Time { return now }, 10*time.Second),
	})
	managerHandler := managerapi.NewHandler(managerapi.Dependencies{
		Admin: config.AdminConfig{Username: "admin", Password: "password", SessionTTL: time.Hour}, Cache: cache, Store: store,
		Logger: logger, Now: func() time.Time { return now }, Rand: bytes.NewReader(bytes.Repeat([]byte{1}, 32)),
	})
	handler := newAppHandler(v2Handler, http.NotFoundHandler(), managerHandler)

	login := httptest.NewRequest(http.MethodPost, "/manager/api/session", bytes.NewBufferString(`{"username":"admin","password":"password"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusNoContent {
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	cookie := loginResponse.Result().Cookies()[0]

	v2WithCookie := httptest.NewRequest(http.MethodPost, "/v2/video_generation", bytes.NewBufferString(`{"model":"MiniMax-H3","content":[{"type":"text","text":"test"}],"resolution":"2K","duration":5,"ratio":"16:9"}`))
	v2WithCookie.Header.Set("Content-Type", "application/json")
	v2WithCookie.AddCookie(cookie)
	v2Response := httptest.NewRecorder()
	handler.ServeHTTP(v2Response, v2WithCookie)
	if v2Response.Code != http.StatusUnauthorized {
		t.Fatalf("monitor cookie authorized V2: status=%d", v2Response.Code)
	}

	monitorWithBearer := httptest.NewRequest(http.MethodGet, "/manager/api/snapshot", nil)
	monitorWithBearer.Header.Set("Authorization", "Bearer api-secret")
	monitorResponse := httptest.NewRecorder()
	handler.ServeHTTP(monitorResponse, monitorWithBearer)
	if monitorResponse.Code != http.StatusUnauthorized {
		t.Fatalf("V2 bearer authorized monitor: status=%d", monitorResponse.Code)
	}

	v2Request := httptest.NewRequest(http.MethodPost, "/v2/video_generation", bytes.NewBufferString(`{"model":"MiniMax-H3","content":[{"type":"text","text":"test"}],"resolution":"2K","duration":5,"ratio":"16:9"}`))
	v2Request.Header.Set("Content-Type", "application/json")
	v2Request.Header.Set("Authorization", "Bearer api-secret")
	v2OK := httptest.NewRecorder()
	handler.ServeHTTP(v2OK, v2Request)
	if v2OK.Code != http.StatusOK || store.createCalls != 1 {
		t.Fatalf("V2 route status=%d creates=%d body=%s", v2OK.Code, store.createCalls, v2OK.Body.String())
	}
	redirect := httptest.NewRecorder()
	handler.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/monitor", nil))
	if redirect.Code != http.StatusPermanentRedirect || redirect.Header().Get("Location") != "/manager/" {
		t.Fatalf("monitor redirect status=%d location=%q", redirect.Code, redirect.Header().Get("Location"))
	}
}

func TestAppHandlerServesManagerAndRedirectsLegacyMonitor(t *testing.T) {
	managerHandler := managerapi.NewHandler(managerapi.Dependencies{
		Admin: config.AdminConfig{Username: "admin", Password: "password", SessionTTL: time.Hour},
		Cache: monitorcache.NewCache(nil), Store: &appStore{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	handler := newAppHandler(http.NotFoundHandler(), http.NotFoundHandler(), managerHandler)

	manager := httptest.NewRecorder()
	handler.ServeHTTP(manager, httptest.NewRequest(http.MethodGet, "/manager", nil))
	if manager.Code == http.StatusNotFound {
		t.Fatal("/manager was not registered")
	}
	legacy := httptest.NewRecorder()
	handler.ServeHTTP(legacy, httptest.NewRequest(http.MethodGet, "/monitor", nil))
	if legacy.Code != http.StatusPermanentRedirect || legacy.Header().Get("Location") != "/manager/" {
		t.Fatalf("legacy redirect status=%d location=%q", legacy.Code, legacy.Header().Get("Location"))
	}
	legacyAPI := httptest.NewRecorder()
	handler.ServeHTTP(legacyAPI, httptest.NewRequest(http.MethodGet, "/monitor/api/snapshot", nil))
	if legacyAPI.Code != http.StatusNotFound {
		t.Fatalf("legacy API status=%d, want 404", legacyAPI.Code)
	}
}

func TestBootstrapLegacyNodesImportsOnceAndThenIgnoresYAML(t *testing.T) {
	ctx := context.Background()
	store, err := storepkg.Open(ctx, filepath.Join(t.TempDir(), "bootstrap.db"), storepkg.Options{
		ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10, Retention: time.Hour, IdempotencyTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	legacy := []config.LegacyUpstreamConfig{{
		ID: "node-1", BaseURL: "http://private.example:7860", JobsBaseURL: "http://jobs.example:8188", PublicBaseURL: "https://video.example",
		HealthPath: "/", SubmitAPIName: "submit", CheckAPIName: "check", PollInterval: "3s", RequestTimeout: "30s",
	}}
	count, err := bootstrapLegacyNodes(ctx, store, legacy)
	if err != nil || count != 1 {
		t.Fatalf("bootstrap count=%d err=%v", count, err)
	}
	nodes, err := store.ListModelNodes(ctx)
	if err != nil || len(nodes) != 1 || nodes[0].ID != "node-1" || !nodes[0].Enabled {
		t.Fatalf("nodes=%+v err=%v", nodes, err)
	}
	count, err = bootstrapLegacyNodes(ctx, store, []config.LegacyUpstreamConfig{{ID: "broken", BaseURL: "${MISSING_AFTER_IMPORT}"}})
	if err != nil || count != 0 {
		t.Fatalf("second bootstrap count=%d err=%v", count, err)
	}
}

func TestLegacyAPIKeyInputsHashAndMaskSecrets(t *testing.T) {
	inputs, err := legacyAPIKeyInputs([]config.APIKeyConfig{{ID: "customer-a", Key: "legacy-secret", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0].ID != "customer-a" || inputs[0].Name != "customer-a" || inputs[0].KeyPrefix != "lega" || inputs[0].KeySuffix != "cret" {
		t.Fatalf("inputs=%+v", inputs)
	}
	if inputs[0].KeyDigest != authkey.Digest("legacy-secret") {
		t.Fatal("legacy key digest mismatch")
	}
}

func TestBootstrapLegacyNodesDoesNotParseYAMLWhenDatabaseHasNodes(t *testing.T) {
	ctx := context.Background()
	store, err := storepkg.Open(ctx, filepath.Join(t.TempDir(), "existing.db"), storepkg.Options{
		ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 10, Retention: time.Hour, IdempotencyTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	input := domain.ModelNodeInput{
		ID: "db-node", BaseURL: "http://private.example:7860", JobsBaseURL: "http://jobs.example:8188", PublicBaseURL: "https://video.example",
		HealthPath: "/", SubmitAPIName: "submit", CheckAPIName: "check", PollInterval: time.Second, RequestTimeout: time.Second, Enabled: true,
	}
	if _, err := store.CreateModelNode(ctx, input); err != nil {
		t.Fatal(err)
	}
	count, err := bootstrapLegacyNodes(ctx, store, []config.LegacyUpstreamConfig{{ID: "broken", BaseURL: "${MISSING_EXISTING_NODE}"}})
	if err != nil || count != 0 {
		t.Fatalf("bootstrap count=%d err=%v", count, err)
	}
	pending, err := store.LegacyNodeImportPending(ctx)
	if err != nil || pending {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
}

type appStore struct{ createCalls int }

func (s *appStore) Create(_ context.Context, input domain.NewTask, _ string, available func() bool) (domain.Task, error) {
	if available != nil && !available() {
		return domain.Task{}, domain.ErrResourceUnavailable
	}
	s.createCalls++
	return domain.Task{TaskID: input.TaskID, APIKeyID: input.APIKeyID, Status: domain.StatusQueuedOpen}, nil
}
func (*appStore) Get(context.Context, string, string) (domain.Task, error) {
	return domain.Task{}, domain.ErrTaskNotFound
}
func (*appStore) List(context.Context, string, domain.TaskFilter) ([]domain.Task, int, error) {
	return nil, 0, nil
}
func (*appStore) CancelOrDelete(context.Context, string, string) (domain.Action, error) {
	return "", domain.ErrTaskNotFound
}
func (*appStore) ListAdminTasks(context.Context, domain.AdminTaskFilter) ([]domain.AdminTaskSummary, int, error) {
	return nil, 0, nil
}
func (*appStore) GetAdminTaskDetail(context.Context, string) (domain.AdminTaskDetail, error) {
	return domain.AdminTaskDetail{}, domain.ErrTaskNotFound
}
func (*appStore) ListTaskArtifactLocations(context.Context, string) ([]domain.TaskArtifactLocation, error) {
	return nil, nil
}
func (*appStore) GetInputSpoolFile(context.Context, string, string) (domain.InputSpoolFile, error) {
	return domain.InputSpoolFile{}, domain.ErrTaskNotFound
}
func (*appStore) EnsureTaskPurgeReady(context.Context, string) error { return nil }
func (*appStore) RequestAdminCancel(context.Context, string) error   { return domain.ErrTaskNotFound }
func (*appStore) AdminDelete(context.Context, string, ...domain.TaskArtifactLocation) error {
	return domain.ErrTaskNotFound
}
