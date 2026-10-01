package remote_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/httpapi/v2"
	"minimax-h3-tc/internal/inputspool"
	"minimax-h3-tc/internal/protocol"
	"minimax-h3-tc/internal/remote"
	"minimax-h3-tc/internal/routing"
	"minimax-h3-tc/internal/store/sqlite"
	"minimax-h3-tc/internal/upstream/tk2sd"
)

const integrationModel = "integration-seedance"
const integrationNodeKey = "local-test-node-key"

type integrationJob struct {
	ID, Body, Status string
}

type integrationUpstream struct {
	model                                                           string
	nodeID                                                          string
	actualDuration                                                  float64
	t                                                               *testing.T
	mu                                                              sync.Mutex
	server                                                          *httptest.Server
	store                                                           *sqlite.Store
	jobs                                                            map[string]*integrationJob
	assets                                                          map[string][]byte
	submitKeys, submitBodies                                        []string
	modelCalls, uploadCalls, queryCalls, metadataCalls, cancelCalls int
	loseSubmitResponse                                              bool
	metadataUnavailable                                             bool
	blockCancel                                                     <-chan struct{}
	cancelStarted                                                   chan<- struct{}
}

func newIntegrationUpstream(t *testing.T, modelID ...string) *integrationUpstream {
	t.Helper()
	model := integrationModel
	if len(modelID) > 0 {
		model = modelID[0]
	}
	u := &integrationUpstream{t: t, model: model, nodeID: "tk-integration", actualDuration: 5.062, jobs: map[string]*integrationJob{}, assets: map[string][]byte{}}
	u.server = httptest.NewServer(http.HandlerFunc(u.serveHTTP))
	t.Cleanup(u.server.Close)
	return u
}

