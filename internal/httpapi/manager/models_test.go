package manager

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/routing"
	"minimax-h3-tc/internal/store/sqlite"
)

type catalogNodeStore struct {
	nodeStoreStub
	catalog   domain.ModelCatalog
	refreshes int
}

func (s *catalogNodeStore) GetModelCatalog(context.Context, string) (domain.ModelCatalog, error) {
	if s.created.ModelCatalog != nil {
		c := *s.created.ModelCatalog
		c.Revision = 1
		return c, nil
	}
	return s.catalog, nil
}
func (s *catalogNodeStore) RefreshModelCatalog(_ context.Context, _ string, _, _ int64, items []domain.NodeModel, code string) (domain.ModelCatalog, error) {
	s.refreshes++
	if code != "" {
		s.catalog.LastErrorCode = code
		s.catalog.Status = "error"
	} else {
		s.catalog.Items = items
		s.catalog.Revision++
	}
	return s.catalog, nil
}
func modelHandler(t *testing.T, s *catalogNodeStore, inv *routing.Inventory) (http.Handler, string) {
	t.Helper()
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Nodes: s, NodeSecrets: testNodeSecrets{}, Inventory: inv})
	return h, login(t, h, "admin", "secret", "192.0.2.50:1")
}
func modelRequest(h http.Handler, method, path, body, cookie string) *httptest.ResponseRecorder {
	return serve(h, method, path, body, "application/json", cookie, "192.0.2.50:1", false)
}
func newTKBody(server string) map[string]any {
	return map[string]any{"id": "tk-1", "service_url": server, "protocol_version": domain.ProtocolTK2SD, "api_key": "test-token", "max_concurrency": 2, "replace_result_url": false, "poll_interval": "3s", "request_timeout": "2s", "enabled": true, "enabled_models": []string{"model-a"}}
}
func asJSON(v any) string { data, _ := json.Marshal(v); return string(data) }

func TestModelEndpointsRequireSessionAndStrictParameters(t *testing.T) {
	h, cookie := modelHandler(t, &catalogNodeStore{}, nil)
	for _, path := range []string{"/manager/api/protocols", "/manager/api/nodes/node-1/models", "/manager/api/model-routes"} {
		if r := modelRequest(h, "GET", path, "", ""); r.Code != 401 {
			t.Fatalf("%s: %d", path, r.Code)
		}
	}
	if r := modelRequest(h, "GET", "/manager/api/protocols", "", cookie); r.Code != 200 || !strings.Contains(r.Body.String(), `"tk2sd-v1"`) || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("protocols: %d %s", r.Code, r.Body.String())
	}
	for _, query := range []string{"?unknown=1", "?page_size=101", "?page_num=0", "?page_size=1&page_size=2", "?include_missing=maybe"} {
		r := modelRequest(h, "GET", "/manager/api/nodes/node-1/models"+query, "", cookie)
		if r.Code != 400 {
			t.Fatalf("%s: %d", query, r.Code)
		}
	}
}

func TestCreateDiscoveredNodeUsesTrustedCatalogAndEncryptedFingerprint(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("wrong token")
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"model-a","mode":"text_to_video","durations":[5]},{"id":"model-b","mode":"reference_to_video","durations":[6]}]}`)
	}))
	defer server.Close()
	store := &catalogNodeStore{}
	h, cookie := modelHandler(t, store, &routing.Inventory{HTTPClient: server.Client()})
	result := modelRequest(h, "POST", "/manager/api/nodes", asJSON(newTKBody(server.URL)), cookie)
	if result.Code != 201 || calls != 1 || store.created.ModelCatalog == nil {
		t.Fatalf("create: %d %s", result.Code, result.Body.String())
	}
	if store.created.APIKeyFingerprint != "sha256:test" || !store.created.ModelCatalog.Items[0].Enabled || store.created.ModelCatalog.Items[1].Enabled {
		t.Fatalf("wrong persisted selection: %+v", store.created.ModelCatalog)
	}
	if strings.Contains(result.Body.String(), "test-token") || !strings.Contains(result.Body.String(), `"model_catalog"`) {
		t.Fatal("unsafe or missing DTO")
	}
}

func TestDiscoveryFailureAndUnknownSelectionNeverCreateNode(t *testing.T) {
	for _, status := range []int{401, 200} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"data":[]}`)
		}))
		store := &catalogNodeStore{}
		h, cookie := modelHandler(t, store, &routing.Inventory{HTTPClient: server.Client()})
		result := modelRequest(h, "POST", "/manager/api/nodes", asJSON(newTKBody(server.URL)), cookie)
		if (status == 401 && result.Code != 502) || (status == 200 && result.Code != 400) || store.createCalls != 0 {
			t.Fatalf("unsafe create: %d %s", result.Code, result.Body.String())
		}
		server.Close()
	}
}

