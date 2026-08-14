package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
)

type nodeStoreStub struct {
	mu          sync.Mutex
	items       []domain.ModelNode
	created     domain.ModelNodeInput
	updatedID   string
	updatedVer  int64
	updated     domain.ModelNodeInput
	deletedID   string
	deletedVer  int64
	createErr   error
	updateErr   error
	deleteErr   error
	createCalls int
}

func (s *nodeStoreStub) ListModelNodes(context.Context) ([]domain.ModelNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.ModelNode(nil), s.items...), nil
}

func (s *nodeStoreStub) GetModelNode(_ context.Context, id string) (domain.ModelNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.created.ID == id {
		return domain.ModelNode{ModelNodeInput: s.created, Version: 1}, nil
	}
	for _, item := range s.items {
		if item.ID == id {
			return item, nil
		}
	}
	return domain.ModelNode{}, domain.ErrNodeNotFound
}

type testNodeSecrets struct{}

func (testNodeSecrets) Seal(value string) ([]byte, []byte, string, error) {
	return []byte("nonce"), []byte("encrypted:" + value), "sha256:test", nil
}
func (testNodeSecrets) Open(_, ciphertext []byte) (string, error) {
	return string(ciphertext), nil
}

func (s *nodeStoreStub) CreateModelNode(_ context.Context, input domain.ModelNodeInput) (domain.ModelNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createCalls++
	s.created = input
	if s.createErr != nil {
		return domain.ModelNode{}, s.createErr
	}
	return domain.ModelNode{ModelNodeInput: input, Version: 1, CreatedAt: time.Unix(10, 0), UpdatedAt: time.Unix(10, 0)}, nil
}

func (s *nodeStoreStub) UpdateModelNode(_ context.Context, id string, version int64, input domain.ModelNodeInput) (domain.ModelNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updatedID, s.updatedVer, s.updated = id, version, input
	if s.updateErr != nil {
		return domain.ModelNode{}, s.updateErr
	}
	return domain.ModelNode{ModelNodeInput: input, Version: version + 1, CreatedAt: time.Unix(10, 0), UpdatedAt: time.Unix(20, 0)}, nil
}

func (s *nodeStoreStub) DeleteModelNode(_ context.Context, id string, version int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletedID, s.deletedVer = id, version
	return s.deleteErr
}

func TestNodeCRUDRequiresSessionAndWakesRegistry(t *testing.T) {
	store := &nodeStoreStub{}
	wakeCount := 0
	h := testHandler(Dependencies{
		Admin:       config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Nodes:       store,
		Wake:        func() { wakeCount++ },
		NodeSecrets: testNodeSecrets{},
	})
	if response := serve(h, http.MethodGet, "/manager/api/nodes", "", "", "", "192.0.2.1:1", false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", response.Code)
	}
	cookie := login(t, h, "admin", "secret", "192.0.2.1:1")
	body := validNodeJSON(true)
	created := serve(h, http.MethodPost, "/manager/api/nodes", body, "application/json", cookie, "192.0.2.1:1", false)
	if created.Code != http.StatusCreated || created.Header().Get("Location") != "/manager/api/nodes/node-1" {
		t.Fatalf("create status=%d location=%q body=%s", created.Code, created.Header().Get("Location"), created.Body.String())
	}
	if store.created.ID != "node-1" || store.created.PollInterval != 3*time.Second || !store.created.Enabled || wakeCount != 1 {
		t.Fatalf("created=%+v wake=%d", store.created, wakeCount)
	}

	updateBody := `{"service_url":"http://private.example:7860","protocol_version":"h3-node-v1","poll_interval":"4s","request_timeout":"30s","enabled":false,"version":1}`
	updated := serve(h, http.MethodPut, "/manager/api/nodes/node-1", updateBody, "application/json", cookie, "192.0.2.1:1", false)
	if updated.Code != http.StatusOK || store.updatedID != "node-1" || store.updatedVer != 1 || store.updated.Enabled || wakeCount != 2 {
		t.Fatalf("update status=%d id=%q version=%d input=%+v wake=%d body=%s", updated.Code, store.updatedID, store.updatedVer, store.updated, wakeCount, updated.Body.String())
	}

	deleted := serve(h, http.MethodDelete, "/manager/api/nodes/node-1?version=2", "", "", cookie, "192.0.2.1:1", false)
	if deleted.Code != http.StatusNoContent || store.deletedID != "node-1" || store.deletedVer != 2 || wakeCount != 3 {
		t.Fatalf("delete status=%d id=%q version=%d wake=%d body=%s", deleted.Code, store.deletedID, store.deletedVer, wakeCount, deleted.Body.String())
	}
}

