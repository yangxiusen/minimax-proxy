package manager

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	monitorcache "minimax-h3-tc/internal/monitor"
)

type taskStoreStub struct {
	mu            sync.Mutex
	filter        domain.AdminTaskFilter
	items         []domain.AdminTaskSummary
	total         int
	err           error
	cancelledTask string
	deletedTask   string
	cancelError   error
	deleteError   error
}

func (s *taskStoreStub) RequestAdminCancel(_ context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelledTask = taskID
	return s.cancelError
}

func (s *taskStoreStub) AdminDelete(_ context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletedTask = taskID
	return s.deleteError
}

func TestWebRoutesRedirectAuthenticateAndServeEmbeddedAssets(t *testing.T) {
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}})

	root := serve(h, http.MethodGet, "/manager", "", "", "", "192.0.2.10:1", false)
	if root.Code != http.StatusPermanentRedirect || root.Header().Get("Location") != "/manager/" {
		t.Fatalf("root status=%d location=%q", root.Code, root.Header().Get("Location"))
	}
	loginPage := serve(h, http.MethodGet, "/manager/login", "", "", "", "192.0.2.10:1", false)
	if loginPage.Code != http.StatusOK || !strings.Contains(loginPage.Body.String(), "登录") {
		t.Fatalf("login status=%d body=%q", loginPage.Code, loginPage.Body.String())
	}
	protected := serve(h, http.MethodGet, "/manager/", "", "", "", "192.0.2.10:1", false)
	if protected.Code != http.StatusSeeOther || protected.Header().Get("Location") != "/manager/login" {
		t.Fatalf("protected status=%d location=%q", protected.Code, protected.Header().Get("Location"))
	}

	cookie := login(t, h, "admin", "secret", "192.0.2.10:1")
	authorized := serve(h, http.MethodGet, "/manager/", "", "", cookie, "192.0.2.10:1", false)
	if authorized.Code != http.StatusOK || !strings.Contains(authorized.Body.String(), "私有服务监控") {
		t.Fatalf("authorized status=%d body=%q", authorized.Code, authorized.Body.String())
	}
	alreadyLoggedIn := serve(h, http.MethodGet, "/manager/login", "", "", cookie, "192.0.2.10:1", false)
	if alreadyLoggedIn.Code != http.StatusSeeOther || alreadyLoggedIn.Header().Get("Location") != "/manager/" {
		t.Fatalf("logged-in login status=%d location=%q", alreadyLoggedIn.Code, alreadyLoggedIn.Header().Get("Location"))
	}

	for _, testCase := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{"/manager/assets/styles.css", "text/css; charset=utf-8", "@media"},
		{"/manager/assets/login.js", "text/javascript; charset=utf-8", "fetch"},
		{"/manager/assets/manager.js", "text/javascript; charset=utf-8", "fetch"},
	} {
		response := serve(h, http.MethodGet, testCase.path, "", "", "", "192.0.2.10:1", false)
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != testCase.contentType || !strings.Contains(response.Body.String(), testCase.contains) {
			t.Errorf("%s status=%d content-type=%q body=%q", testCase.path, response.Code, response.Header().Get("Content-Type"), response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s Cache-Control=%q", testCase.path, response.Header().Get("Cache-Control"))
		}
	}
	for _, response := range []*httptest.ResponseRecorder{root, loginPage, protected, authorized, alreadyLoggedIn} {
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("status=%d Cache-Control=%q", response.Code, response.Header().Get("Cache-Control"))
		}
	}
}

func TestManagerScriptGuardsTaskRenderingWithRequestGeneration(t *testing.T) {
	script, err := webAssets.ReadFile("web/manager.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, expected := range []string{
		"let taskRequestGeneration",
		"const requestGeneration = ++taskRequestGeneration",
		"requestGeneration !== taskRequestGeneration",
	} {
		if !strings.Contains(source, expected) {
			t.Errorf("manager.js missing stale task response guard %q", expected)
		}
	}
}