func TestPreviewDoesNotPersistAndRejectsClientCapabilities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"data":[]}`) }))
	defer server.Close()
	store := &catalogNodeStore{}
	h, cookie := modelHandler(t, store, &routing.Inventory{HTTPClient: server.Client()})
	body := map[string]any{"protocol_version": domain.ProtocolTK2SD, "service_url": server.URL, "request_timeout": "2s", "api_key": "test-token"}
	response := modelRequest(h, "POST", "/manager/api/nodes/discover-models", asJSON(body), cookie)
	if response.Code != 200 || store.createCalls != 0 || store.refreshes != 0 || !strings.Contains(response.Body.String(), `"items":[]`) {
		t.Fatalf("preview: %d %s", response.Code, response.Body.String())
	}
	body["capabilities"] = map[string]any{}
	if response = modelRequest(h, "POST", "/manager/api/nodes/discover-models", asJSON(body), cookie); response.Code != 400 {
		t.Fatal("client capabilities trusted")
	}
}

func TestNewOfficialNodeUsesManualIDsAndRejectsAliases(t *testing.T) {
	store := &catalogNodeStore{}
	h, cookie := modelHandler(t, store, nil)
	body := newTKBody("https://official.invalid")
	body["protocol_version"] = domain.ProtocolOfficial
	response := modelRequest(h, "POST", "/manager/api/nodes", asJSON(body), cookie)
	if response.Code != 201 || store.created.UpstreamModel != "" || store.created.ModelCatalog == nil || store.created.ModelCatalog.Source != "manual" || store.created.ModelCatalog.Items[0].Verified {
		t.Fatalf("manual create: %d %s", response.Code, response.Body.String())
	}
	body["upstream_model"] = "legacy-alias"
	if response = modelRequest(h, "POST", "/manager/api/nodes", asJSON(body), cookie); response.Code != 400 {
		t.Fatal("new alias accepted")
	}
}

func TestNodeModelListPaginatesCachedSnapshot(t *testing.T) {
	store := &catalogNodeStore{catalog: domain.ModelCatalog{Source: "discovered", Revision: 7, Status: "ready", Items: []domain.NodeModel{{ModelID: "b", Present: true}, {ModelID: "a", Present: true}, {ModelID: "missing"}}}}
	store.items = []domain.ModelNode{{ModelNodeInput: domain.ModelNodeInput{ID: "node-1"}, Version: 1}}
	h, cookie := modelHandler(t, store, nil)
	response := modelRequest(h, "GET", "/manager/api/nodes/node-1/models?page_size=1&page_num=2", "", cookie)
	var got struct {
		Revision int64              `json:"catalog_revision"`
		Total    int                `json:"total"`
		Items    []domain.NodeModel `json:"items"`
	}
	decodeResponse(t, response, &got)
	if response.Code != 200 || got.Revision != 7 || got.Total != 2 || len(got.Items) != 1 || got.Items[0].ModelID != "b" {
		t.Fatalf("list: %s", response.Body.String())
	}
}

func TestOfficialConfigAllowsEmptyDirectModelAndPreservesLegacyAlias(t *testing.T) {
	input := domain.ModelNodeInput{ID: "official", ServiceURL: "https://official.invalid", ProtocolVersion: domain.ProtocolOfficial, MaxConcurrency: 1, PollInterval: time.Second, RequestTimeout: time.Second}
	if got, _, err := config.NormalizeModelNode(input); err != nil || got.UpstreamModel != "" {
		t.Fatalf("direct config: %v", err)
	}
	input.UpstreamModel = "legacy-alias"
	input.LegacyModelCompat = true
	if got, _, err := config.NormalizeModelNode(input); err != nil || got.UpstreamModel != "legacy-alias" {
		t.Fatalf("legacy config: %v", err)
	}
}

func tkNodeFixture(server string) *catalogNodeStore {
	s := &catalogNodeStore{catalog: domain.ModelCatalog{Source: "discovered", Revision: 7, Status: "ready", LastSuccessAt: time.Now().Unix(), ValidUntil: time.Now().Add(30 * time.Minute).Unix(), Items: []domain.NodeModel{{ModelID: "model-a", Enabled: true, Present: true, Verified: true}}}}
	s.items = []domain.ModelNode{{ModelNodeInput: domain.ModelNodeInput{ID: "tk-1", ServiceURL: server, ProtocolVersion: domain.ProtocolTK2SD, APIKeyFingerprint: "old-fingerprint", APIKeyNonce: []byte("nonce"), APIKeyCiphertext: []byte("test-token"), MaxConcurrency: 2, PollInterval: 3 * time.Second, RequestTimeout: 2 * time.Second, Enabled: true}, Version: 4}}
	return s
}
func updateTKBody(server string) map[string]any {
	body := newTKBody(server)
	delete(body, "id")
	delete(body, "api_key")
	body["version"] = 4
	body["catalog_revision"] = 7
	return body
}

func TestCatalogSelectionOnlyUsesCacheAndCAS(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("selection update contacted upstream") }))
	defer server.Close()
	store := tkNodeFixture(server.URL)
	h, cookie := modelHandler(t, store, &routing.Inventory{})
	response := modelRequest(h, "PUT", "/manager/api/nodes/tk-1", asJSON(updateTKBody(server.URL)), cookie)
	if response.Code != 200 || store.updated.ModelCatalog == nil || store.updated.ModelCatalog.Revision != 7 || store.updated.APIKeyFingerprint != "old-fingerprint" {
		t.Fatalf("update: %d %s", response.Code, response.Body.String())
	}
	body := updateTKBody(server.URL)
	body["catalog_revision"] = 6
	assertManagerError(t, modelRequest(h, "PUT", "/manager/api/nodes/tk-1", asJSON(body), cookie), 409, "catalog_version_conflict")
	delete(body, "catalog_revision")
	if response = modelRequest(h, "PUT", "/manager/api/nodes/tk-1", asJSON(body), cookie); response.Code != 400 {
		t.Fatalf("missing revision: %d", response.Code)
	}
}

func TestTKKeyRepairRediscoversBeforeSavingAndKeepsExpectedRevision(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer repaired-token" {
			t.Error("new token not used")
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"model-a","mode":"text_to_video","durations":[5]},{"id":"new-model","mode":"text_to_video","durations":[6]}]}`)
	}))
	defer server.Close()
	store := tkNodeFixture(server.URL)
	h, cookie := modelHandler(t, store, &routing.Inventory{})
	body := updateTKBody(server.URL)
	delete(body, "enabled_models")
	delete(body, "catalog_revision")
	body["api_key"] = "repaired-token"
	response := modelRequest(h, "PUT", "/manager/api/nodes/tk-1", asJSON(body), cookie)
	if response.Code != 200 || calls != 1 || store.updated.ModelCatalog == nil || store.updated.ModelCatalog.Revision != 7 || store.updated.APIKeyFingerprint != "sha256:test" || !store.updated.ModelCatalog.Items[0].Enabled || store.updated.ModelCatalog.Items[1].Enabled {
		t.Fatalf("key repair: %d %s", response.Code, response.Body.String())
	}
}