func TestUpdateNodeWithEmptyAPIKeyReusesStoredSecret(t *testing.T) {
	stored := domain.ModelNodeInput{
		ID:                "node-1",
		ServiceURL:        "http://private.example:7860",
		ProtocolVersion:   "h3-node-v1",
		APIKeyCiphertext:  []byte("stored-ciphertext"),
		APIKeyNonce:       []byte("stored-nonce"),
		APIKeyFingerprint: "sha256:stored",
		PollInterval:      3 * time.Second,
		RequestTimeout:    30 * time.Second,
		Enabled:           true,
	}
	store := &nodeStoreStub{items: []domain.ModelNode{{ModelNodeInput: stored, Version: 1}}}
	h := testHandler(Dependencies{
		Admin:       config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Nodes:       store,
		NodeSecrets: testNodeSecrets{},
	})
	cookie := login(t, h, "admin", "secret", "192.0.2.6:1")
	response := serve(h, http.MethodPut, "/manager/api/nodes/node-1", `{"service_url":"http://new.example:7860","protocol_version":"h3-node-v1","api_key":"","poll_interval":"4s","request_timeout":"45s","enabled":true,"version":1}`, "application/json", cookie, "192.0.2.6:1", false)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if string(store.updated.APIKeyCiphertext) != string(stored.APIKeyCiphertext) ||
		string(store.updated.APIKeyNonce) != string(stored.APIKeyNonce) ||
		store.updated.APIKeyFingerprint != stored.APIKeyFingerprint {
		t.Fatalf("stored secret was not reused: updated=%+v", store.updated)
	}
}

func TestNodeProbeWithStoredKeyKeepsDraftConnectionSettings(t *testing.T) {
	const storedKey = "Abcdefghijklmnopqrstuvwx12345678"
	stored := domain.ModelNodeInput{
		ID:                "node-1",
		ServiceURL:        "http://old.example:7860",
		ProtocolVersion:   "h3-node-v1",
		APIKeyCiphertext:  []byte(storedKey),
		APIKeyNonce:       []byte("stored-nonce"),
		APIKeyFingerprint: "sha256:stored",
		PollInterval:      3 * time.Second,
		RequestTimeout:    30 * time.Second,
		Enabled:           true,
	}
	store := &nodeStoreStub{items: []domain.ModelNode{{ModelNodeInput: stored, Version: 1}}}
	var probed NodeProbeInput
	h := testHandler(Dependencies{
		Admin:       config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Nodes:       store,
		NodeSecrets: testNodeSecrets{},
		ProbeNode: func(_ context.Context, input NodeProbeInput) NodeProbeResult {
			probed = input
			return NodeProbeResult{Reachable: true, Authenticated: true, ProtocolVersion: "h3-node-v1"}
		},
	})
	cookie := login(t, h, "admin", "secret", "192.0.2.7:1")
	response := serve(h, http.MethodPost, "/manager/api/nodes/test", `{"id":"node-1","service_url":"http://new.example:7860","protocol_version":"h3-node-v1","poll_interval":"4s","request_timeout":"45s","enabled":false,"use_stored_api_key":true}`, "application/json", cookie, "192.0.2.7:1", false)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if probed.APIKey != storedKey || probed.Node.ServiceURL != "http://new.example:7860" ||
		probed.Node.PollInterval != 4*time.Second || probed.Node.RequestTimeout != 45*time.Second || probed.Node.Enabled {
		t.Fatalf("probe did not preserve draft settings: %+v", probed)
	}
}

