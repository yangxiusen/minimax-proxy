package manager

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/routing"
)

type modelRouteMemory struct {
	routes                           []domain.ModelRoute
	revision                         int64
	boundModel, boundProtocol, admin string
	binds                            int
	err                              error
}

func (s *modelRouteMemory) ListModelRoutes(context.Context) ([]domain.ModelRoute, int64, error) {
	return s.routes, s.revision, nil
}
func (s *modelRouteMemory) GetModelRoute(_ context.Context, model string) (domain.ModelRoute, int64, error) {
	for _, r := range s.routes {
		if r.Model == model {
			return r, s.revision, nil
		}
	}
	return domain.ModelRoute{}, s.revision, domain.ErrUnsupportedModel
}
func (s *modelRouteMemory) BindModelRoute(_ context.Context, model, p, admin string, v, rev int64) (domain.ModelRoute, int64, error) {
	s.binds++
	s.boundModel, s.boundProtocol, s.admin = model, p, admin
	if s.err != nil {
		return domain.ModelRoute{}, 0, s.err
	}
	r, _, _ := s.GetModelRoute(context.Background(), model)
	r.ProtocolVersion = p
	r.SelectionMode = "manual"
	r.Version = v + 1
	return r, rev + 1, nil
}
func routeHandler(t *testing.T, s *modelRouteMemory) (http.Handler, string) {
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, RouteStore: s, Routing: &routing.Service{Healthy: func(id string) bool { return id == "healthy" }}})
	return h, login(t, h, "admin", "secret", "192.0.2.50:1")
}
func routeFixture() *modelRouteMemory {
	return &modelRouteMemory{revision: 18, routes: []domain.ModelRoute{{Model: "model-a", SelectionMode: "unresolved", Version: 1, Candidates: []domain.RouteCandidate{{ProtocolVersion: domain.ProtocolH3, NodeIDs: []string{"unhealthy"}}, {ProtocolVersion: domain.ProtocolOfficial, NodeIDs: []string{"healthy"}}}, LegacyAwaitingTasks: 2}, {Model: "model-b", ProtocolVersion: domain.ProtocolTK2SD, SelectionMode: "manual", Version: 2}}}
}

func TestModelRouteListRetainsUnavailableAndUsesHealthWithoutWrites(t *testing.T) {
	s := routeFixture()
	h, cookie := routeHandler(t, s)
	response := modelRequest(h, "GET", "/manager/api/model-routes?page_size=1&state=ambiguous", "", cookie)
	var got struct {
		Revision int64 `json:"routing_revision"`
		Total    int   `json:"total"`
		Items    []struct {
			Protocol   *string                 `json:"protocol_version"`
			Candidates []domain.RouteCandidate `json:"candidates"`
			Legacy     int                     `json:"legacy_awaiting_tasks"`
		} `json:"items"`
	}
	decodeResponse(t, response, &got)
	if response.Code != 200 || got.Revision != 18 || got.Total != 1 || len(got.Items) != 1 || got.Items[0].Protocol != nil || got.Items[0].Candidates[1].HealthyNodes != 1 || got.Items[0].Legacy != 2 || s.binds != 0 {
		t.Fatalf("routes: %s", response.Body.String())
	}
	response = modelRequest(h, "GET", "/manager/api/model-routes?state=unavailable", "", cookie)
	var unavailable struct {
		Items []domain.ModelRoute `json:"items"`
	}
	decodeResponse(t, response, &unavailable)
	if len(unavailable.Items) != 1 || unavailable.Items[0].Model != "model-b" || unavailable.Items[0].ProtocolVersion != domain.ProtocolTK2SD {
		t.Fatal("manual route disappeared")
	}
}