func TestManagerScriptConfirmsActionsAndOpensPublicVideo(t *testing.T) {
	script, err := webAssets.ReadFile("web/manager.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, expected := range []string{
		"window.confirm",
		"/cancel`",
		`method: action === "cancel" ? "POST" : "DELETE"`,
		`window.open(item.video_url, "_blank", "noopener,noreferrer")`,
	} {
		if !strings.Contains(source, expected) {
			t.Errorf("manager.js missing task action behavior %q", expected)
		}
	}
}

func TestManagerPageIncludesNodeConfigurationWorkflow(t *testing.T) {
	page, err := webAssets.ReadFile("web/manager.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := webAssets.ReadFile("web/manager.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"open-node-config", "node-config-dialog", "node-config-form", "jobs_base_url", "test-node", "save-node", "delete-node"} {
		if !strings.Contains(string(page), expected) {
			t.Errorf("manager.html missing node configuration control %q", expected)
		}
	}
	for _, expected := range []string{"/manager/api/nodes", "window.confirm", "formDirty", "NodeProbe", "requestJSON"} {
		if !strings.Contains(string(script), expected) {
			t.Errorf("manager.js missing node configuration behavior %q", expected)
		}
	}
}

func (s *taskStoreStub) ListAdminTasks(_ context.Context, filter domain.AdminTaskFilter) ([]domain.AdminTaskSummary, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filter = filter
	return s.items, s.total, s.err
}

func TestSessionLoginIsStrictAndLogoutInvalidatesCookie(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	h := testHandler(Dependencies{
		Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Now:   func() time.Time { return now },
		Rand:  bytes.NewReader(bytes.Repeat([]byte{0xab}, 32)),
	})

	for name, testCase := range map[string]struct {
		contentType string
		body        string
		want        int
		remote      string
	}{
		"wrong username": {"application/json", `{"username":"other","password":"secret"}`, http.StatusUnauthorized, "192.0.2.1:5000"},
		"wrong password": {"application/json", `{"username":"admin","password":"wrong"}`, http.StatusUnauthorized, "192.0.2.2:5000"},
		"unknown field":  {"application/json", `{"username":"admin","password":"secret","extra":true}`, http.StatusBadRequest, "192.0.2.3:5000"},
		"second object":  {"application/json", `{"username":"admin","password":"secret"}{}`, http.StatusBadRequest, "192.0.2.4:5000"},
		"wrong media":    {"text/plain", `{"username":"admin","password":"secret"}`, http.StatusBadRequest, "192.0.2.5:5000"},
	} {
		t.Run(name, func(t *testing.T) {
			response := serve(h, http.MethodPost, "/manager/api/session", testCase.body, testCase.contentType, "", testCase.remote, false)
			if response.Code != testCase.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
			}
		})
	}

	login := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json; charset=utf-8", "", "192.0.2.200:5000", false)
	if login.Code != http.StatusNoContent || login.Body.Len() != 0 {
		t.Fatalf("login status=%d body=%q", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	decoded, err := hex.DecodeString(cookie.Value)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("cookie token=%q err=%v", cookie.Value, err)
	}
	if cookie.Name != SessionCookieName || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/manager" || cookie.Secure {
		t.Fatalf("cookie=%+v", cookie)
	}

	authorized := serve(h, http.MethodGet, "/manager/api/snapshot", "", "", cookie.Value, "192.0.2.2:5000", false)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status=%d body=%s", authorized.Code, authorized.Body.String())
	}
	logout := serve(h, http.MethodDelete, "/manager/api/session", "", "", cookie.Value, "192.0.2.2:5000", false)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d", logout.Code)
	}
	expired := logout.Result().Cookies()[0]
	if expired.Name != SessionCookieName || expired.MaxAge >= 0 || expired.Path != "/manager" {
		t.Fatalf("expired cookie=%+v", expired)
	}
	if response := serve(h, http.MethodGet, "/manager/api/snapshot", "", "", cookie.Value, "192.0.2.2:5000", false); response.Code != http.StatusUnauthorized {
		t.Fatalf("post-logout status=%d", response.Code)
	}
}