func TestNodeProbeCannotReuseMissingStoredKey(t *testing.T) {
	store := &nodeStoreStub{items: []domain.ModelNode{{ModelNodeInput: domain.ModelNodeInput{
		ID: "node-1", ServiceURL: "http://old.example:7860", ProtocolVersion: "legacy-gradio-v1",
		PollInterval: 3 * time.Second, RequestTimeout: 30 * time.Second, Enabled: true,
	}, Version: 1}}}
	h := testHandler(Dependencies{
		Admin:       config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Nodes:       store,
		NodeSecrets: testNodeSecrets{},
		ProbeNode: func(context.Context, NodeProbeInput) NodeProbeResult {
			return NodeProbeResult{Reachable: true, Authenticated: true, ProtocolVersion: "h3-node-v1"}
		},
	})
	cookie := login(t, h, "admin", "secret", "192.0.2.8:1")
	response := serve(h, http.MethodPost, "/manager/api/nodes/test", `{"id":"node-1","service_url":"http://new.example:7860","protocol_version":"h3-node-v1","poll_interval":"4s","request_timeout":"45s","enabled":true,"use_stored_api_key":true}`, "application/json", cookie, "192.0.2.8:1", false)
	assertManagerError(t, response, http.StatusBadRequest, "bad_request_error")
}

func TestNodeRequestsAreStrictAndMapDomainConflicts(t *testing.T) {
	store := &nodeStoreStub{createErr: domain.ErrNodeIDConflict}
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Nodes: store, NodeSecrets: testNodeSecrets{}})
	cookie := login(t, h, "admin", "secret", "192.0.2.2:1")

	unknown := serve(h, http.MethodPost, "/manager/api/nodes", `{"id":"node-1","unknown":true}`, "application/json", cookie, "192.0.2.2:1", false)
	assertManagerError(t, unknown, http.StatusBadRequest, "bad_request_error")
	duplicate := serve(h, http.MethodPost, "/manager/api/nodes", `{"id":"node-1","id":"node-2"}`, "application/json", cookie, "192.0.2.2:1", false)
	assertManagerError(t, duplicate, http.StatusBadRequest, "bad_request_error")
	conflict := serve(h, http.MethodPost, "/manager/api/nodes", validNodeJSON(true), "application/json", cookie, "192.0.2.2:1", false)
	assertManagerError(t, conflict, http.StatusConflict, "node_id_conflict")

	store.items = []domain.ModelNode{{ModelNodeInput: domain.ModelNodeInput{ID: "node-1", ServiceURL: "http://private.example:7860", ProtocolVersion: "h3-node-v1", APIKeyCiphertext: []byte("encrypted"), APIKeyNonce: []byte("nonce"), APIKeyFingerprint: "sha256:test", PollInterval: 3 * time.Second, RequestTimeout: 30 * time.Second, Enabled: true}, Version: 1}}
	store.updateErr = domain.ErrNodeHasActiveTask
	update := serve(h, http.MethodPut, "/manager/api/nodes/node-1", `{"service_url":"http://private.example:7860","protocol_version":"h3-node-v1","poll_interval":"3s","request_timeout":"30s","enabled":true,"version":1}`, "application/json", cookie, "192.0.2.2:1", false)
	assertManagerError(t, update, http.StatusConflict, "node_has_active_task")

	store.deleteErr = domain.ErrNodeMustBeDisabled
	deleted := serve(h, http.MethodDelete, "/manager/api/nodes/node-1?version=1", "", "", cookie, "192.0.2.2:1", false)
	assertManagerError(t, deleted, http.StatusConflict, "node_must_be_disabled")
	invalidID := serve(h, http.MethodDelete, "/manager/api/nodes/bad%20id?version=1", "", "", cookie, "192.0.2.2:1", false)
	assertManagerError(t, invalidID, http.StatusBadRequest, "bad_request_error")
}

