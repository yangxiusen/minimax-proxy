package remote

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/store/sqlite"
	"minimax-h3-tc/internal/upstream/tk2sd"
)

const upstreamID = "0123456789abcdef0123456789abcdef"
const assetID = "abcdef0123456789abcdef0123456789"

type remoteFake struct {
	mu                                                  sync.Mutex
	keys, bodies                                        []string
	submitErr, queryErr, cancelErr, metadataErr         error
	query                                               tk2sd.Task
	meta                                                tk2sd.Metadata
	assetDuration                                       float64
	uploadErr                                           error
	uploads, queries, cancels, metadataCalls, downloads int
	onSubmit                                            func()
	onUpload                                            func()
	onCancel                                            func()
	onQuery                                             func()
}

func newFake() *remoteFake {
	return &remoteFake{query: tk2sd.Task{ID: upstreamID, Model: "seed-model", Status: "queued", VideoURL: "http://node.example/media/tasks/" + upstreamID + "?expires=9999999999&signature=opaque"}, meta: tk2sd.Metadata{Duration: 5.062, Width: 1280, Height: 720}, assetDuration: 3}
}
func (f *remoteFake) Submit(_ context.Context, b []byte, k string) (string, error) {
	f.mu.Lock()
	f.keys = append(f.keys, k)
	f.bodies = append(f.bodies, string(b))
	err := f.submitErr
	hook := f.onSubmit
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return upstreamID, err
}
func (f *remoteFake) Query(context.Context, string) (tk2sd.Task, error) {
	f.mu.Lock()
	f.queries++
	result, err, hook := f.query, f.queryErr, f.onQuery
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return result, err
}
func (f *remoteFake) Cancel(context.Context, string) error {
	f.mu.Lock()
	f.cancels++
	err, hook := f.cancelErr, f.onCancel
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}
func (f *remoteFake) Metadata(context.Context, string) (tk2sd.Metadata, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metadataCalls++
	return f.meta, f.metadataErr
}
func (f *remoteFake) Download(context.Context, string, string, int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloads++
	return 10, nil
}
func (f *remoteFake) Upload(_ context.Context, name, mime string, r io.Reader) (tk2sd.Asset, error) {
	f.mu.Lock()
	f.uploads++
	hook, uploadErr, duration := f.onUpload, f.uploadErr, f.assetDuration
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if uploadErr != nil {
		return tk2sd.Asset{}, uploadErr
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return tk2sd.Asset{}, err
	}
	kind, _, _ := strings.Cut(mime, "/")
	if kind == "image" {
		duration = 0
	}
	return tk2sd.Asset{ID: assetID, URL: "asset://" + assetID, Kind: kind, Filename: name, Width: 1280, Height: 720, Duration: duration, Size: int64(len(b))}, nil
}