func TestSessionRejectsDuplicateCredentialFields(t *testing.T) {
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}})
	for index, body := range []string{
		`{"username":"admin","username":"admin","password":"secret"}`,
		`{"username":"admin","password":"secret","password":"secret"}`,
	} {
		response := serve(h, http.MethodPost, "/manager/api/session", body, "application/json", "", "198.51.100."+strconv.Itoa(index+1)+":1", false)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("duplicate body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
}

func TestMalformedLoginFailuresAreRateLimited(t *testing.T) {
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}})
	for attempt := 1; attempt <= 5; attempt++ {
		response := serve(h, http.MethodPost, "/manager/api/session", `{"username":`, "application/json", "", "203.0.113.50:1000", false)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("attempt=%d status=%d body=%s", attempt, response.Code, response.Body.String())
		}
	}
	limited := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", "203.0.113.50:2000", false)
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("limited status=%d body=%s", limited.Code, limited.Body.String())
	}
}

func TestExpiredSourceBlockIsClearedBeforeGlobalCleanupIsDue(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	h := testHandler(Dependencies{
		Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Now:   func() time.Time { return now },
	})
	blockedSource := "203.0.113.51:1000"
	for attempt := 1; attempt <= 4; attempt++ {
		response := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"wrong"}`, "application/json", "", blockedSource, false)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt=%d status=%d", attempt, response.Code)
		}
	}
	now = now.Add(50 * time.Second)
	if response := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"wrong"}`, "application/json", "", blockedSource, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("fifth failure status=%d", response.Code)
	}

	now = now.Add(11 * time.Second)
	if response := serve(h, http.MethodPost, "/manager/api/session", `{"username":"other","password":"wrong"}`, "application/json", "", "203.0.113.52:1000", false); response.Code != http.StatusUnauthorized {
		t.Fatalf("cleanup trigger status=%d", response.Code)
	}

	now = now.Add(50 * time.Second)
	response := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", blockedSource, false)
	if response.Code != http.StatusNoContent {
		t.Fatalf("expired source block status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLoginRequiresExactCredentialKeysAndCountsFailures(t *testing.T) {
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}})
	for _, body := range []string{
		`{"Username":"admin","password":"secret"}`,
		`{"username":"admin","Password":"secret"}`,
		`{"password":"secret"}`,
		`{"username":"admin"}`,
		`{"username":"admin","password":"secret","extra":true}`,
	} {
		response := serve(h, http.MethodPost, "/manager/api/session", body, "application/json", "", "203.0.113.60:1000", false)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
	limited := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", "203.0.113.60:2000", false)
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("limited status=%d body=%s", limited.Code, limited.Body.String())
	}
}

func TestLoginRejectsNonStringCredentialValuesAndCountsFailures(t *testing.T) {
	values := []string{"null", "123", "true", `{}`, `[]`}
	for index, field := range []string{"username", "password"} {
		h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}})
		remote := "203.0.113." + strconv.Itoa(70+index) + ":1000"
		for _, value := range values {
			body := `{"username":"admin","password":"secret"}`
			if field == "username" {
				body = `{"username":` + value + `,"password":"secret"}`
			} else {
				body = `{"username":"admin","password":` + value + `}`
			}
			response := serve(h, http.MethodPost, "/manager/api/session", body, "application/json", "", remote, false)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("field=%s value=%s status=%d response=%s", field, value, response.Code, response.Body.String())
			}
		}
		limited := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", remote, false)
		if limited.Code != http.StatusTooManyRequests {
			t.Fatalf("field=%s limited status=%d body=%s", field, limited.Code, limited.Body.String())
		}
	}
}