func TestModelRouteBindAllowsUnhealthyProtocolAndRequiresVersions(t *testing.T) {
	s := routeFixture()
	h, cookie := routeHandler(t, s)
	response := modelRequest(h, "PUT", "/manager/api/model-routes", `{"model":"model-a","protocol_version":"h3-node-v1","version":1,"routing_revision":18}`, cookie)
	var got struct {
		Revision int64  `json:"routing_revision"`
		State    string `json:"state"`
		Protocol string `json:"protocol_version"`
	}
	decodeResponse(t, response, &got)
	if response.Code != 200 || got.State != "unavailable" || got.Protocol != domain.ProtocolH3 || got.Revision != 19 || s.admin != "admin" || s.binds != 1 {
		t.Fatalf("bind: %s", response.Body.String())
	}
	response = modelRequest(h, "PUT", "/manager/api/model-routes", `{"model":"model-a","protocol_version":"h3-node-v1","version":1,"routing_revision":17}`, cookie)
	assertManagerError(t, response, 409, "model_catalog_changed")
	response = modelRequest(h, "PUT", "/manager/api/model-routes", `{"model":"model-a","protocol_version":"h3-node-v1","version":2,"routing_revision":18}`, cookie)
	assertManagerError(t, response, 409, "model_route_version_conflict")
	if s.binds != 1 {
		t.Fatal("stale request reached store")
	}
}

func TestModelRouteRequestsRejectInvalidFields(t *testing.T) {
	s := routeFixture()
	h, cookie := routeHandler(t, s)
	for _, body := range []string{`{"model":"*","protocol_version":"h3-node-v1","version":1,"routing_revision":18}`, `{"model":"model-a","protocol_version":"unknown","version":1,"routing_revision":18}`, `{"model":"model-a","protocol_version":"tk2sd-v1","version":1,"routing_revision":18}`, `{"model":"model-a","protocol_version":"h3-node-v1","version":1,"routing_revision":18,"fallback":true}`} {
		if response := modelRequest(h, "PUT", "/manager/api/model-routes", body, cookie); response.Code != 400 {
			t.Fatalf("invalid bind: %d %s", response.Code, response.Body.String())
		}
	}
	for _, query := range []string{"?state=bad", "?q=a&q=b", "?page_size=101", "?unknown=1"} {
		if response := modelRequest(h, "GET", "/manager/api/model-routes"+query, "", cookie); response.Code != 400 {
			t.Fatalf("query: %d", response.Code)
		}
	}
	if s.binds != 0 {
		t.Fatal("invalid bind reached store")
	}
}

type legacyRouteStore struct {
	taskStoreStub
	boundID, protocol string
	version, revision int64
	binds             int
	bindErr           error
}

func (s *legacyRouteStore) BindLegacyTaskRoute(_ context.Context, id, p string, v, rev int64) (domain.Task, error) {
	s.binds++
	s.boundID, s.protocol, s.version, s.revision = id, p, v, rev
	if s.bindErr != nil {
		return domain.Task{}, s.bindErr
	}
	return domain.Task{TaskID: id, ProtocolVersion: p, RouteState: "ready", Version: v + 1}, nil
}

func TestLegacyTaskRouteDelegatesGuardedStoreWithoutChangingRequest(t *testing.T) {
	s := &legacyRouteStore{}
	s.detail.Task = domain.Task{TaskID: "task-1", Model: "model-a", Status: domain.StatusQueuedOpen, RouteState: "awaiting_route", Version: 7}
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Store: s})
	cookie := login(t, h, "admin", "secret", "192.0.2.50:1")
	response := modelRequest(h, "POST", "/manager/api/tasks/task-1/route", `{"protocol_version":"tk2sd-v1","version":7,"routing_revision":18}`, cookie)
	var got struct {
		TaskID     string `json:"task_id"`
		Version    int64  `json:"version"`
		RouteState string `json:"route_state"`
	}
	decodeResponse(t, response, &got)
	if response.Code != 200 || got.TaskID != "task-1" || got.Version != 8 || got.RouteState != "ready" || s.version != 7 || s.revision != 18 {
		t.Fatalf("legacy bind: %s", response.Body.String())
	}
	s.detail.Task.RouteState = "migration_blocked"
	response = modelRequest(h, "POST", "/manager/api/tasks/task-1/route", `{"protocol_version":"tk2sd-v1","version":7,"routing_revision":18}`, cookie)
	assertManagerError(t, response, 409, "migration_reconciliation_required")
	if s.binds != 1 {
		t.Fatal("blocked migration reached binder")
	}
}