func processorFixture(t *testing.T) (*Processor, *sqlite.Store, *sql.DB, domain.Task, *remoteFake) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "remote.db")
	s, err := sqlite.Open(context.Background(), path, sqlite.Options{PerKeyLimit: 20, GlobalLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO model_service_nodes(id,service_url,protocol_version,api_key_ciphertext,api_key_nonce,api_key_fingerprint,api_key_id,base_url,jobs_base_url,public_base_url,created_at,updated_at) VALUES('node','http://node.example','tk2sd-v1',X'01',X'02','fingerprint','key','','','',1,1)`)
	exec(`INSERT INTO node_model_catalogs(node_id,source,connection_fingerprint,last_status,last_success_at,valid_until,created_at,updated_at) VALUES('node','discovered',?,'ready',1,9999999999,1,1)`, sqlite.CatalogFingerprint(domain.ModelNodeInput{ProtocolVersion: domain.ProtocolTK2SD, ServiceURL: "http://node.example", APIKeyFingerprint: "fingerprint"}))
	exec(`INSERT INTO node_models(node_id,model_id,enabled,present,capabilities_json,verified,created_at,updated_at) VALUES('node','seed-model',1,1,'{"schema_version":1,"modes":[{"scenario":"t2va","durations":[5]}],"max_media":12}',1,1,1)`)
	exec(`INSERT INTO video_tasks(task_id,api_key_id,model,scenario,request_json,request_hash,status,resolution,duration,protocol_version,route_state,routing_snapshot_json,callback_url_ciphertext,callback_url_nonce,created_at,updated_at,expires_at) VALUES('task','owner','seed-model','t2va','{"model":"seed-model","content":[{"type":"text","text":"test"}],"duration":5,"resolution":"ignored","callback_url":"https://private.example"}','hash','queued_open','',5,'tk2sd-v1','ready','{"schema_version":1,"model":"seed-model","protocol_version":"tk2sd-v1","requirements":{"scenario":"t2va","duration":5}}',X'01',X'02',1,1,9999999999)`)
	task, err := s.ClaimNextRemote(context.Background(), "node", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	f := newFake()
	base, _ := url.Parse("http://node.example")
	p := &Processor{Store: s, Client: f, Inputs: &InputMaterializer{Store: s}, NodeID: "node", NodeURL: base, NodeVersion: 1, Capacity: 1}
	return p, s, db, task, f
}

func TestSubmitUnknownRecoveryUsesPersistedBodyAndNamespace(t *testing.T) {
	p, s, db, task, f := processorFixture(t)
	ctx := context.Background()
	f.submitErr = errors.New("connection lost after create")
	if err := p.ProcessTask(ctx, task); !errors.Is(err, domain.ErrRemotePending) {
		t.Fatal(err)
	}
	r, err := s.GetRemoteRun(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Phase != domain.RemoteSubmitIntent || r.SubmitAttempts != 1 || r.RequestBodyJSON == "" || r.LeaseToken != "" {
		t.Fatalf("unknown run %+v", r)
	}
	var instance string
	if err = db.QueryRow(`SELECT instance_id FROM routing_state`).Scan(&instance); err != nil {
		t.Fatal(err)
	}
	if r.SubmissionKey != "proxy:"+instance+":task" {
		t.Fatal(r.SubmissionKey)
	}
	if strings.Contains(r.RequestBodyJSON, "resolution") || strings.Contains(r.RequestBodyJSON, "callback") {
		t.Fatal("non-protocol fields sent")
	}
	// 模拟重启与凭据修复：执行器重建，客户端更换，原始请求不得重新装配。
	f2 := newFake()
	f2.query.Status = "succeeded"
	p2 := *p
	p2.Client = f2
	task.RequestJSON = `{"model":"changed"}`
	if err = p2.ProcessTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if f2.keys[0] != f.keys[0] || f2.bodies[0] != f.bodies[0] {
		t.Fatal("recovery changed submit identity")
	}
	got, err := s.Get(ctx, "owner", task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusSucceeded || got.UpstreamSlotActive || got.MetadataStatus != "ready" || !strings.Contains(got.ResultMetadataJSON, "5.062") || got.UsageOutputSeconds != 0 {
		t.Fatalf("result %+v", got)
	}
	var callbacks int
	db.QueryRow(`SELECT COUNT(*) FROM callback_deliveries WHERE task_id='task'`).Scan(&callbacks)
	if callbacks != 1 {
		t.Fatal(callbacks)
	}
}

type failingBindStore struct {
	*sqlite.Store
	fail bool
}

type stolenLeaseStore struct {
	*sqlite.Store
	db *sql.DB
}

func (s *stolenLeaseStore) PrepareRemoteRequest(ctx context.Context, r domain.RemoteRun, body string) error {
	if err := s.Store.PrepareRemoteRequest(ctx, r, body); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE task_remote_runs SET lease_token='other-worker',lease_expires_at=9999999999 WHERE task_id=?`, r.TaskID)
	return err
}

func TestRemoteLeaseLostDuringPreparationCannotSubmit(t *testing.T) {
	p, s, db, task, f := processorFixture(t)
	p.Store = &stolenLeaseStore{Store: s, db: db}
	if err := p.ProcessTask(context.Background(), task); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatal("expected fenced worker", err)
	}
	if len(f.keys) != 0 {
		t.Fatal("old worker submitted with new owner's lease")
	}
}

func (s *failingBindStore) BindRemoteTask(ctx context.Context, r domain.RemoteRun, id string) error {
	if s.fail {
		s.fail = false
		return errors.New("crash before binding")
	}
	return s.Store.BindRemoteTask(ctx, r, id)
}