func TestExpiredSessionIsRemovedWithoutAnotherRequest(t *testing.T) {
	raw, ok := NewHandler(Dependencies{
		Admin:  config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: 40 * time.Millisecond},
		Cache:  monitorcache.NewCache(nil),
		Store:  &taskStoreStub{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).(*handler)
	if !ok {
		t.Fatal("NewHandler must return the monitor handler")
	}
	if cookie := loginResponse(raw, "admin", "secret", "192.0.2.80:1"); cookie == "" {
		t.Fatal("login failed")
	}
	raw.sessionMu.Lock()
	initial := len(raw.sessions)
	raw.sessionMu.Unlock()
	if initial != 1 {
		t.Fatalf("initial sessions=%d", initial)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		raw.sessionMu.Lock()
		remaining := len(raw.sessions)
		raw.sessionMu.Unlock()
		if remaining == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expired session was not removed in the background")
}

func TestSessionRejectsForgedAndExpiredCookiesAndSetsSecureOverTLS(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Minute}, Now: func() time.Time { return now }})
	login := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", "192.0.2.1:1", true)
	cookie := login.Result().Cookies()[0]
	if !cookie.Secure {
		t.Fatalf("secure=%v", cookie.Secure)
	}
	if forged := serve(h, http.MethodGet, "/manager/api/snapshot", "", "", strings.Repeat("0", 64), "192.0.2.1:1", true); forged.Code != http.StatusUnauthorized {
		t.Fatalf("forged status=%d", forged.Code)
	}
	now = now.Add(time.Minute)
	if expired := serve(h, http.MethodGet, "/manager/api/snapshot", "", "", cookie.Value, "192.0.2.1:1", true); expired.Code != http.StatusUnauthorized {
		t.Fatalf("expired status=%d", expired.Code)
	}
}

func TestSessionSetsSecureCookieWhenConfiguredBehindTLSProxy(t *testing.T) {
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Minute, SecureCookie: true}})
	login := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", "192.0.2.1:1", false)
	cookie := login.Result().Cookies()[0]
	if !cookie.Secure {
		t.Fatal("configured secure cookie must be set without direct TLS")
	}
}

func TestSnapshotMapsOnlyWhitelistedFieldsAndSummarizesNodes(t *testing.T) {
	queue, cpu := 2, 12.5
	updated := time.Unix(2_000_000_100, 0)
	cache := monitorcache.NewCache([]monitorcache.NodeSnapshot{
		{ID: "gpu-1", Address: "private.local:7860", Health: monitorcache.HealthHealthy, Runtime: monitorcache.RuntimeRunning, PrivateQueue: &queue, CPUPercent: &cpu, CheckedAt: updated.Add(-time.Second), LastHealthyAt: updated.Add(-2 * time.Second), UpdatedAt: updated, CurrentTask: &monitorcache.CurrentTaskSnapshot{ID: "task-running", Status: "running", StartedAt: updated.Add(-time.Minute)}, LatestFinishedTask: &monitorcache.FinishedTaskSnapshot{ID: "task-done", APIKeyID: "customer", Status: "succeeded", DurationSeconds: 9, FinishedAt: updated.Add(-time.Hour)}, LastError: &monitorcache.ErrorSnapshot{Code: "upstream_poll_error", Summary: "raw secret https://private"}},
		{ID: "gpu-2", Health: monitorcache.HealthUnhealthy, Runtime: monitorcache.RuntimeIdle, UpdatedAt: updated.Add(-time.Minute)},
		{ID: "gpu-3"},
		{ID: "gpu-4", Disabled: true, Applying: true, Health: monitorcache.HealthHealthy, Runtime: monitorcache.RuntimeRunning},
	})
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "a", Password: "b", SessionTTL: time.Hour, MonitorInterval: 2 * time.Second}, Cache: cache})
	cookie := login(t, h, "a", "b", "198.51.100.1:1")
	response := serve(h, http.MethodGet, "/manager/api/snapshot", "", "", cookie, "198.51.100.1:1", false)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var body struct {
		UpdatedAt        int64                                              `json:"updated_at"`
		StaleAfterSecond int64                                              `json:"stale_after_seconds"`
		Summary          struct{ Healthy, Unhealthy, Unknown, Running int } `json:"summary"`
		Upstreams        []struct {
			ID, Address, Health, Runtime string
			Enabled, Applying            bool
			PrivateQueue                 *int                            `json:"private_queue"`
			CheckedAt                    int64                           `json:"checked_at"`
			LastError                    *struct{ Code, Summary string } `json:"last_error"`
		} `json:"upstreams"`
	}
	decodeResponse(t, response, &body)
	if body.UpdatedAt != updated.Unix() || body.StaleAfterSecond != 6 || body.Summary.Healthy != 1 || body.Summary.Unhealthy != 1 || body.Summary.Unknown != 1 || body.Summary.Running != 1 || len(body.Upstreams) != 4 {
		t.Fatalf("snapshot=%+v", body)
	}
	if body.Upstreams[0].LastError == nil || body.Upstreams[0].LastError.Summary != "私有服务状态查询失败" || body.Upstreams[2].CheckedAt != 0 || body.Upstreams[3].Enabled || !body.Upstreams[3].Applying {
		t.Fatalf("upstreams=%+v", body.Upstreams)
	}
	output := response.Body.String()
	for _, sensitive := range []string{"raw secret", "https://private", "password", "result_internal_url", "request_json"} {
		if strings.Contains(output, sensitive) {
			t.Fatalf("response leaked %q: %s", sensitive, output)
		}
	}
}