func TestStaleSelectionCannotExtendValidityOrCallDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("selection update made network call") }))
	defer server.Close()
	store := tkNodeFixture(server.URL)
	store.catalog.ValidUntil = 1
	h, cookie := modelHandler(t, store, &routing.Inventory{})
	response := modelRequest(h, "PUT", "/manager/api/nodes/tk-1", asJSON(updateTKBody(server.URL)), cookie)
	if response.Code != 400 || store.updatedID != "" {
		t.Fatalf("stale selection: %d %s", response.Code, response.Body.String())
	}
}

func TestManagerModelMutationsUseSameOriginGuard(t *testing.T) {
	h, cookie := modelHandler(t, &catalogNodeStore{}, nil)
	for _, tc := range []struct{ method, path, body string }{{"POST", "/manager/api/nodes", validNodeJSON(true)}, {"POST", "/manager/api/nodes/discover-models", `{}`}, {"POST", "/manager/api/nodes/tk-1/models/refresh", `{}`}, {"PUT", "/manager/api/model-routes", `{}`}, {"POST", "/manager/api/tasks/task-1/route", `{}`}, {"DELETE", "/manager/api/nodes/tk-1?version=1", ""}} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: cookie})
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://attacker.invalid")
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		if response.Code != 403 {
			t.Errorf("%s: %d", tc.path, response.Code)
		}
	}
}

