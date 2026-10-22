package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/logsafe"
	"minimax-h3-tc/internal/netguard"
	"minimax-h3-tc/internal/upstream/tk2sd"
)

func TestInputDiagnosticsDistinguishFailures(t *testing.T) {
	for _, test := range []struct {
		name, source, stage, reason, code, message string
		status                                     int
		object, corrupt, invalidMedia, missing     bool
		uploadErr                                  error
	}{
		{name: "object_forbidden", source: "object_storage", stage: "source_download", reason: "http_status", code: "tk2sd_input_source_http_error", message: "对象存储", status: 403, object: true},
		{name: "url_not_found", source: "media_url", stage: "source_download", reason: "http_status", code: "tk2sd_input_source_http_error", message: "素材 URL", status: 404},
		{name: "object_server_error", source: "object_storage", stage: "source_download", reason: "http_status", code: "tk2sd_input_source_http_error", message: "HTTP 503", status: 503, object: true},
		{name: "media_format", source: "media_url", stage: "media_detect", reason: "unsupported_media", code: "tk2sd_input_media_invalid", message: "格式", invalidMedia: true},
		{name: "object_checksum", source: "object_storage", stage: "integrity_check", reason: "checksum_mismatch", code: "tk2sd_input_integrity_mismatch", message: "SHA256", object: true, corrupt: true},
		{name: "missing_local_file", source: "local_spool", stage: "local_file", reason: "open_failed", code: "tk2sd_input_local_file_invalid", message: "本地暂存", missing: true},
		{name: "upstream_rejected", source: "upstream", stage: "asset_upload", reason: "http_rejected", code: "tk2sd_asset_upload_rejected", message: "上游素材上传", status: 422, uploadErr: &tk2sd.HTTPError{StatusCode: 422, Code: "secret-token", Message: "https://private.example/?signature=secret-token", Class: tk2sd.ErrorRejected}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, store, _, task, client := processorFixture(t)
			var logs bytes.Buffer
			p.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
			client.uploadErr = test.uploadErr
			raw := "http://media.example/private-input?signature=secret-token"
			payload := append([]byte(nil), pngInput...)
			if test.invalidMedia {
				payload = []byte("not an image: secret-token")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.status != 0 && test.uploadErr == nil {
					w.WriteHeader(test.status)
				}
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			p.Inputs.Guard = sourceGuard(server)
			if test.object || test.missing {
				digest := sha256.Sum256(pngInput)
				meta := domain.InputSpoolFile{ID: "input", TaskID: task.TaskID, ContentIndex: 1, ContentType: "image_url", Role: "reference_image", SourceKind: "data_uri", MediaType: "image/png", RelativePath: "secret-local.png", Extension: ".png", SizeBytes: int64(len(pngInput)), SHA256: hex.EncodeToString(digest[:])}
				if test.corrupt {
					meta.SHA256 = strings.Repeat("0", 64)
				}
				if test.object {
					meta.ObjectURL = raw
				} else {
					p.Inputs.Root = t.TempDir()
					raw = "proxy-input://" + task.TaskID + "/input"
				}
				p.Inputs.Store = inputRecords{AssetStore: store, files: []domain.InputSpoolFile{meta}}
			}
			task.RequestJSON = imageRequest(raw)
			if err := p.ProcessTask(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			got, err := store.Get(context.Background(), "owner", task.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != domain.StatusFailed || got.ErrorCode != test.code || !strings.Contains(got.ErrorMessage, test.message) || !strings.Contains(got.ErrorMessage, "content[1]") {
				t.Errorf("task status=%s code=%s message=%s", got.Status, got.ErrorCode, got.ErrorMessage)
			}
			if len(client.keys) != 0 {
				t.Error("invalid input submitted generation")
			}
			failure := inputFailureLog(t, logs.String())
			for key, want := range map[string]any{"task_id": task.TaskID, "node_id": "node", "content_index": float64(1), "content_type": "image_url", "source": test.source, "stage": test.stage, "reason": test.reason, "error_code": test.code, "http_status": float64(test.status), "retryable": false} {
				if failure[key] != want {
					t.Errorf("log %s=%v want %v", key, failure[key], want)
				}
			}
			for _, secret := range []string{"media.example", "signature=", "secret-token", "secret-local", "private.example"} {
				if strings.Contains(logs.String()+got.ErrorMessage, secret) {
					t.Errorf("sensitive input leaked: %s", secret)
				}
			}
		})
	}
}

func inputFailureLog(t *testing.T, logs string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["event"] == "input_failed" {
			return record
		}
	}
	t.Fatalf("missing input failure log: %s", logs)
	return nil
}

func TestInputDiagnosticsTemporaryUploadRemainsPending(t *testing.T) {
	for _, uploadErr := range []error{errors.New("secret-token https://private.example"), &tk2sd.HTTPError{StatusCode: 503, Class: tk2sd.ErrorTemporary}, &tk2sd.HTTPError{StatusCode: 401, Class: tk2sd.ErrorAuthentication}, tk2sd.ErrInvalidResponse} {
		p, store, _, task, client := processorFixture(t)
		var logs bytes.Buffer
		p.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
		client.uploadErr = uploadErr
		task.RequestJSON = imageRequest("data:image/png;base64," + base64.StdEncoding.EncodeToString(pngInput))
		if err := p.ProcessTask(context.Background(), task); !errors.Is(err, domain.ErrRemotePending) {
			t.Fatal(err)
		}
		got, err := store.Get(context.Background(), "owner", task.TaskID)
		if err != nil || got.Status == domain.StatusFailed || len(client.keys) != 0 {
			t.Fatal("temporary error completed or submitted task", err)
		}
		failure := inputFailureLog(t, logs.String())
		if failure["source"] != "upstream" || failure["stage"] != "asset_upload" || failure["retryable"] != true {
			t.Fatal(failure)
		}
		if strings.Contains(logs.String(), "secret-token") || strings.Contains(logs.String(), "private.example") || strings.Contains(logs.String(), "base64") {
			t.Fatal("unsafe temporary upload diagnostics")
		}
	}
}

func TestInputDiagnosticsRequestValidationIsSpecific(t *testing.T) {
	p, store, _, task, client := processorFixture(t)
	var logs bytes.Buffer
	p.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	task.RequestJSON = `{"model":"seed-model","content":[{"type":"text","text":"private-prompt"}],"duration":99}`
	if err := p.ProcessTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(context.Background(), "owner", task.TaskID)
	if got.ErrorCode != "tk2sd_input_request_invalid" || !strings.Contains(got.ErrorMessage, "4–15") || len(client.keys) != 0 {
		t.Fatalf("request diagnostics: %s %s", got.ErrorCode, got.ErrorMessage)
	}
	if inputFailureLog(t, logs.String())["stage"] != "request_validate" || strings.Contains(logs.String(), "private-prompt") {
		t.Fatal("missing or unsafe request validation log")
	}
}

func TestInputDiagnosticsInvalidFieldsAreNotLogged(t *testing.T) {
	for _, field := range []string{"type", "role"} {
		p, store, _, task, _ := processorFixture(t)
		var logs bytes.Buffer
		p.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
		var request map[string]any
		if err := json.Unmarshal([]byte(imageRequest("data:image/png;base64,private-data")), &request); err != nil {
			t.Fatal(err)
		}
		request["content"].([]any)[1].(map[string]any)[field] = "private-prompt https://private.example/?signature=secret"
		data, _ := json.Marshal(request)
		task.RequestJSON = string(data)
		if err := p.ProcessTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		got, _ := store.Get(context.Background(), "owner", task.TaskID)
		for _, sensitive := range []string{"private-prompt", "private.example", "signature=", "private-data"} {
			if strings.Contains(logs.String()+got.ErrorMessage, sensitive) {
				t.Errorf("invalid %s leaked %s", field, sensitive)
			}
		}
	}
}

func TestInputDiagnosticsDownloadTransportKeepsRetryAndCause(t *testing.T) {
	p, store, _, task, client := processorFixture(t)
	var logs bytes.Buffer
	p.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	p.Inputs.Guard = netguard.New(netguard.Options{Resolver: publicResolver{}, Dialer: func(context.Context, string, string) (net.Conn, error) {
		return nil, context.DeadlineExceeded
	}})
	task.RequestJSON = imageRequest("http://media.example/input?token=private-token")
	if err := p.ProcessTask(context.Background(), task); !errors.Is(err, domain.ErrRemotePending) {
		t.Fatal(err)
	}
	got, _ := store.Get(context.Background(), "owner", task.TaskID)
	if got.Status == domain.StatusFailed || len(client.keys) != 0 || client.uploads != 0 {
		t.Fatal("temporary download error failed or submitted task")
	}
	failure := inputFailureLog(t, logs.String())
	if failure["source"] != "media_url" || failure["stage"] != "source_download" || failure["retryable"] != true || failure["cause_class"] != "timeout" {
		t.Fatal(failure)
	}
	_, err := p.Inputs.Prepare(context.Background(), task, domain.RemoteRun{NodeID: "node"}, client)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("download cause was lost", err)
	}
}

