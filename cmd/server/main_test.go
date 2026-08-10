package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	monitorapi "minimax-h3-tc/internal/httpapi/monitor"
	"minimax-h3-tc/internal/httpapi/v2"
	monitorcache "minimax-h3-tc/internal/monitor"
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

func TestNodeCacheAvailabilityAndCachedHealthUseFreshSnapshots(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	baseURL, _ := url.Parse("http://private.example:7860/internal/path")
	cache := newNodeCache([]config.UpstreamConfig{{ID: "gpu-1", BaseURL: baseURL}, {ID: "gpu-2", BaseURL: baseURL}})

	initial, _ := cache.Get("gpu-1")
	if initial.Address != "private.example:7860" || initial.Health != monitorcache.HealthUnknown {
		t.Fatalf("initial snapshot = %+v", initial)
	}
	available := cacheAvailability(cache, func() time.Time { return now }, 10*time.Second)
	if available() || cachedNodeHealth(cache, "gpu-1", now, 10*time.Second) == nil {
		t.Fatal("unknown nodes must be unavailable")
	}

	cache.Update("gpu-2", func(node *monitorcache.NodeSnapshot) {
		node.Health = monitorcache.HealthHealthy
		node.Runtime = monitorcache.RuntimeRunning
		node.CheckedAt = now.Add(-9 * time.Second)
	})
	if !available() || cachedNodeHealth(cache, "gpu-2", now, 10*time.Second) != nil {
		t.Fatal("one fresh healthy node must be available even when running")
	}

	cache.Update("gpu-2", func(node *monitorcache.NodeSnapshot) { node.CheckedAt = now.Add(-11 * time.Second) })
	if available() || cachedNodeHealth(cache, "gpu-2", now, 10*time.Second) == nil {
		t.Fatal("stale health must be unavailable")
	}
	cache.Update("gpu-2", func(node *monitorcache.NodeSnapshot) {
		node.Health = monitorcache.HealthUnhealthy
		node.CheckedAt = now
	})
	if available() {
		t.Fatal("all unhealthy nodes must be unavailable")
	}
}

func TestCachedNodeSchedulableRejectsPrivateWorkWithoutLocalTask(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	queue := 1
	cache := monitorcache.NewCache([]monitorcache.NodeSnapshot{{ID: "gpu-1", Health: monitorcache.HealthHealthy, Runtime: monitorcache.RuntimeRunning, PrivateQueue: &queue, CheckedAt: now}})
	if err := cachedNodeSchedulable(cache, "gpu-1", now, 10*time.Second); err == nil {
		t.Fatal("running private instance was considered schedulable")
	}
	cache.Update("gpu-1", func(node *monitorcache.NodeSnapshot) {
		node.Runtime = monitorcache.RuntimeIdle
		zero := 0
		node.PrivateQueue = &zero
	})
	if err := cachedNodeSchedulable(cache, "gpu-1", now, 10*time.Second); err != nil {
		t.Fatalf("idle private instance was not schedulable: %v", err)
	}
}

func TestCachedNodeSchedulableRejectsExplicitSchedulingBlock(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	zero := 0
	cache := monitorcache.NewCache([]monitorcache.NodeSnapshot{{ID: "gpu-1", Health: monitorcache.HealthHealthy, Runtime: monitorcache.RuntimeIdle, PrivateQueue: &zero, CheckedAt: now, SchedulingBlocked: true}})
	if err := cachedNodeSchedulable(cache, "gpu-1", now, 10*time.Second); err == nil {
		t.Fatal("explicitly blocked node was considered schedulable")
	}
}

func TestMaxHealthAgeCoversSlowestProbe(t *testing.T) {
	upstreams := []config.UpstreamConfig{
		{ID: "gpu-1", RequestTimeout: 30 * time.Second},
		{ID: "gpu-2", RequestTimeout: 8 * time.Second},
	}
	if got, want := maxHealthSnapshotAge(5*time.Second, upstreams), 35*time.Second; got != want {
		t.Fatalf("maxHealthSnapshotAge() = %s, want %s", got, want)
	}
	if got, want := maxHealthSnapshotAge(20*time.Second, upstreams[:1]), 50*time.Second; got != want {
		t.Fatalf("interval floor = %s, want %s", got, want)
	}
}