func TestTasksValidatesFiltersAndReturnsMinimalShape(t *testing.T) {
	created := time.Unix(2_000_000_000, 0)
	started := created.Add(10 * time.Minute)
	store := &taskStoreStub{total: 1, items: []domain.AdminTaskSummary{{TaskID: "task-1", APIKeyID: "customer", Status: domain.V2Running, InternalStatus: domain.StatusReconciling, RetryCount: 1, UpstreamID: "gpu-1", Scenario: "t2va", Resolution: "768P", Duration: 99, CreatedAt: created, StartedAt: started}}}
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "a", Password: "b", SessionTTL: time.Hour}, Store: store, Now: func() time.Time { return started.Add(65 * time.Second) }})
	cookie := login(t, h, "a", "b", "203.0.113.1:1")
	response := serve(h, http.MethodGet, "/manager/api/tasks?page_num=2&page_size=20&status=running&upstream_id=gpu-1&search=task", "", "", cookie, "203.0.113.1:1", false)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	store.mu.Lock()
	filter := store.filter
	store.mu.Unlock()
	if filter != (domain.AdminTaskFilter{PageNum: 2, PageSize: 20, Status: domain.V2Running, UpstreamID: "gpu-1", Search: "task"}) {
		t.Fatalf("filter=%+v", filter)
	}
	var raw map[string]any
	decodeResponse(t, response, &raw)
	items := raw["items"].([]any)
	item := items[0].(map[string]any)
	if len(item) != 13 || item["id"] != "task-1" || item["created_at"] != float64(created.Unix()) || item["duration_seconds"] != float64(65) || item["phase"] != "retrying" || item["retry_count"] != float64(1) || item["can_cancel"] != true || item["can_delete"] != false || item["video_url"] != nil || raw["page_num"] != float64(2) || raw["page_size"] != float64(20) {
		t.Fatalf("body=%v", raw)
	}
	for _, forbidden := range []string{"duration", "started_at", "finished_at", "request_json", "result_internal_url"} {
		if _, ok := item[forbidden]; ok {
			t.Fatalf("unexpected field %q in %v", forbidden, item)
		}
	}

	for _, path := range []string{
		"/manager/api/tasks?page_num=0",
		"/manager/api/tasks?page_size=15",
		"/manager/api/tasks?status=unknown",
		"/manager/api/tasks?extra=1",
		"/manager/api/tasks?page_num=1&page_num=2",
	} {
		if invalid := serve(h, http.MethodGet, path, "", "", cookie, "203.0.113.1:1", false); invalid.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d body=%s", path, invalid.Code, invalid.Body.String())
		}
	}
	defaults := serve(h, http.MethodGet, "/manager/api/tasks", "", "", cookie, "203.0.113.1:1", false)
	if defaults.Code != http.StatusOK {
		t.Fatalf("defaults status=%d", defaults.Code)
	}
	store.mu.Lock()
	filter = store.filter
	store.mu.Unlock()
	if filter.PageNum != 1 || filter.PageSize != 10 {
		t.Fatalf("defaults=%+v", filter)
	}
}