func TestSubmitRecoveryAfterIDReceivedBeforePersistence(t *testing.T) {
	p, s, _, task, f := processorFixture(t)
	ctx := context.Background()
	p.Store = &failingBindStore{Store: s, fail: true}
	if err := p.ProcessTask(ctx, task); err == nil {
		t.Fatal("expected binding failure")
	}
	if err := p.ProcessTask(ctx, task); !errors.Is(err, domain.ErrRemotePending) {
		t.Fatal(err)
	}
	if len(f.keys) != 2 || f.keys[0] != f.keys[1] || f.bodies[0] != f.bodies[1] {
		t.Fatal("lost idempotent recovery")
	}
}

func TestPollingFailureNeverResubmitsOrReleasesCapacity(t *testing.T) {
	p, s, _, task, f := processorFixture(t)
	ctx := context.Background()
	for _, pollErr := range []error{&tk2sd.HTTPError{StatusCode: 404}, &tk2sd.HTTPError{StatusCode: 401}, errors.New("timeout"), nil} {
		f.queryErr = pollErr
		f.query.Status = "unknown"
		if err := p.ProcessTask(ctx, task); !errors.Is(err, domain.ErrRemotePending) {
			t.Fatal(err)
		}
		got, _ := s.Get(ctx, "owner", task.TaskID)
		if !got.UpstreamSlotActive || got.Status != domain.StatusRunning {
			t.Fatalf("released unknown task %+v", got)
		}
	}
	if len(f.keys) != 1 {
		t.Fatalf("POST count %d", len(f.keys))
	}
}

func TestReplayRejectedRequiresReconciliation(t *testing.T) {
	for _, status := range []int{400, 409, 422} {
		t.Run(string(rune(status)), func(t *testing.T) {
			p, s, _, task, f := processorFixture(t)
			ctx := context.Background()
			f.submitErr = errors.New("lost")
			p.ProcessTask(ctx, task)
			f.submitErr = &tk2sd.HTTPError{StatusCode: status}
			if err := p.ProcessTask(ctx, task); !errors.Is(err, domain.ErrRemoteReconciliation) {
				t.Fatal(err)
			}
			r, _ := s.GetRemoteRun(ctx, task.TaskID)
			got, _ := s.Get(ctx, "owner", task.TaskID)
			if !r.ReconciliationRequired || !got.UpstreamSlotActive || r.Phase != domain.RemoteSubmitIntent {
				t.Fatalf("discarded ambiguous submission %+v %+v", r, got)
			}
		})
	}
}