func TestCatalogNullAndNonCanonicalFieldsRejected(t *testing.T) {
	h, cookie := modelHandler(t, &catalogNodeStore{}, nil)
	for _, extra := range []string{`,"enabled_models":null`, `,"catalog_revision":null`, `,"legacy_model_compat":null`, `,"ENABLED_MODELS":["bad"]`} {
		body := strings.TrimSuffix(validNodeJSON(true), "}") + extra + "}"
		if response := modelRequest(h, "POST", "/manager/api/nodes", body, cookie); response.Code != 400 {
			t.Errorf("accepted %s: %d", extra, response.Code)
		}
	}
}

func TestLegacyCompatibilityPreservedAndExplicitExitClearsAlias(t *testing.T) {
	store := tkNodeFixture("https://official.invalid")
	store.items[0].ProtocolVersion = domain.ProtocolOfficial
	store.items[0].LegacyModelCompat = true
	store.items[0].UpstreamModel = "old-alias"
	store.catalog.Source = "manual"
	store.catalog.Items = []domain.NodeModel{{ModelID: "MiniMax-H3", Enabled: true, Present: true}}
	h, cookie := modelHandler(t, store, nil)
	body := updateTKBody("https://official.invalid")
	body["protocol_version"] = domain.ProtocolOfficial
	delete(body, "enabled_models")
	delete(body, "catalog_revision")
	response := modelRequest(h, "PUT", "/manager/api/nodes/tk-1", asJSON(body), cookie)
	if response.Code != 200 || !store.updated.LegacyModelCompat || store.updated.UpstreamModel != "old-alias" {
		t.Fatalf("legacy update: %d %s", response.Code, response.Body.String())
	}
	body["legacy_model_compat"] = false
	body["enabled_models"] = []string{"real-model"}
	body["catalog_revision"] = 7
	response = modelRequest(h, "PUT", "/manager/api/nodes/tk-1", asJSON(body), cookie)
	if response.Code != 200 || store.updated.LegacyModelCompat || store.updated.UpstreamModel != "" || store.updated.ModelCatalog.Items[0].ModelID != "real-model" {
		t.Fatalf("legacy exit: %d %s", response.Code, response.Body.String())
	}
}

func TestTKProbeUsesReadOnlyListWithoutPersistence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v3/contents/generations/tasks" || r.URL.Query().Get("page_size") != "1" {
			t.Error("probe used unexpected endpoint")
		}
		_, _ = io.WriteString(w, `{"total":0,"items":[]}`)
	}))
	defer server.Close()
	store := &catalogNodeStore{}
	h, cookie := modelHandler(t, store, &routing.Inventory{})
	response := modelRequest(h, "POST", "/manager/api/nodes/test", asJSON(newTKBody(server.URL)), cookie)
	if response.Code != 200 || store.createCalls != 0 || store.refreshes != 0 {
		t.Fatalf("probe: %d %s", response.Code, response.Body.String())
	}
}