func TestTaskActionsRequireSessionAndMapStoreResults(t *testing.T) {
	wakeCount := 0
	store := &taskStoreStub{}
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "a", Password: "b", SessionTTL: time.Hour}, Store: store, Wake: func() { wakeCount++ }})
	cookie := login(t, h, "a", "b", "203.0.113.2:1")

	unauthorized := serve(h, http.MethodPost, "/manager/api/tasks/task-1/cancel", "", "", "", "203.0.113.2:1", false)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	cancelled := serve(h, http.MethodPost, "/manager/api/tasks/task-1/cancel", "", "", cookie, "203.0.113.2:1", false)
	if cancelled.Code != http.StatusAccepted || wakeCount != 1 || store.cancelledTask != "task-1" {
		t.Fatalf("cancel status=%d wake=%d task=%q body=%s", cancelled.Code, wakeCount, store.cancelledTask, cancelled.Body.String())
	}
	deleted := serve(h, http.MethodDelete, "/manager/api/tasks/task-1", "", "", cookie, "203.0.113.2:1", false)
	if deleted.Code != http.StatusNoContent || store.deletedTask != "task-1" || deleted.Body.Len() != 0 {
		t.Fatalf("delete status=%d task=%q body=%s", deleted.Code, store.deletedTask, deleted.Body.String())
	}

	store.cancelError = domain.ErrTaskNotOperable
	if response := serve(h, http.MethodPost, "/manager/api/tasks/task-2/cancel", "", "", cookie, "203.0.113.2:1", false); response.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", response.Code, response.Body.String())
	}
	store.deleteError = domain.ErrTaskNotFound
	if response := serve(h, http.MethodDelete, "/manager/api/tasks/task-2", "", "", cookie, "203.0.113.2:1", false); response.Code != http.StatusNotFound {
		t.Fatalf("not found status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTaskDurationSecondsHandlesQueuedAndFinishedTasks(t *testing.T) {
	started := time.Unix(2_000_000_000, 0)
	if got := taskDurationSeconds(domain.AdminTaskSummary{Status: domain.V2Queued}, started); got != nil {
		t.Fatalf("queued duration = %v", *got)
	}
	got := taskDurationSeconds(domain.AdminTaskSummary{Status: domain.V2Succeeded, StartedAt: started, FinishedAt: started.Add(125 * time.Second)}, started.Add(time.Hour))
	if got == nil || *got != 125 {
		t.Fatalf("finished duration = %v", got)
	}
}

func TestProtectedRoutesMethodsRateLimitAndConcurrentSessions(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Now: func() time.Time { return now }})
	for _, path := range []string{"/manager/api/snapshot", "/manager/api/tasks"} {
		response := serve(h, http.MethodGet, path, "", "", "", "192.0.2.1:1", false)
		if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d headers=%v", path, response.Code, response.Header())
		}
	}
	if response := serve(h, http.MethodPut, "/manager/api/session", "", "", "", "192.0.2.1:1", false); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status=%d", response.Code)
	}
	for i := 0; i < 5; i++ {
		response := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"wrong"}`, "application/json", "", "192.0.2.20:1000", false)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d status=%d", i+1, response.Code)
		}
	}
	if limited := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", "192.0.2.20:2000", false); limited.Code != http.StatusTooManyRequests {
		t.Fatalf("limited status=%d body=%s", limited.Code, limited.Body.String())
	}
	now = now.Add(time.Minute)
	if recovered := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", "192.0.2.20:3000", false); recovered.Code != http.StatusNoContent {
		t.Fatalf("recovered status=%d", recovered.Code)
	}

	var wg sync.WaitGroup
	errors := make(chan string, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			remote := "198.51.100." + string(rune('A'+i)) + ":1"
			cookie := loginResponse(h, "admin", "secret", remote)
			if cookie == "" {
				errors <- "login failed"
				return
			}
			if got := serve(h, http.MethodGet, "/manager/api/snapshot", "", "", cookie, remote, false); got.Code != http.StatusOK {
				errors <- "authorization failed"
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestConcurrentLoginFailuresReserveRateLimitCapacity(t *testing.T) {
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}})
	start := make(chan struct{})
	statuses := make(chan int, 20)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			response := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"wrong"}`, "application/json", "", "198.51.100.200:1000", false)
			statuses <- response.Code
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	notLimited := 0
	for status := range statuses {
		if status != http.StatusTooManyRequests {
			notLimited++
		}
	}
	if notLimited > loginFailureLimit {
		t.Fatalf("non-429 responses=%d, want at most %d", notLimited, loginFailureLimit)
	}
	if response := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"wrong"}`, "application/json", "", "198.51.100.200:2000", false); response.Code != http.StatusTooManyRequests {
		t.Fatalf("subsequent status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLoginFailureSourcesHaveHardCapacity(t *testing.T) {
	raw, ok := NewHandler(Dependencies{
		Admin:  config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Cache:  monitorcache.NewCache(nil),
		Store:  &taskStoreStub{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).(*handler)
	if !ok {
		t.Fatal("NewHandler must return the monitor handler")
	}
	const capacity = 4096
	for i := 0; i < capacity; i++ {
		remote := "198.18." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256) + ":1"
		response := serve(raw, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"wrong"}`, "application/json", "", remote, false)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("source=%d status=%d body=%s", i, response.Code, response.Body.String())
		}
	}
	overflow := serve(raw, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"wrong"}`, "application/json", "", "203.0.113.250:1", false)
	if overflow.Code != http.StatusTooManyRequests {
		t.Fatalf("overflow status=%d body=%s", overflow.Code, overflow.Body.String())
	}
	raw.failureMu.Lock()
	sources := len(raw.failures)
	raw.failureMu.Unlock()
	if sources > capacity {
		t.Fatalf("failure sources=%d, want at most %d", sources, capacity)
	}
}

func testHandler(dependencies Dependencies) http.Handler {
	if dependencies.Cache == nil {
		dependencies.Cache = monitorcache.NewCache(nil)
	}
	if dependencies.Store == nil {
		dependencies.Store = &taskStoreStub{}
	}
	dependencies.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewHandler(dependencies)
}

func login(t *testing.T, handler http.Handler, username, password, remote string) string {
	t.Helper()
	cookie := loginResponse(handler, username, password, remote)
	if cookie == "" {
		t.Fatal("login failed")
	}
	return cookie
}

func loginResponse(handler http.Handler, username, password, remote string) string {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	response := serve(handler, http.MethodPost, "/manager/api/session", string(body), "application/json", "", remote, false)
	if response.Code != http.StatusNoContent || len(response.Result().Cookies()) != 1 {
		return ""
	}
	return response.Result().Cookies()[0].Value
}

func serve(handler http.Handler, method, path, body, contentType, cookie, remote string, useTLS bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = remote
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: cookie})
	}
	if useTLS {
		request.TLS = &tls.ConnectionState{}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode %q: %v", response.Body.String(), err)
	}
}