func TestRemoteCancelBeforePrepareSendsNoGeneration(t *testing.T) {
	p, s, db, task, f := processorFixture(t)
	ctx := context.Background()
	if err := s.RequestRemoteCancel(ctx, task.TaskID); err != nil {
		t.Fatal(err)
	}
	if err := p.ProcessTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "owner", task.TaskID)
	if got.Status != domain.StatusCancelled || len(f.keys) != 0 || f.cancels != 0 {
		t.Fatal("cancel created generation")
	}
	if err := s.RequestRemoteCancel(ctx, task.TaskID); err != nil {
		t.Fatal(err)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM callback_deliveries`).Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
}

func TestRemoteCancelUnknownSubmitRecoversBeforeDelete(t *testing.T) {
	p, s, _, task, f := processorFixture(t)
	ctx := context.Background()
	f.submitErr = errors.New("unknown")
	p.ProcessTask(ctx, task)
	s.RequestRemoteCancel(ctx, task.TaskID)
	f.submitErr = nil
	if err := p.ProcessTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "owner", task.TaskID)
	if got.Status != domain.StatusCancelled || len(f.keys) != 2 || f.keys[0] != f.keys[1] || f.cancels != 1 {
		t.Fatalf("cancel unknown %+v", got)
	}
}

func TestRemoteCancelRaces(t *testing.T) {
	for _, state := range []string{"running", "cancelled", "succeeded", "failed"} {
		t.Run(state, func(t *testing.T) {
			p, s, db, task, f := processorFixture(t)
			ctx := context.Background()
			p.ProcessTask(ctx, task)
			s.RequestRemoteCancel(ctx, task.TaskID)
			f.cancelErr = &tk2sd.HTTPError{StatusCode: 409}
			f.onCancel = func() { f.query.Status = state }
			err := p.ProcessTask(ctx, task)
			got, _ := s.Get(ctx, "owner", task.TaskID)
			if state == "running" {
				if !errors.Is(err, domain.ErrRemotePending) || got.Status != domain.StatusRunning || !got.UpstreamSlotActive {
					t.Fatal(err, got.Status)
				}
				if err = s.RequestRemoteCancel(ctx, task.TaskID); !errors.Is(err, domain.ErrRemoteNotCancellable) {
					t.Fatal(err)
				}
			} else {
				if err != nil || string(got.Status) != state || got.UpstreamSlotActive {
					t.Fatal(err, got.Status)
				}
				var count int
				db.QueryRow(`SELECT COUNT(*) FROM callback_deliveries`).Scan(&count)
				if count != 1 {
					t.Fatal(count)
				}
			}
		})
	}
}

func TestRemoteCancelLostResponseConfirmedByQuery(t *testing.T) {
	p, s, _, task, f := processorFixture(t)
	ctx := context.Background()
	p.ProcessTask(ctx, task)
	s.RequestRemoteCancel(ctx, task.TaskID)
	f.cancelErr = errors.New("response lost")
	if err := p.ProcessTask(ctx, task); !errors.Is(err, domain.ErrRemotePending) {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "owner", task.TaskID)
	if !got.UpstreamSlotActive || got.Status == domain.StatusCancelled {
		t.Fatal("cancel guessed")
	}
	f.query.Status = "cancelled"
	if err := p.ProcessTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if len(f.keys) != 1 || f.cancels != 1 {
		t.Fatal("repeated generation or unnecessary DELETE")
	}
}

func TestMetadataFailureKeepsSuccessUnknownActualDuration(t *testing.T) {
	p, s, _, task, f := processorFixture(t)
	f.query.Status = "succeeded"
	f.metadataErr = errors.New("unavailable")
	if err := p.ProcessTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(context.Background(), "owner", task.TaskID)
	if got.Status != domain.StatusSucceeded || got.MetadataStatus != "pending" || got.ResultMetadataJSON != "" || got.UsageTotalSeconds != 0 {
		t.Fatalf("invented actual metadata %+v", got)
	}
}

func TestRemoteDeliveryEnqueuesOnlyAfterGeneration(t *testing.T) {
	p, s, db, task, f := processorFixture(t)
	task.DeliveryRequired = true
	db.Exec(`UPDATE video_tasks SET delivery_required=1 WHERE task_id='task'`)
	f.query.Status = "succeeded"
	if err := p.ProcessTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(context.Background(), "owner", task.TaskID)
	var jobs, callbacks int
	db.QueryRow(`SELECT COUNT(*) FROM result_upload_jobs WHERE status='pending' AND max_attempts=3`).Scan(&jobs)
	db.QueryRow(`SELECT COUNT(*) FROM callback_deliveries`).Scan(&callbacks)
	if got.Status != domain.StatusReconciling || got.UpstreamSlotActive || got.ResultPublicURL != "" || got.ResultInternalURL == "" || jobs != 1 || callbacks != 0 {
		t.Fatal(got.Status, jobs, callbacks)
	}
}

func TestActualMetadataUsesMediaDimensions(t *testing.T) {
	data, ratio, err := ActualMetadata(tk2sd.Metadata{Duration: 5.062, Width: 720, Height: 1280})
	if err != nil || ratio != "9:16" {
		t.Fatal(data, ratio, err)
	}
	var got map[string]any
	json.Unmarshal([]byte(data), &got)
	if got["resolution"] != "720p" || got["duration"] != 5.062 {
		t.Fatal(got)
	}
	data, _, _ = ActualMetadata(tk2sd.Metadata{Duration: 4, Width: 777, Height: 999})
	json.Unmarshal([]byte(data), &got)
	if got["resolution"] != nil {
		t.Fatal("invented resolution")
	}
}

func TestRemoteLeaseRenewalDuringSlowSubmit(t *testing.T) {
	p, s, db, task, f := processorFixture(t)
	p.LeaseDuration = 2 * time.Second
	f.onSubmit = func() {
		time.Sleep(1200 * time.Millisecond)
		var expires int64
		db.QueryRow(`SELECT lease_expires_at FROM task_remote_runs WHERE task_id='task'`).Scan(&expires)
		if expires <= time.Now().Unix() {
			t.Error("lease was not renewed")
		}
		if _, err := s.AcquireRemoteLease(context.Background(), task.TaskID, "node", "competitor", time.Minute); !errors.Is(err, domain.ErrStateConflict) {
			t.Error("concurrent claimant", err)
		}
	}
	if err := p.ProcessTask(context.Background(), task); !errors.Is(err, domain.ErrRemotePending) {
		t.Fatal(err)
	}
}