type remoteManagerStore struct {
	taskStoreStub
	tasks map[string]domain.Task
	runs  map[string]domain.RemoteRun
}

func (s *remoteManagerStore) GetRemoteRun(_ context.Context, id string) (domain.RemoteRun, error) {
	run, ok := s.runs[id]
	if !ok {
		return domain.RemoteRun{}, domain.ErrTaskNotFound
	}
	return run, nil
}

func (s *remoteManagerStore) GetTaskForExecution(_ context.Context, id string) (domain.Task, error) {
	task, ok := s.tasks[id]
	if !ok {
		return domain.Task{}, domain.ErrTaskNotFound
	}
	return task, nil
}

type remoteResultSpy struct {
	calls    int
	err      error
	metadata string
}

func (s *remoteResultSpy) Refresh(_ context.Context, task domain.Task) (domain.Task, error) {
	s.calls++
	task.ResultPublicURL = "http://bound-node.invalid/media/tasks/remote-id?expires=9999999999&signature=validated"
	task.ResultMetadataJSON = s.metadata
	if s.metadata != "" {
		task.MetadataStatus = "ready"
	}
	return task, s.err
}

func TestManagerRemoteCancellationMapsNotCancellable(t *testing.T) {
	s := &taskStoreStub{cancelError: domain.ErrRemoteNotCancellable}
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Store: s})
	cookie := login(t, h, "admin", "secret", "192.0.2.50:1")
	response := modelRequest(h, "POST", "/manager/api/tasks/task-1/cancel", "", cookie)
	assertManagerError(t, response, 409, "task_not_cancellable")
}

func TestManagerRemoteDetailUsesActualOrUnknownMetadata(t *testing.T) {
	for _, metadata := range []string{"", `{"duration":4.72,"width":1280,"height":720,"ratio":"16:9","resolution":"720p"}`} {
		task := domain.Task{TaskID: "task-1", Model: "video-1.5-pro", ProtocolVersion: domain.ProtocolTK2SD, RouteState: "ready", Status: domain.StatusSucceeded, Resolution: "1080p", RatioRequested: "1:1", Duration: 5, RequestJSON: `{"model":"video-1.5-pro","content":[],"duration":5}`, MetadataStatus: "pending", Version: 7}
		s := &remoteManagerStore{tasks: map[string]domain.Task{"task-1": task}}
		s.detail.Task = task
		remote := &remoteResultSpy{metadata: metadata}
		h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Store: s, RemoteResults: remote})
		cookie := login(t, h, "admin", "secret", "192.0.2.50:1")
		response := modelRequest(h, "GET", "/manager/api/tasks/task-1", "", cookie)
		var got map[string]any
		decodeResponse(t, response, &got)
		if response.Code != 200 || remote.calls != 1 || got["video_url"] == nil || got["status"] != "succeeded" {
			t.Fatalf("detail: %s", response.Body.String())
		}
		if metadata == "" {
			if got["duration"] != nil || got["ratio"] != nil || got["resolution"] != nil {
				t.Fatal("request metadata presented as actual")
			}
		} else if got["duration"] != 4.72 || got["ratio"] != "16:9" || got["resolution"] != "720p" {
			t.Fatalf("wrong metadata: %+v", got)
		}
	}
}

