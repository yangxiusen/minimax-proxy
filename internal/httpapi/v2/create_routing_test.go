package v2

import (
	"bytes"
	"context"
	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/routing"
	"minimax-h3-tc/internal/store/sqlite"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateDirectWithoutH3ProfileAndReplayOffline(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routed.db"), sqlite.Options{PerKeyLimit: 10, GlobalLimit: 100, Retention: time.Hour, IdempotencyTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cap := domain.ModelCapability{SchemaVersion: 1, Modes: []domain.ModeCapability{{Scenario: "t2va", Durations: []int{5}}}, ResolutionPolicy: "ignored"}
	_, err = store.CreateModelNode(ctx, domain.ModelNodeInput{ID: "tk", ServiceURL: "http://tk.example", ProtocolVersion: domain.ProtocolTK2SD, APIKeyCiphertext: []byte{1}, APIKeyNonce: []byte{2}, APIKeyFingerprint: "test", Enabled: true, MaxConcurrency: 1, PollInterval: time.Second, RequestTimeout: time.Second, ModelCatalog: &domain.ModelCatalog{Source: "discovered", Status: "ready", LastSuccessAt: time.Now().Unix(), ValidUntil: time.Now().Add(time.Minute).Unix(), Items: []domain.NodeModel{{ModelID: "video-1.5-pro", Enabled: true, Present: true, Verified: true, Capabilities: cap}}}})
	if err != nil {
		t.Fatal(err)
	}
	healthy := true
	router := &routing.Service{Store: store, Healthy: func(string) bool { return healthy }}
	h := NewHandler(Dependencies{Store: store, Routing: router, ActiveProfiles: store, APIKeys: []config.APIKeyConfig{{ID: "owner", Key: "key-a", Enabled: true}}})
	body := []byte(`{"model":"video-1.5-pro","content":[{"type":"text","text":"test"}]}`)
	createWithKey := func(payload []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v2/video_generation", bytes.NewReader(payload))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer key-a")
		r.Header.Set("Idempotency-Key", "routed-once")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	first := createWithKey(body)
	if first.Code != http.StatusOK {
		t.Fatalf("create=%d %s", first.Code, first.Body.String())
	}
	list := request(t, h, http.MethodGet, "/v2/query/video_generation?filter.model=video-1.5-pro", nil, "key-a")
	if list.Code != http.StatusOK || !containsJSONModel(list.Body.Bytes(), "video-1.5-pro") {
		t.Fatalf("list=%s", list.Body.String())
	}
	healthy = false
	replay := createWithKey(body)
	if replay.Code != 200 || replay.Body.String() != first.Body.String() {
		t.Fatalf("offline replay=%d %s", replay.Code, replay.Body.String())
	}
	conflict := createWithKey(bytes.ReplaceAll(body, []byte(`"test"`), []byte(`"changed"`)))
	if conflict.Code != 409 {
		t.Fatalf("replay conflict=%d %s", conflict.Code, conflict.Body.String())
	}
	offline := request(t, h, http.MethodPost, "/v2/video_generation", body, "key-a")
	if offline.Code != 503 {
		t.Fatalf("offline=%d %s", offline.Code, offline.Body.String())
	}
}
func containsJSONModel(body []byte, model string) bool {
	return strings.Contains(string(body), `"model":"`+model+`"`)
}
