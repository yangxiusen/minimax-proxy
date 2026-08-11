package manager

import (
	"context"
	"encoding/json"
	"net/http"
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
		Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Nodes: store,
		Wake:  func() { wakeCount++ },
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

	updateBody := `{"base_url":"http://private.example:7860","jobs_base_url":"http://jobs.example:8188","public_base_url":"https://video.example","health_path":"/","submit_api_name":"submit","check_api_name":"check","poll_interval":"4s","request_timeout":"30s","enabled":false,"version":1}`
	updated := serve(h, http.MethodPut, "/manager/api/nodes/node-1", updateBody, "application/json", cookie, "192.0.2.1:1", false)
	if updated.Code != http.StatusOK || store.updatedID != "node-1" || store.updatedVer != 1 || store.updated.Enabled || wakeCount != 2 {
		t.Fatalf("update status=%d id=%q version=%d input=%+v wake=%d body=%s", updated.Code, store.updatedID, store.updatedVer, store.updated, wakeCount, updated.Body.String())
	}

	deleted := serve(h, http.MethodDelete, "/manager/api/nodes/node-1?version=2", "", "", cookie, "192.0.2.1:1", false)
	if deleted.Code != http.StatusNoContent || store.deletedID != "node-1" || store.deletedVer != 2 || wakeCount != 3 {
		t.Fatalf("delete status=%d id=%q version=%d wake=%d body=%s", deleted.Code, store.deletedID, store.deletedVer, wakeCount, deleted.Body.String())
	}
}

func TestNodeRequestsAreStrictAndMapDomainConflicts(t *testing.T) {
	store := &nodeStoreStub{createErr: domain.ErrNodeIDConflict}
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Nodes: store})
	cookie := login(t, h, "admin", "secret", "192.0.2.2:1")

	unknown := serve(h, http.MethodPost, "/manager/api/nodes", `{"id":"node-1","unknown":true}`, "application/json", cookie, "192.0.2.2:1", false)
	assertManagerError(t, unknown, http.StatusBadRequest, "bad_request_error")
	duplicate := serve(h, http.MethodPost, "/manager/api/nodes", `{"id":"node-1","id":"node-2"}`, "application/json", cookie, "192.0.2.2:1", false)
	assertManagerError(t, duplicate, http.StatusBadRequest, "bad_request_error")
	conflict := serve(h, http.MethodPost, "/manager/api/nodes", validNodeJSON(true), "application/json", cookie, "192.0.2.2:1", false)
	assertManagerError(t, conflict, http.StatusConflict, "node_id_conflict")

	store.updateErr = domain.ErrNodeHasActiveTask
	update := serve(h, http.MethodPut, "/manager/api/nodes/node-1", `{"base_url":"http://private.example:7860","jobs_base_url":"http://jobs.example:8188","public_base_url":"https://video.example","health_path":"/","submit_api_name":"submit","check_api_name":"check","poll_interval":"3s","request_timeout":"30s","enabled":true,"version":1}`, "application/json", cookie, "192.0.2.2:1", false)
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
		ProbeNode: func(_ context.Context, input domain.ModelNodeInput) NodeProbeResult {
			probed = input
			return NodeProbeResult{Gradio: NodeCheck{OK: true}, Jobs: NodeCheck{OK: false, ErrorCode: "upstream_jobs_unhealthy"}}
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
	if payload.Error["type"] != "node_probe_failed" || !payload.Checks.Gradio.OK || payload.Checks.Jobs.ErrorCode != "upstream_jobs_unhealthy" {
		t.Fatalf("payload=%+v", payload)
	}
}

func validNodeJSON(enabled bool) string {
	value := map[string]any{
		"id": "node-1", "base_url": "http://private.example:7860", "jobs_base_url": "http://jobs.example:8188",
		"public_base_url": "https://video.example", "health_path": "/", "submit_api_name": "submit", "check_api_name": "check",
		"poll_interval": "3s", "request_timeout": "30s", "enabled": enabled,
	}
	data, _ := json.Marshal(value)
	return string(data)
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
