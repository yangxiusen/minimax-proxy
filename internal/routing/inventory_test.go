package routing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

type inventoryMemory struct {
	mu      sync.Mutex
	node    domain.ModelNode
	catalog domain.ModelCatalog
	writes  int
	err     error
}

func (s *inventoryMemory) GetModelNode(context.Context, string) (domain.ModelNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.node, nil
}
func (s *inventoryMemory) ListModelNodes(context.Context) ([]domain.ModelNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []domain.ModelNode{s.node}, nil
}
func (s *inventoryMemory) GetModelCatalog(context.Context, string) (domain.ModelCatalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalog, nil
}
func (s *inventoryMemory) RefreshModelCatalog(_ context.Context, _ string, nv, cv int64, items []domain.NodeModel, code string) (domain.ModelCatalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return domain.ModelCatalog{}, s.err
	}
	if nv != s.node.Version {
		return domain.ModelCatalog{}, domain.ErrNodeVersionConflict
	}
	if cv != s.catalog.Revision {
		return domain.ModelCatalog{}, domain.ErrCatalogConflict
	}
	s.writes++
	if code != "" {
		s.catalog.LastErrorCode = code
		s.catalog.Status = "error"
	} else {
		s.catalog.Items = items
	}
	return s.catalog, nil
}

type inventorySecrets struct{}

func (inventorySecrets) Open([]byte, []byte) (string, error) { return "test-token", nil }
func inventoryInput(server string) domain.ModelNodeInput {
	return domain.ModelNodeInput{ID: "node-1", ServiceURL: server, ProtocolVersion: domain.ProtocolTK2SD, RequestTimeout: time.Second, PollInterval: time.Second, MaxConcurrency: 1, Enabled: true, APIKeyNonce: []byte("n"), APIKeyCiphertext: []byte("c")}
}

const discoveryJSON = `{"data":[{"id":"model-a","mode":"image_to_video","durations":[5]},{"id":"model-a","mode":"text_to_video","durations":[4,5]},{"id":"model-b","mode":"reference_to_video","durations":[6]}]}`

func TestInventoryPreviewConvertsModesWithoutPersistence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("incorrect discovery request")
		}
		_, _ = io.WriteString(w, discoveryJSON)
	}))
	defer server.Close()
	now := time.Unix(1000, 0)
	store := &inventoryMemory{}
	inv := &Inventory{Store: store, Now: func() time.Time { return now }, HTTPClient: server.Client()}
	got, err := inv.Preview(context.Background(), inventoryInput(server.URL), "test-token")
	if err != nil || len(got.Items) != 2 || got.Source != "discovered" || got.ValidUntil != 2800 || store.writes != 0 {
		t.Fatalf("preview: %+v %v", got, err)
	}
	if !got.Items[0].Capabilities.Supports("t2va", 4) || got.Items[0].Capabilities.Supports("i2va", 4) || !got.Items[1].Capabilities.Supports("r2va", 6) || !got.Items[0].Verified {
		t.Fatalf("capability conversion: %+v", got.Items)
	}
}

func TestInventoryFailureRetainsLastSuccessfulCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":{"message":"test-token secret-url"}}`)
	}))
	defer server.Close()
	store := &inventoryMemory{node: domain.ModelNode{ModelNodeInput: inventoryInput(server.URL), Version: 4}, catalog: domain.ModelCatalog{Source: "discovered", Revision: 2, LastSuccessAt: 100, ValidUntil: 1900, Items: []domain.NodeModel{{ModelID: "cached"}}}}
	inv := &Inventory{Store: store, Secrets: inventorySecrets{}}
	got, err := inv.Refresh(context.Background(), "node-1", 4, 2)
	if !errors.Is(err, ErrDiscoveryFailed) || got.ValidUntil != 1900 || got.LastSuccessAt != 100 || got.Items[0].ModelID != "cached" || store.writes != 1 || got.LastErrorCode != "model_discovery_failed" {
		t.Fatalf("refresh: %+v %v", got, err)
	}
}

func TestInventoryRejectsStaleVersionBeforeHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("stale request reached upstream") }))
	defer server.Close()
	store := &inventoryMemory{node: domain.ModelNode{ModelNodeInput: inventoryInput(server.URL), Version: 4}, catalog: domain.ModelCatalog{Source: "discovered", Revision: 2}}
	inv := &Inventory{Store: store, Secrets: inventorySecrets{}}
	if _, err := inv.Refresh(context.Background(), "node-1", 3, 2); !errors.Is(err, domain.ErrNodeVersionConflict) {
		t.Fatal(err)
	}
	if _, err := inv.Refresh(context.Background(), "node-1", 4, 1); !errors.Is(err, domain.ErrCatalogConflict) {
		t.Fatal(err)
	}
}