func TestInputDiagnosticsSourceSizeLimitIsExplicit(t *testing.T) {
	p, store, _, task, client := processorFixture(t)
	var logs bytes.Buffer
	p.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "999999999")
		w.WriteHeader(200)
	}))
	defer server.Close()
	p.Inputs.Guard = sourceGuard(server)
	task.RequestJSON = imageRequest("http://media.example/oversize")
	if err := p.ProcessTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(context.Background(), "owner", task.TaskID)
	if !strings.Contains(got.ErrorMessage, "Content-Length") || client.uploads != 0 {
		t.Fatal("missing source size diagnostics", got.ErrorMessage)
	}
	if inputFailureLog(t, logs.String())["reason"] != "size_exceeded" {
		t.Fatal("missing size failure reason")
	}
}

type diagnosticStore struct {
	AssetStore
	stage string
}

func (s diagnosticStore) ListInputSpoolFiles(ctx context.Context, task string) ([]domain.InputSpoolFile, error) {
	if s.stage == "spool_lookup" {
		return nil, errors.New("private database path")
	}
	return s.AssetStore.ListInputSpoolFiles(ctx, task)
}

func (s diagnosticStore) GetRemoteAsset(ctx context.Context, task, node string, index int) (domain.NodeAsset, error) {
	if s.stage == "asset_lookup" {
		return domain.NodeAsset{}, errors.New("private database path")
	}
	return s.AssetStore.GetRemoteAsset(ctx, task, node, index)
}