func TestNodeProbeReturnsChecksWithoutPersisting(t *testing.T) {
	store := &nodeStoreStub{}
	var probed domain.ModelNodeInput
	h := testHandler(Dependencies{
		Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Nodes: store,
		ProbeNode: func(_ context.Context, input NodeProbeInput) NodeProbeResult {
			probed = input.Node
			return NodeProbeResult{Reachable: true, Authenticated: false, ProtocolVersion: "h3-node-v1", Checks: []NodeCheck{{Name: "health", Status: "passed"}, {Name: "authentication", Status: "failed", ErrorCode: "node_authentication_failed"}}}
		},
	})
	cookie := login(t, h, "admin", "secret", "192.0.2.3:1")
	response := serve(h, http.MethodPost, "/manager/api/nodes/test", validNodeJSON(true), "application/json", cookie, "192.0.2.3:1", false)
	if response.Code != http.StatusBadGateway || store.createCalls != 0 || probed.ID != "node-1" {
		t.Fatalf("probe status=%d creates=%d input=%+v body=%s", response.Code, store.createCalls, probed, response.Body.String())
	}
	var payload struct {
		Error  map[string]string `json:"error"`
		Checks NodeProbeResult   `json:"checks"`
	}
	decodeResponse(t, response, &payload)
	if payload.Error["type"] != "node_probe_failed" || !payload.Checks.Reachable || payload.Checks.Authenticated || payload.Checks.Checks[1].ErrorCode != "node_authentication_failed" {
		t.Fatalf("payload=%+v", payload)
	}
}

func validNodeJSON(enabled bool) string {
	value := map[string]any{
		"id": "node-1", "service_url": "http://private.example:7860", "protocol_version": "h3-node-v1",
		"api_key": "Abcdefghijklmnopqrstuvwx12345678", "poll_interval": "3s", "request_timeout": "30s", "enabled": enabled,
	}
	data, _ := json.Marshal(value)
	return string(data)
}

func TestNodeAPIKeyIsExactly32AlphanumericCharacters(t *testing.T) {
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Nodes: &nodeStoreStub{}, NodeSecrets: testNodeSecrets{}})
	cookie := login(t, h, "admin", "secret", "192.0.2.4:1")
	for _, key := range []string{"short", strings.Repeat("A", 31), strings.Repeat("A", 33), strings.Repeat("A", 31) + "!"} {
		value := map[string]any{
			"id": "node-1", "service_url": "http://private.example:7860", "protocol_version": "h3-node-v1",
			"api_key": key, "poll_interval": "3s", "request_timeout": "30s", "enabled": true,
		}
		body, _ := json.Marshal(value)
		response := serve(h, http.MethodPost, "/manager/api/nodes", string(body), "application/json", cookie, "192.0.2.4:1", false)
		assertManagerError(t, response, http.StatusBadRequest, "bad_request_error")
	}
}

func TestDeprecatedNodeAPIKeyIDIsRejected(t *testing.T) {
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Nodes: &nodeStoreStub{}, NodeSecrets: testNodeSecrets{}})
	cookie := login(t, h, "admin", "secret", "192.0.2.5:1")
	response := serve(h, http.MethodPost, "/manager/api/nodes", `{"id":"node-1","api_key_id":"proxy"}`, "application/json", cookie, "192.0.2.5:1", false)
	assertManagerError(t, response, http.StatusBadRequest, "bad_request_error")
}

func assertManagerError(t *testing.T, response interface{ Result() *http.Response }, status int, kind string) {
	t.Helper()
	result := response.Result()
	if result.StatusCode != status {
		t.Fatalf("status=%d want=%d", result.StatusCode, status)
	}
	defer result.Body.Close()
	var payload struct {
		Error map[string]string `json:"error"`
	}
	if err := json.NewDecoder(result.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error["type"] != kind {
		t.Fatalf("error=%v want=%q", payload.Error, kind)
	}
}