func TestInventoryRefreshDiscardsConnectionChangedDuringHTTP(t *testing.T) {
	store := &inventoryMemory{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		store.node.Version++
		store.mu.Unlock()
		_, _ = io.WriteString(w, discoveryJSON)
	}))
	defer server.Close()
	store.node = domain.ModelNode{ModelNodeInput: inventoryInput(server.URL), Version: 4}
	store.catalog = domain.ModelCatalog{Source: "discovered", Revision: 2}
	inv := &Inventory{Store: store, Secrets: inventorySecrets{}}
	if _, err := inv.Refresh(context.Background(), "node-1", 4, 2); !errors.Is(err, domain.ErrNodeVersionConflict) || store.writes != 0 {
		t.Fatalf("stale result saved: %v", err)
	}
}

func TestInventoryRunRefreshesDueNodesAndStops(t *testing.T) {
	var calls atomic.Int64
	refreshed := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, discoveryJSON) }))
	defer server.Close()
	store := &inventoryMemory{node: domain.ModelNode{ModelNodeInput: inventoryInput(server.URL), Version: 4}, catalog: domain.ModelCatalog{Source: "discovered", Revision: 2}}
	inv := &Inventory{Store: store, Secrets: inventorySecrets{}, Wake: func() { refreshed <- struct{}{} }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { inv.Run(ctx); close(done) }()
	select {
	case <-refreshed:
	case <-time.After(time.Second):
		t.Fatal("initial refresh missing")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("run did not stop")
	}
	if calls.Load() != 1 || CatalogRefreshInterval != 5*time.Minute || CatalogValidity != 30*time.Minute {
		t.Fatal("incorrect refresh policy")
	}
}

func TestInventoryRejectsInvalidDiscoveredIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"bad model","mode":"text_to_video","durations":[5]}]}`)
	}))
	defer server.Close()
	if _, err := (&Inventory{}).Preview(context.Background(), inventoryInput(server.URL), "test-token"); !errors.Is(err, ErrDiscoveryFailed) {
		t.Fatal(err)
	}
}

func TestInventoryCoalescesConcurrentRefreshAndWaiterCanCancel(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		_, _ = io.WriteString(w, discoveryJSON)
	}))
	defer server.Close()
	store := &inventoryMemory{node: domain.ModelNode{ModelNodeInput: inventoryInput(server.URL), Version: 4}, catalog: domain.ModelCatalog{Source: "discovered", Revision: 2}}
	inv := &Inventory{Store: store, Secrets: inventorySecrets{}}
	done := make(chan error, 1)
	go func() { _, err := inv.Refresh(context.Background(), "node-1", 4, 2); done <- err }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := inv.Refresh(ctx, "node-1", 4, 2)
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || store.writes != 1 {
		t.Fatal("concurrent refresh was not coalesced")
	}
}

func TestInventoryPeriodicRefreshSkipsRecentAndDisabledNodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("not-due node contacted upstream") }))
	defer server.Close()
	now := time.Unix(1000, 0)
	store := &inventoryMemory{node: domain.ModelNode{ModelNodeInput: inventoryInput(server.URL), Version: 4}, catalog: domain.ModelCatalog{Source: "discovered", Revision: 2, LastAttemptAt: 999}}
	inv := &Inventory{Store: store, Secrets: inventorySecrets{}, Now: func() time.Time { return now }}
	inv.refreshDue(context.Background())
	store.catalog.LastAttemptAt = 0
	store.node.Enabled = false
	inv.refreshDue(context.Background())
	if store.writes != 0 {
		t.Fatal("unexpected publication")
	}
}

func TestInventoryPreviewHonorsNodeTimeoutWithoutMutatingClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, discoveryJSON) }))
	defer server.Close()
	client := server.Client()
	client.Timeout = time.Nanosecond
	inv := &Inventory{HTTPClient: client}
	if _, err := inv.Preview(context.Background(), inventoryInput(server.URL), "test-token"); err != nil {
		t.Fatalf("node timeout not applied: %v", err)
	}
	if client.Timeout != time.Nanosecond {
		t.Fatal("injected client mutated")
	}
}
