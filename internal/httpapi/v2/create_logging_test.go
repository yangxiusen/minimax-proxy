package v2

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type missingCreateIDStore struct{ createSpyStore }

func (s *missingCreateIDStore) Create(context.Context, domain.NewTask, string, func() bool) (domain.Task, error) {
	return domain.Task{}, nil
}
func TestCreateNeverReturnsSuccessWithoutTaskID(t *testing.T) {
	h := NewHandler(Dependencies{Store: &missingCreateIDStore{}, Profiles: profiles(), APIKeys: []config.APIKeyConfig{{ID: "owner", Key: "key-a", Enabled: true}}})
	w := request(t, h, http.MethodPost, "/v2/video_generation", validCreateJSON(), "key-a")
	if w.Code != 500 {
		t.Fatalf("false success: %d %s", w.Code, w.Body.String())
	}
}
func TestCreateLogsRequestAndCompatibleResponse(t *testing.T) {
	var output bytes.Buffer
	h := NewHandler(Dependencies{Store: apiStore(t, 0), Profiles: profiles(), Logger: slog.New(slog.NewJSONHandler(&output, nil)), APIKeys: []config.APIKeyConfig{{ID: "owner", Key: "key-a", Enabled: true}}})
	r := httptest.NewRequest(http.MethodPost, "/v2/video_generation", bytes.NewReader(validCreateJSON()))
	r.Header.Set("Authorization", "Bearer key-a")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Request-Id", "diagnostic-request")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var accepted map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &accepted)
	log := output.String()
	if !strings.Contains(log, `"event":"request"`) || !strings.Contains(log, `"event":"response"`) || !strings.Contains(log, `"duration_provided":true`) || !strings.Contains(log, accepted["task_id"]) {
		t.Fatalf("missing create diagnostics: %s", log)
	}
	if strings.Contains(log, "key-a") || strings.Contains(log, "海边日落") {
		t.Fatalf("secret request logged: %s", log)
	}
}