func (s diagnosticStore) SaveRemoteAsset(ctx context.Context, run domain.RemoteRun, asset domain.NodeAsset) error {
	if s.stage == "asset_save" {
		return errors.New("private database path")
	}
	return s.AssetStore.SaveRemoteAsset(ctx, run, asset)
}

func TestInputDiagnosticsPersistenceFailureRemainsPending(t *testing.T) {
	for _, stage := range []string{"spool_lookup", "asset_lookup", "asset_save"} {
		t.Run(stage, func(t *testing.T) {
			p, store, _, task, client := processorFixture(t)
			var logs bytes.Buffer
			p.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
			p.Inputs.Store = diagnosticStore{AssetStore: store, stage: stage}
			task.RequestJSON = imageRequest("data:image/png;base64," + base64.StdEncoding.EncodeToString(pngInput))
			if err := p.ProcessTask(context.Background(), task); !errors.Is(err, domain.ErrRemotePending) {
				t.Fatal(err)
			}
			failure := inputFailureLog(t, logs.String())
			if failure["stage"] != stage || failure["source"] != "proxy_store" || failure["retryable"] != true || len(client.keys) != 0 {
				t.Fatal(failure)
			}
			if strings.Contains(logs.String(), "private database path") {
				t.Fatal("database details leaked")
			}
		})
	}
}

func TestInputDiagnosticsSuccessAndReuseLogBoundaries(t *testing.T) {
	p, store, _, task, client := processorFixture(t)
	var logs bytes.Buffer
	p.Inputs.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	task.RequestJSON = imageRequest("data:image/png;base64," + base64.StdEncoding.EncodeToString(pngInput))
	ctx := logsafe.WithTask(context.Background(), task.TaskID, "node")
	run, err := store.AcquireRemoteLease(ctx, task.TaskID, "node", "diagnostic-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := p.Inputs.Prepare(ctx, task, run, client); err != nil {
			t.Fatal(err)
		}
	}
	if client.uploads != 1 {
		t.Fatal("reuse caused duplicate upload", client.uploads)
	}
	for _, event := range []string{"source_open_started", "source_opened", "asset_upload_started", "asset_uploaded", "asset_reused"} {
		if !strings.Contains(logs.String(), `"event":"`+event+`"`) {
			t.Error("missing boundary log", event)
		}
	}
	if strings.Contains(logs.String(), "base64") || strings.Contains(logs.String(), base64.StdEncoding.EncodeToString(pngInput)) {
		t.Fatal("inline input leaked")
	}
}

type failingSourceClient struct{ uploadErr error }

func (c failingSourceClient) Upload(_ context.Context, _, _ string, source io.Reader) (tk2sd.Asset, error) {
	_, err := io.Copy(io.Discard, source)
	if c.uploadErr != nil {
		err = c.uploadErr
	}
	return tk2sd.Asset{}, err
}

func TestInputDiagnosticsTruncatedSourceIsNotMisattributedToUpstream(t *testing.T) {
	for _, mode := range []string{"reader", "real_client", "early_http_rejection"} {
		t.Run(mode, func(t *testing.T) {
			p, _, _, task, _ := processorFixture(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "4096")
				_, _ = w.Write(append(append([]byte(nil), pngInput...), bytes.Repeat([]byte("x"), 1024)...))
			}))
			defer server.Close()
			p.Inputs.Guard = sourceGuard(server)
			p.Inputs.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
			task.RequestJSON = imageRequest("http://media.example/truncated")
			var client AssetClient = failingSourceClient{}
			if mode == "real_client" {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.WriteHeader(http.StatusBadRequest)
				}))
				defer upstream.Close()
				base, _ := url.Parse(upstream.URL)
				client = tk2sd.NewClient(base, "private-key", &http.Client{Timeout: time.Second}, 1<<20).WithLogger(slog.New(slog.NewJSONHandler(io.Discard, nil)))
			} else if mode == "early_http_rejection" {
				client = failingSourceClient{uploadErr: &tk2sd.HTTPError{StatusCode: 422, Class: tk2sd.ErrorRejected}}
			}
			_, err := p.Inputs.Prepare(context.Background(), task, domain.RemoteRun{NodeID: "node"}, client)
			var detail *inputError
			if mode == "early_http_rejection" {
				if !errors.As(err, &detail) || detail.Source != "upstream" || detail.Stage != "asset_upload" || detail.HTTPStatus != 422 || !rejected(err) {
					t.Fatal("upstream rejection lost status or was misattributed", err)
				}
				return
			}
			if !errors.As(err, &detail) || detail.Source != "media_url" || detail.Stage != "source_read" || !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatal("source failure lost origin or cause", err)
			}
		})
	}
}
