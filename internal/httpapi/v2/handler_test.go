package v2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
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

type createSpyStore struct{ createCalls int }

func (s *createSpyStore) Create(_ context.Context, _ domain.NewTask, _ string, available func() bool) (domain.Task, error) {
	if available != nil && !available() {
		return domain.Task{}, domain.ErrResourceUnavailable
	}
	s.createCalls++
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