func TestRestoreNodeSnapshotsRestoresCurrentAndLatestDuration(t *testing.T) {
	started := time.Unix(2_000_000_000, 0).UTC()
	finished := started.Add(97 * time.Second)
	cache := monitorcache.NewCache([]monitorcache.NodeSnapshot{{ID: "gpu-1"}})
	store := restoreStore{
		active: domain.Task{TaskID: "current-1", Status: domain.StatusReconciling, StartedAt: started},
		latest: domain.AdminTaskSummary{TaskID: "done-1", APIKeyID: "customer-a", Status: domain.V2Succeeded, StartedAt: started, FinishedAt: finished},
	}
	if err := restoreNodeSnapshots(context.Background(), cache, store, []string{"gpu-1"}); err != nil {
		t.Fatal(err)
	}
	node, _ := cache.Get("gpu-1")
	if node.CurrentTask == nil || node.CurrentTask.ID != "current-1" || !node.CurrentTask.StartedAt.Equal(started) || node.Runtime != monitorcache.RuntimeRunning {
		t.Fatalf("current task = %+v runtime=%s", node.CurrentTask, node.Runtime)
	}
	if node.LatestFinishedTask == nil || node.LatestFinishedTask.ID != "done-1" || node.LatestFinishedTask.DurationSeconds != 97 || !node.LatestFinishedTask.FinishedAt.Equal(finished) {
		t.Fatalf("latest task = %+v", node.LatestFinishedTask)
	}
}

func TestAppHandlerKeepsMonitorCookieAndV2BearerIsolated(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	cache := monitorcache.NewCache([]monitorcache.NodeSnapshot{{ID: "gpu-1", Health: monitorcache.HealthHealthy, CheckedAt: now}})
	store := &appStore{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	v2Handler := v2.NewHandler(v2.Dependencies{
		Store: store, APIKeys: []config.APIKeyConfig{{ID: "customer-a", Key: "api-secret", Enabled: true}},
		Profiles: map[string]config.GenerationProfile{"2K": {Dimensions: map[string]config.Dimension{"16:9": {Width: 1920, Height: 1080}}}},
		Logger:   logger, Available: cacheAvailability(cache, func() time.Time { return now }, 10*time.Second),
	})
	monitorHandler := monitorapi.NewHandler(monitorapi.Dependencies{
		Admin: config.AdminConfig{Username: "admin", Password: "password", SessionTTL: time.Hour}, Cache: cache, Store: store,
		Logger: logger, Now: func() time.Time { return now }, Rand: bytes.NewReader(bytes.Repeat([]byte{1}, 32)),
	})
	handler := newAppHandler(v2Handler, monitorHandler)

	login := httptest.NewRequest(http.MethodPost, "/monitor/api/session", bytes.NewBufferString(`{"username":"admin","password":"password"}`))
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

	monitorWithBearer := httptest.NewRequest(http.MethodGet, "/monitor/api/snapshot", nil)
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
	if redirect.Code != http.StatusPermanentRedirect || redirect.Header().Get("Location") != "/monitor/" {
		t.Fatalf("monitor redirect status=%d location=%q", redirect.Code, redirect.Header().Get("Location"))
	}
}

type restoreStore struct {
	active domain.Task
	latest domain.AdminTaskSummary
}

func (s restoreStore) ActiveForUpstream(context.Context, string) (domain.Task, error) {
	if s.active.TaskID == "" {
		return domain.Task{}, domain.ErrTaskNotFound
	}
	return s.active, nil
}
func (s restoreStore) LatestFinishedForUpstream(context.Context, string) (domain.AdminTaskSummary, error) {
	if s.latest.TaskID == "" {
		return domain.AdminTaskSummary{}, domain.ErrTaskNotFound
	}
	return s.latest, nil
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
func (*appStore) RequestAdminCancel(context.Context, string) error { return domain.ErrTaskNotFound }
func (*appStore) AdminDelete(context.Context, string) error        { return domain.ErrTaskNotFound }