func (u *integrationUpstream) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+integrationNodeKey {
		u.t.Error("node authorization missing")
		w.WriteHeader(401)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		u.mu.Lock()
		u.modelCalls++
		u.mu.Unlock()
		models := []tk2sd.Model{{ID: u.model, Mode: "reference_to_video", Durations: []int{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}}}
		if u.model == integrationModel {
			models = append(models, tk2sd.Model{ID: u.model, Mode: "text_to_video", Durations: []int{5}}, tk2sd.Model{ID: u.model, Mode: "image_to_video", Durations: []int{5}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": models})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/assets":
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			u.t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, header, err := r.FormFile("file")
		if err != nil {
			u.t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer file.Close()
		payload, err := io.ReadAll(file)
		if err != nil {
			u.t.Error(err)
			w.WriteHeader(400)
			return
		}
		decoded, err := png.Decode(bytes.NewReader(payload))
		if err != nil {
			u.t.Error("upload is not original PNG", err)
			w.WriteHeader(422)
			return
		}
		u.mu.Lock()
		u.uploadCalls++
		id := fmt.Sprintf("%032x", u.uploadCalls+100)
		u.assets[id] = payload
		u.mu.Unlock()
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(tk2sd.Asset{ID: id, URL: "asset://" + id, Kind: "image", Filename: header.Filename, Width: decoded.Bounds().Dx(), Height: decoded.Bounds().Dy(), Size: int64(len(payload))})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v3/contents/generations/tasks":
		u.submit(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v3/contents/generations/tasks/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v3/contents/generations/tasks/")
		u.mu.Lock()
		var job *integrationJob
		for _, candidate := range u.jobs {
			if candidate.ID == id {
				job = candidate
				break
			}
		}
		if job == nil {
			u.mu.Unlock()
			w.WriteHeader(404)
			return
		}
		if r.Method == http.MethodDelete {
			u.cancelCalls++
			block, started := u.blockCancel, u.cancelStarted
			u.mu.Unlock()
			if started != nil {
				select {
				case started <- struct{}{}:
				default:
				}
			}
			if block != nil {
				select {
				case <-block:
				case <-r.Context().Done():
					return
				}
			}
			u.mu.Lock()
			if job.Status != "queued" {
				u.mu.Unlock()
				w.WriteHeader(409)
				return
			}
			job.Status = "cancelled"
			u.mu.Unlock()
			w.WriteHeader(204)
			return
		}
		if r.Method != http.MethodGet {
			u.mu.Unlock()
			u.t.Error("unexpected task method", r.Method)
			w.WriteHeader(405)
			return
		}
		u.queryCalls++
		status := job.Status
		var submitted domain.GenerationRequest
		_ = json.Unmarshal([]byte(job.Body), &submitted)
		u.mu.Unlock()
		result := map[string]any{"id": id, "model": u.model, "status": status, "duration": submitted.Duration}
		if status == "succeeded" {
			result["content"] = map[string]string{"video_url": u.server.URL + "/media/tasks/" + id + "?expires=9999999999&signature=local-signed"}
		}
		_ = json.NewEncoder(w).Encode(result)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/tasks/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/tasks/")
		u.mu.Lock()
		u.metadataCalls++
		unavailable := u.metadataUnavailable
		actualDuration := u.actualDuration
		u.mu.Unlock()
		if unavailable {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "succeeded", "content": tk2sd.Metadata{Duration: actualDuration, Width: 1280, Height: 720}})
	default:
		u.t.Error("unexpected upstream endpoint", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}
}

func (u *integrationUpstream) submit(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		u.t.Error(err)
		w.WriteHeader(400)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	parts := strings.Split(key, ":")
	if len(parts) != 3 || parts[0] != "proxy" || parts[1] == "" {
		u.t.Error("invalid durable namespace key")
		w.WriteHeader(400)
		return
	}
	// 在 HTTP 边界验证已提交的持久证据；这里若仍持有 SQLite 写事务，测试会超时。
	run, err := u.store.GetRemoteRun(r.Context(), parts[2])
	digest := sha256.Sum256(body)
	if err != nil || run.Phase != domain.RemoteSubmitIntent || run.NodeID != u.nodeID || run.SubmissionKey != key || run.RequestBodyJSON != string(body) || run.RequestBodyHash != hex.EncodeToString(digest[:]) || run.SubmitAttempts < 1 {
		u.t.Error("HTTP create preceded durable intent", err, run.Phase)
		w.WriteHeader(500)
		return
	}
	var request domain.GenerationRequest
	if err = json.Unmarshal(body, &request); err != nil || request.Model != u.model {
		u.t.Error("invalid create body", err)
		w.WriteHeader(400)
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	for index, item := range request.Content {
		if item.Type == "text" {
			continue
		}
		if item.Media() == nil {
			u.t.Error("media missing")
			w.WriteHeader(422)
			return
		}
		assetID := strings.TrimPrefix(item.Media().URL, "asset://")
		wantRole := "first_frame"
		if u.model != integrationModel {
			wantRole = "reference_image"
		}
		if _, ok := u.assets[assetID]; !ok || item.Role != wantRole {
			u.t.Error("nonpersisted asset or missing normalized role")
			w.WriteHeader(422)
			return
		}
		asset, err := u.store.GetRemoteAsset(r.Context(), parts[2], run.NodeID, index)
		if err != nil || asset.AssetID != assetID {
			u.t.Error("create preceded durable asset", err)
			w.WriteHeader(500)
			return
		}
	}
	u.submitKeys = append(u.submitKeys, key)
	u.submitBodies = append(u.submitBodies, string(body))
	job := u.jobs[key]
	if job != nil && job.Body != string(body) {
		w.WriteHeader(409)
		return
	}
	if job == nil {
		job = &integrationJob{ID: fmt.Sprintf("%032x", len(u.jobs)+1), Body: string(body), Status: "queued"}
		u.jobs[key] = job
	}
	if u.loseSubmitResponse {
		u.loseSubmitResponse = false
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			u.t.Error(err)
			return
		}
		_, _ = io.WriteString(connection, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 128\r\nConnection: close\r\n\r\n{")
		_ = connection.Close()
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"id": job.ID})
}

func (u *integrationUpstream) setStatus(taskID, status string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for key, job := range u.jobs {
		if strings.HasSuffix(key, ":"+taskID) {
			job.Status = status
			return
		}
	}
	u.t.Fatalf("upstream job for %s missing", taskID)
}

type integrationFixture struct {
	logger     *slog.Logger
	t          *testing.T
	store      *sqlite.Store
	upstream   *integrationUpstream
	processor  *remote.Processor
	handler    http.Handler
	node       domain.ModelNode
	path, root string
	options    sqlite.Options
}

func newIntegrationFixture(t *testing.T, modelID ...string) *integrationFixture {
	t.Helper()
	f := &integrationFixture{t: t, upstream: newIntegrationUpstream(t, modelID...), root: filepath.Join(t.TempDir(), "inputs"), path: filepath.Join(t.TempDir(), "tasks.db"), options: sqlite.Options{PerKeyLimit: 20, GlobalLimit: 100, Retention: time.Hour, IdempotencyTTL: time.Hour}}
	var err error
	f.store, err = sqlite.Open(context.Background(), f.path, f.options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	f.upstream.store = f.store
	input := domain.ModelNodeInput{ID: "tk-integration", ServiceURL: f.upstream.server.URL, ProtocolVersion: domain.ProtocolTK2SD, APIKeyCiphertext: []byte{1}, APIKeyNonce: []byte{2}, APIKeyFingerprint: "test-fingerprint", Enabled: true, MaxConcurrency: 2, PollInterval: time.Second, RequestTimeout: 3 * time.Second}
	inventory := &routing.Inventory{Store: f.store, HTTPClient: f.upstream.server.Client()}
	catalog, err := inventory.Preview(context.Background(), input, integrationNodeKey)
	if err != nil {
		t.Fatal(err)
	}
	wantModes := 3
	if f.upstream.model != integrationModel {
		wantModes = 1
	}
	if len(catalog.Items) != 1 || len(catalog.Items[0].Capabilities.Modes) != wantModes || catalog.Source != "discovered" {
		t.Fatal("real discovery was not normalized", catalog)
	}
	input.ModelCatalog = &catalog
	f.node, err = f.store.CreateModelNode(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := f.store.ListProfiles(context.Background())
	if err != nil || len(profiles) != 0 {
		t.Fatal("fixture must not have H3 profiles", err)
	}
	f.wire()
	return f
}

func (f *integrationFixture) wire() {
	f.t.Helper()
	base, err := url.Parse(f.upstream.server.URL)
	if err != nil {
		f.t.Fatal(err)
	}
	logger := f.logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	client := tk2sd.NewClient(base, integrationNodeKey, &http.Client{Timeout: 3 * time.Second}, 1<<20).WithLogger(logger)
	f.processor = &remote.Processor{Store: f.store, Client: client, Inputs: &remote.InputMaterializer{Store: f.store, Root: f.root, Timeout: 3 * time.Second}, NodeID: f.node.ID, NodeVersion: f.node.Version, NodeURL: base, Capacity: 2, PollInterval: time.Second, Logger: logger}
	results := &remote.ResultAccess{Store: f.store, Resolve: func(_ context.Context, nodeID string) (remote.ResultClient, *url.URL, error) {
		if nodeID != f.node.ID {
			return nil, nil, domain.ErrNodeNotFound
		}
		return client, base, nil
	}}
	f.handler = v2.NewHandler(v2.Dependencies{Store: f.store, Routing: &routing.Service{Store: f.store, Healthy: func(string) bool { return true }}, RemoteResults: results, InputSpooler: inputspool.New(f.root), ActiveProfiles: f.store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}, {ID: "owner-b", Key: "key-b", Enabled: true}}, Logger: logger})
}

func (f *integrationFixture) request(method, path, key string, body []byte) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+key)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func integrationBody(t *testing.T, media bool) []byte {
	t.Helper()
	r := domain.GenerationRequest{Model: integrationModel, Duration: 5, Content: []domain.GenerationContent{{Type: "text", Text: "Local integration fixture"}}}
	if media {
		var pngBytes bytes.Buffer
		if err := png.Encode(&pngBytes, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
			t.Fatal(err)
		}
		r.Content = append(r.Content, domain.GenerationContent{Type: "image_url", ImageURL: &domain.MediaURL{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes.Bytes())}})
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (f *integrationFixture) create(key string, body []byte) string {
	f.t.Helper()
	w := f.request(http.MethodPost, "/v2/video_generation", key, body)
	if w.Code != 200 {
		f.t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		ID string `json:"task_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.ID == "" {
		f.t.Fatal("missing task id", err)
	}
	return response.ID
}

func (f *integrationFixture) task(id string) domain.Task {
	f.t.Helper()
	task, err := f.store.Get(context.Background(), "owner-a", id)
	if err != nil {
		f.t.Fatal(err)
	}
	return task
}
func (f *integrationFixture) publicTask(id string) v2.TaskResponse {
	f.t.Helper()
	w := f.request(http.MethodGet, "/v2/query/video_generation/"+id, "key-a", nil)
	if w.Code != 200 {
		f.t.Fatalf("query: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Task v2.TaskResponse `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		f.t.Fatal(err)
	}
	return got.Task
}
func (f *integrationFixture) list(key, status string) ([]v2.TaskResponse, int) {
	f.t.Helper()
	w := f.request(http.MethodGet, "/v2/query/video_generation?filter.status="+status, key, nil)
	if w.Code != 200 {
		f.t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Items []v2.TaskResponse `json:"items"`
		Total int               `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		f.t.Fatal(err)
	}
	return got.Items, got.Total
}

func TestIntegrationBase64V2LifecycleWithoutH3Profile(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	id := f.create("key-a", integrationBody(t, true))
	task := f.task(id)
	if task.ProtocolVersion != domain.ProtocolTK2SD || task.ProfileID != "" || (task.ConfigSnapshotJSON != "" && task.ConfigSnapshotJSON != "{}") || !strings.Contains(task.RequestJSON, "proxy-input://") || strings.Contains(task.RequestJSON, "base64,") {
		t.Fatal("task did not take direct spooled route", task.ProtocolVersion, task.ProfileID, task.ConfigSnapshotJSON)
	}
	files, err := f.store.ListInputSpoolFiles(ctx, id)
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	if _, err = os.Stat(filepath.Join(f.root, filepath.FromSlash(files[0].RelativePath))); err != nil {
		t.Fatal("V2 did not spool actual input", err)
	}
	if err = f.processor.ProcessOne(ctx); !errors.Is(err, domain.ErrRemotePending) {
		t.Fatal(err)
	}
	queued := f.publicTask(id)
	items, total := f.list("key-a", "queued")
	if queued.Status != domain.V2Queued || queued.Duration != nil || queued.Usage != nil || total != 1 || len(items) != 1 || items[0].ID != id || items[0].Status != domain.V2Queued {
		t.Fatal("upstream queued missing from V2", queued, total, items)
	}
	if _, total = f.list("key-a", "running"); total != 0 {
		t.Fatal("queued remote task leaked into running filter")
	}
	f.upstream.setStatus(id, "succeeded")
	if err = f.processor.ProcessTask(ctx, f.task(id)); err != nil {
		t.Fatal(err)
	}
	actual := f.publicTask(id)
	run, err := f.store.GetRemoteRun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	wantURL := f.upstream.server.URL + "/media/tasks/" + run.UpstreamTaskID + "?expires=9999999999&signature=local-signed"
	if actual.Status != domain.V2Succeeded || actual.Duration == nil || *actual.Duration != 5.062 || actual.Content == nil || actual.Content.URL != wantURL || actual.Ratio == nil || *actual.Ratio != "16:9" || actual.Resolution == nil || *actual.Resolution != "720p" || actual.Usage != nil {
		t.Fatalf("V2 actual result: %+v", actual)
	}
	if run.Phase != domain.RemoteTerminal || f.task(id).ResultInternalURL != wantURL || f.task(id).UpstreamSlotActive {
		t.Fatal("terminal evidence not stored")
	}
	if _, total = f.list("key-a", "succeeded"); total != 1 {
		t.Fatal("success missing from list")
	}
	f.upstream.mu.Lock()
	defer f.upstream.mu.Unlock()
	if f.upstream.modelCalls != 1 || f.upstream.uploadCalls != 1 || len(f.upstream.jobs) != 1 || len(f.upstream.submitKeys) != 1 || f.upstream.metadataCalls != 1 {
		t.Fatal("unexpected lifecycle calls")
	}
}

func TestIntegrationLostSubmitResponseSurvivesSQLiteReopen(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	id := f.create("key-a", integrationBody(t, true))
	f.upstream.loseSubmitResponse = true
	if err := f.processor.ProcessOne(ctx); !errors.Is(err, domain.ErrRemotePending) {
		t.Fatal(err)
	}
	before, err := f.store.GetRemoteRun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if before.Phase != domain.RemoteSubmitIntent || before.UpstreamTaskID != "" || before.SubmitAttempts != 1 || !f.task(id).UpstreamSlotActive {
		t.Fatal("lost response discarded intent", before)
	}
	if err = f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = sqlite.Open(ctx, f.path, f.options)
	if err != nil {
		t.Fatal(err)
	}
	f.upstream.store = f.store
	f.wire()
	f.upstream.setStatus(id, "succeeded")
	if err = f.processor.ProcessTask(ctx, f.task(id)); err != nil {
		t.Fatal(err)
	}
	after, err := f.store.GetRemoteRun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if before.SubmissionKey != after.SubmissionKey || before.RequestBodyJSON != after.RequestBodyJSON || after.SubmitAttempts != 2 || f.publicTask(id).Status != domain.V2Succeeded {
		t.Fatal("restart changed submission evidence")
	}
	f.upstream.mu.Lock()
	defer f.upstream.mu.Unlock()
	if len(f.upstream.jobs) != 1 || len(f.upstream.submitKeys) != 2 || f.upstream.uploadCalls != 1 || f.upstream.submitKeys[0] != f.upstream.submitKeys[1] || f.upstream.submitBodies[0] != f.upstream.submitBodies[1] {
		t.Fatal("recovery created extra job or asset")
	}
}

func TestIntegrationOwnerIsolationAndQueuedCancelConfirmation(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	id := f.create("key-a", integrationBody(t, false))
	if err := f.processor.ProcessOne(ctx); !errors.Is(err, domain.ErrRemotePending) {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		path := "/v2/video_generation/" + id
		if method == http.MethodGet {
			path = "/v2/query/video_generation/" + id
		}
		w := f.request(method, path, "key-b", nil)
		if w.Code != 400 {
			t.Fatalf("foreign owner %s: %d %s", method, w.Code, w.Body.String())
		}
	}
	if items, total := f.list("key-b", "queued"); len(items) != 0 || total != 0 {
		t.Fatal("foreign task in owner list")
	}
	run, err := f.store.GetRemoteRun(ctx, id)
	if err != nil || run.CancelState != "none" {
		t.Fatal("foreign delete changed cancel intent", err)
	}
	w := f.request(http.MethodDelete, "/v2/video_generation/"+id, "key-a", nil)
	if w.Code != 202 {
		t.Fatalf("cancel request %d %s", w.Code, w.Body.String())
	}
	if f.publicTask(id).Status == domain.V2Cancelled || !f.task(id).UpstreamSlotActive {
		t.Fatal("cancel reported before remote confirmation")
	}
	started, release := make(chan struct{}, 1), make(chan struct{})
	f.upstream.mu.Lock()
	f.upstream.cancelStarted = started
	f.upstream.blockCancel = release
	f.upstream.mu.Unlock()
	done := make(chan error, 1)
	task := f.task(id)
	go func() { done <- f.processor.ProcessTask(ctx, task) }()
	select {
	case <-started:
	case <-time.After(4 * time.Second):
		close(release)
		<-done
		t.Fatal("upstream DELETE not called")
	}
	if f.publicTask(id).Status == domain.V2Cancelled || !f.task(id).UpstreamSlotActive {
		t.Error("cancel completed while upstream DELETE pending")
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if f.publicTask(id).Status != domain.V2Cancelled || f.task(id).UpstreamSlotActive {
		t.Fatal("confirmed remote cancel not committed")
	}
	if items, total := f.list("key-a", "cancelled"); total != 1 || len(items) != 1 || items[0].ID != id {
		t.Fatal("cancelled filter mismatch")
	}
	f.upstream.mu.Lock()
	defer f.upstream.mu.Unlock()
	if f.upstream.cancelCalls != 1 || len(f.upstream.jobs) != 1 || len(f.upstream.submitKeys) != 1 {
		t.Fatal("foreign request or cancel duplicated upstream operations")
	}
}

func TestIntegrationFrozenProtocolCannotBeClaimedByOtherExecutor(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	tkID := f.create("key-a", integrationBody(t, false))
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected request to other protocol")
		w.WriteHeader(500)
	}))
	defer other.Close()
	capability := protocol.BuiltinCapability(domain.ProtocolOfficial)
	node, err := f.store.CreateModelNode(ctx, domain.ModelNodeInput{ID: "official-integration", ServiceURL: other.URL, ProtocolVersion: domain.ProtocolOfficial, APIKeyCiphertext: []byte{1}, APIKeyNonce: []byte{2}, APIKeyFingerprint: "other-test-key", Enabled: true, MaxConcurrency: 1, PollInterval: time.Second, RequestTimeout: 3 * time.Second, ModelCatalog: &domain.ModelCatalog{Source: "manual", Status: "ready", LastSuccessAt: time.Now().Unix(), Items: []domain.NodeModel{{ModelID: integrationModel, Enabled: true, Present: true, Capabilities: capability}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ClaimNextOfficial(ctx, node.ID, node.Version, 1); !errors.Is(err, domain.ErrQueueEmpty) {
		t.Fatal("official executor claimed frozen tk2sd task", err)
	}
	w := f.request(http.MethodPost, "/v2/video_generation", "key-a", integrationBody(t, false))
	if w.Code != 409 {
		t.Fatalf("ambiguous protocol accepted: %d %s", w.Code, w.Body.String())
	}
	route, revision, err := f.store.GetModelRoute(ctx, integrationModel)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.store.BindModelRoute(ctx, integrationModel, domain.ProtocolOfficial, "integration", route.Version, revision); err != nil {
		t.Fatal(err)
	}
	request := domain.GenerationRequest{Model: integrationModel, Duration: 5, Resolution: "768P", Ratio: "16:9", Content: []domain.GenerationContent{{Type: "text", Text: "Official protocol fixture"}}}
	body, _ := json.Marshal(request)
	officialID := f.create("key-a", body)
	if f.task(tkID).ProtocolVersion != domain.ProtocolTK2SD || f.task(officialID).ProtocolVersion != domain.ProtocolOfficial {
		t.Fatal("binding rewrote existing protocol")
	}
	if err = f.processor.ProcessOne(ctx); !errors.Is(err, domain.ErrRemotePending) {
		t.Fatal(err)
	}
	f.upstream.setStatus(tkID, "succeeded")
	if err = f.processor.ProcessTask(ctx, f.task(tkID)); err != nil {
		t.Fatal(err)
	}
	if err = f.processor.ProcessOne(ctx); !errors.Is(err, domain.ErrQueueEmpty) {
		t.Fatal("tk2sd executor crossed protocol after route change", err)
	}
	claimed, err := f.store.ClaimNextOfficial(ctx, node.ID, node.Version, 1)
	if err != nil || claimed.TaskID != officialID {
		t.Fatal("official task not claimed by its executor", claimed.TaskID, err)
	}
	f.upstream.mu.Lock()
	defer f.upstream.mu.Unlock()
	if len(f.upstream.jobs) != 1 || len(f.upstream.submitKeys) != 1 || !strings.HasSuffix(f.upstream.submitKeys[0], ":"+tkID) {
		t.Fatal("cross-protocol submission reached tk2sd")
	}
}

func TestIntegrationV2RecoversActualMetadataWithoutRegeneration(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	id := f.create("key-a", integrationBody(t, false))
	if err := f.processor.ProcessOne(ctx); !errors.Is(err, domain.ErrRemotePending) {
		t.Fatal(err)
	}
	f.upstream.setStatus(id, "succeeded")
	f.upstream.mu.Lock()
	f.upstream.metadataUnavailable = true
	f.upstream.mu.Unlock()
	if err := f.processor.ProcessTask(ctx, f.task(id)); err != nil {
		t.Fatal(err)
	}
	before := f.publicTask(id)
	if before.Status != domain.V2Succeeded || before.Duration != nil || before.Ratio != nil || before.Resolution != nil || before.Usage != nil || before.Content == nil {
		t.Fatal("unavailable metadata was invented", before)
	}
	f.upstream.mu.Lock()
	f.upstream.metadataUnavailable = false
	f.upstream.mu.Unlock()
	after := f.publicTask(id)
	if after.Duration == nil || *after.Duration != 5.062 || after.Content == nil || after.Content.URL != before.Content.URL {
		t.Fatal("read did not recover actual metadata", after)
	}
	if f.task(id).MetadataStatus != "ready" {
		t.Fatal("metadata recovery not persisted")
	}
	f.upstream.mu.Lock()
	defer f.upstream.mu.Unlock()
	if len(f.upstream.jobs) != 1 || len(f.upstream.submitKeys) != 1 || f.upstream.uploadCalls != 0 {
		t.Fatal("metadata recovery regenerated")
	}
}