func TestManagerRemoteListKeepsSuccessOnRefreshFailureAndUsesFrozenProtocol(t *testing.T) {
	task := domain.Task{TaskID: "task-1", ProtocolVersion: domain.ProtocolTK2SD, Status: domain.StatusSucceeded, Resolution: "1080p", MetadataStatus: "pending"}
	s := &remoteManagerStore{tasks: map[string]domain.Task{"task-1": task}}
	s.items = []domain.AdminTaskSummary{{TaskID: "task-1", Status: domain.V2Succeeded, InternalStatus: domain.StatusSucceeded, UpstreamProtocol: domain.ProtocolOfficial, Resolution: "1080p"}}
	s.total = 1
	remote := &remoteResultSpy{err: errors.New("private failure and secret token")}
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Store: s, RemoteResults: remote})
	cookie := login(t, h, "admin", "secret", "192.0.2.50:1")
	response := modelRequest(h, "GET", "/manager/api/tasks", "", cookie)
	var got struct {
		Items []map[string]any `json:"items"`
	}
	decodeResponse(t, response, &got)
	if response.Code != 200 || remote.calls != 1 || len(got.Items) != 1 || got.Items[0]["status"] != "succeeded" || got.Items[0]["video_url"] != nil || got.Items[0]["resolution"] != nil || got.Items[0]["delivery_error"] == nil {
		t.Fatalf("list failed: %s", response.Body.String())
	}
}

func TestPublicVideoURLUsesFrozenRemoteTask(t *testing.T) {
	task := domain.Task{TaskID: "task-1", ProtocolVersion: domain.ProtocolTK2SD, Status: domain.StatusSucceeded}
	store := &remoteManagerStore{tasks: map[string]domain.Task{"task-1": task}}
	remote := &remoteResultSpy{}
	h := &handler{store: store, remoteResults: remote}
	u, err := h.publicVideoURL(context.Background(), domain.AdminTaskSummary{TaskID: "task-1", Status: domain.V2Succeeded, ResultPublicURL: "http://untrusted.invalid"})
	if err != nil || u == nil || remote.calls != 1 || *u == "http://untrusted.invalid" {
		t.Fatalf("remote result: %v %v", u, err)
	}
}

func TestManagerRemoteStatusUsesPersistedPhase(t *testing.T) {
	task := domain.Task{TaskID: "task-1", ProtocolVersion: domain.ProtocolTK2SD, Status: domain.StatusRunning, UpstreamID: "node-1", RequestJSON: `{"content":[{"type":"text","text":"hello"}],"duration":5,"resolution":"1080p"}`}
	store := &remoteManagerStore{tasks: map[string]domain.Task{"task-1": task}, runs: map[string]domain.RemoteRun{"task-1": {TaskID: "task-1", NodeID: "node-1", Phase: domain.RemoteSubmitted, UpstreamStatus: "queued", SubmissionKey: "must-not-expose"}}}
	store.detail.Task = task
	store.items = []domain.AdminTaskSummary{{TaskID: "task-1", Status: domain.V2Queued, InternalStatus: domain.StatusRunning, UpstreamProtocol: domain.ProtocolTK2SD}}
	store.total = 1
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Store: store})
	cookie := login(t, h, "admin", "secret", "192.0.2.50:1")
	response := modelRequest(h, "GET", "/manager/api/tasks/task-1", "", cookie)
	var detail map[string]any
	decodeResponse(t, response, &detail)
	if detail["status"] != "queued" || detail["phase"] != "upstream_queued" || detail["remote"] == nil {
		t.Fatalf("detail phase: %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "must-not-expose") {
		t.Fatal("remote submission secret exposed")
	}
	response = modelRequest(h, "GET", "/manager/api/tasks", "", cookie)
	var list struct {
		Items []map[string]any `json:"items"`
	}
	decodeResponse(t, response, &list)
	if list.Items[0]["status"] != "queued" || list.Items[0]["phase"] != "upstream_queued" {
		t.Fatalf("list phase: %s", response.Body.String())
	}
}

func TestLegacyRouteSubmissionEvidenceConflictIsNotVersionConflict(t *testing.T) {
	s := &legacyRouteStore{bindErr: domain.ErrStateConflict}
	s.detail.Task = domain.Task{TaskID: "task-1", Status: domain.StatusQueuedOpen, RouteState: "awaiting_route", Version: 7}
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Store: s})
	cookie := login(t, h, "admin", "secret", "192.0.2.50:1")
	response := modelRequest(h, "POST", "/manager/api/tasks/task-1/route", `{"protocol_version":"tk2sd-v1","version":7,"routing_revision":18}`, cookie)
	assertManagerError(t, response, 409, "task_route_not_editable")
}