func TestTKProbeHonorsNodeTimeoutWithoutMutatingClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"total":0,"items":[]}`) }))
	defer server.Close()
	client := server.Client()
	client.Timeout = time.Nanosecond
	h, cookie := modelHandler(t, &catalogNodeStore{}, &routing.Inventory{HTTPClient: client})
	response := modelRequest(h, "POST", "/manager/api/nodes/test", asJSON(newTKBody(server.URL)), cookie)
	if response.Code != 200 || client.Timeout != time.Nanosecond {
		t.Fatalf("node timeout not applied: %d %s", response.Code, response.Body.String())
	}
}

type catalogSecrets struct{ testNodeSecrets }

func (catalogSecrets) Open(_ []byte, ciphertext []byte) (string, error) {
	return strings.TrimPrefix(string(ciphertext), "encrypted:"), nil
}

func TestManagerCatalogRefreshPersistsPreferencesAndFailureState(t *testing.T) {
	var phase atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("catalog request used incorrect credential")
		}
		switch phase.Load() {
		case 0:
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a","mode":"text_to_video","durations":[5]},{"id":"model-b","mode":"text_to_video","durations":[6]}]}`)
		case 1:
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a","mode":"text_to_video","durations":[5]},{"id":"model-c","mode":"reference_to_video","durations":[6]}]}`)
		default:
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"error":{"message":"test-token http://secret.invalid"}}`)
		}
	}))
	defer upstream.Close()
	now := time.Now()
	clock := func() time.Time { return now }
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "catalog.db"), sqlite.Options{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	inv := &routing.Inventory{Store: store, Secrets: catalogSecrets{}, Now: clock, HTTPClient: upstream.Client()}
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Nodes: store, Store: store, Inventory: inv, NodeSecrets: catalogSecrets{}, Now: clock})
	cookie := login(t, h, "admin", "secret", "192.0.2.50:1")
	created := modelRequest(h, "POST", "/manager/api/nodes", asJSON(newTKBody(upstream.URL)), cookie)
	if created.Code != 201 {
		t.Fatalf("create: %s", created.Body.String())
	}
	node, err := store.GetModelNode(context.Background(), "tk-1")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.GetModelCatalog(context.Background(), "tk-1")
	if err != nil {
		t.Fatal(err)
	}
	if initial.Fingerprint != sqlite.CatalogFingerprint(node.ModelNodeInput) || initial.Fingerprint == "" {
		t.Fatal("fingerprint did not derive from encrypted persisted node")
	}
	phase.Store(1)
	now = now.Add(5 * time.Minute)
	refresh := modelRequest(h, "POST", "/manager/api/nodes/tk-1/models/refresh", asJSON(map[string]any{"version": node.Version, "catalog_revision": initial.Revision}), cookie)
	if refresh.Code != 200 {
		t.Fatalf("refresh: %s", refresh.Body.String())
	}
	saved, err := store.GetModelCatalog(context.Background(), "tk-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Items) != 3 || !saved.Items[0].Enabled || saved.Items[1].Present || saved.Items[2].Enabled || !saved.Items[2].Present || saved.ValidUntil != now.Add(30*time.Minute).Unix() {
		t.Fatalf("refresh lost preferences: %+v", saved)
	}
	phase.Store(2)
	now = now.Add(5 * time.Minute)
	failed := modelRequest(h, "POST", "/manager/api/nodes/tk-1/models/refresh", asJSON(map[string]any{"version": node.Version, "catalog_revision": saved.Revision}), cookie)
	assertManagerError(t, failed, 502, "model_discovery_failed")
	after, err := store.GetModelCatalog(context.Background(), "tk-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.LastSuccessAt != saved.LastSuccessAt || after.ValidUntil != saved.ValidUntil || after.Revision != saved.Revision || after.Status != "error" || after.LastErrorCode != "model_discovery_failed" {
		t.Fatalf("failure altered successful snapshot: %+v", after)
	}
	if strings.Contains(failed.Body.String(), "test-token") {
		t.Fatal("credential leaked")
	}
}
