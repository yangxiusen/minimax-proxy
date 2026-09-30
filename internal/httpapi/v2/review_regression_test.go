package v2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/routing"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLegacyNormalizerSurvivesProtocolBinding(t *testing.T) {
	store := apiStore(t, 0)
	ctx := context.Background()
	req := domain.GenerationRequest{Model: "MiniMax-H3", Content: []domain.GenerationContent{{Type: "text", Text: "test"}}, Resolution: "2K", Duration: 5, Ratio: "16:9"}
	body, _ := json.Marshal(req)
	hash, _ := requestDigest(req, "legacy-h3-v1")
	key := sha256.Sum256([]byte("legacy-key"))
	snapshot, _ := json.Marshal(domain.RouteSnapshot{SchemaVersion: 1, Model: req.Model, ProtocolVersion: domain.ProtocolTK2SD, Requirements: domain.TaskRequirements{Scenario: "t2va", Duration: 5}})
	_, err := store.Create(ctx, domain.NewTask{TaskID: "legacy-bound", APIKeyID: "owner-a", Model: req.Model, Scenario: "t2va", RequestJSON: string(body), RequestHash: hash, Resolution: req.Resolution, Duration: 5, Ratio: req.Ratio, ProtocolVersion: domain.ProtocolTK2SD, RoutingSnapshotJSON: string(snapshot), RequestNormalizer: "legacy-h3-v1"}, hex.EncodeToString(key[:]), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Dependencies{Store: store, Routing: &routing.Service{Store: store}, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}}})
	r := httptest.NewRequest(http.MethodPost, "/v2/video_generation", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer key-a")
	r.Header.Set("Idempotency-Key", "legacy-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("legacy replay=%d %s", w.Code, w.Body.String())
	}
}

type cancelAfterListStore struct {
	fixedTaskStore
	cancel context.CancelFunc
}

func (s *cancelAfterListStore) List(ctx context.Context, owner string, filter domain.TaskFilter) ([]domain.Task, int, error) {
	items, count, err := s.fixedTaskStore.List(ctx, owner, filter)
	s.cancel()
	return items, count, err
}
func TestExpiredListRefreshKeepsH3Metadata(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &cancelAfterListStore{cancel: cancel}
	for i := 0; i < 64; i++ {
		store.items = append(store.items, domain.Task{TaskID: fmt.Sprint(i), Model: "MiniMax-H3", ProtocolVersion: domain.ProtocolH3, Status: domain.StatusSucceeded, Duration: 5, Resolution: "2K", RatioRequested: "16:9", ResultPublicURL: "https://result.example/video.mp4", CreatedAt: time.Now()})
	}
	h := NewHandler(Dependencies{Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner", Key: "key-a", Enabled: true}}})
	r := httptest.NewRequest(http.MethodGet, "/v2/query/video_generation?page_size=100", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer key-a")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var response struct {
		Items []TaskResponse `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 64 {
		t.Fatalf("response=%s", w.Body.String())
	}
	for _, item := range response.Items {
		if item.Duration == nil || *item.Duration != 5 || item.Content == nil || item.DeliveryError != nil {
			t.Fatalf("H3 damaged by refresh timeout: %+v", item)
		}
	}
}

func TestExpiredListRefreshKeepsRemoteFailuresAndQueuedStates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &cancelAfterListStore{cancel: cancel}
	for i := 0; i < 64; i++ {
		status := domain.StatusFailed
		if i%2 == 1 {
			status = domain.StatusQueuedOpen
		}
		store.items = append(store.items, domain.Task{TaskID: fmt.Sprint(i), Model: "video-1.5-pro", ProtocolVersion: domain.ProtocolTK2SD, Status: status, ErrorCode: "provider_denied", ErrorMessage: "generation rejected"})
	}
	h := NewHandler(Dependencies{Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner", Key: "key-a", Enabled: true}}})
	r := httptest.NewRequest(http.MethodGet, "/v2/query/video_generation?page_size=100", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer key-a")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var response struct {
		Items []TaskResponse `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 64 {
		t.Fatalf("response=%s", w.Body.String())
	}
	for _, item := range response.Items {
		if item.DeliveryError != nil {
			t.Fatalf("spurious refresh error: %+v", item)
		}
		if item.Status == domain.V2Failed && (item.Error == nil || item.Error.Code != "provider_denied") {
			t.Fatalf("generation error lost: %+v", item)
		}
	}
}
