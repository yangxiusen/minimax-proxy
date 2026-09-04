package manager

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/inputspool"
	monitorcache "minimax-h3-tc/internal/monitor"
)

type taskStoreStub struct {
	mu               sync.Mutex
	filter           domain.AdminTaskFilter
	items            []domain.AdminTaskSummary
	total            int
	err              error
	cancelledTask    string
	deletedTask      string
	cancelError      error
	deleteError      error
	detail           domain.AdminTaskDetail
	detailError      error
	locations        []domain.TaskArtifactLocation
	inputFile        domain.InputSpoolFile
	inputFileErr     error
	uploadJob        domain.ResultUploadJob
	uploadRetryError error
	uploadRetryTask  string
}

type managerSignerSpy struct {
	url, artifactID, ownerID string
	err                      error
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func (s *managerSignerSpy) SignURL(_ context.Context, artifactID, ownerID string) (string, error) {
	s.artifactID, s.ownerID = artifactID, ownerID
	return s.url, s.err
}

func (s *taskStoreStub) RequestAdminCancel(_ context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelledTask = taskID
	return s.cancelError
}

func (s *taskStoreStub) AdminDelete(_ context.Context, taskID string, _ ...domain.TaskArtifactLocation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletedTask = taskID
	return s.deleteError
}

func (s *taskStoreStub) GetAdminTaskDetail(_ context.Context, taskID string) (domain.AdminTaskDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.detail.Task.TaskID != taskID {
		return domain.AdminTaskDetail{}, domain.ErrTaskNotFound
	}
	return s.detail, s.detailError
}
func (s *taskStoreStub) ListTaskArtifactLocations(context.Context, string) ([]domain.TaskArtifactLocation, error) {
	return append([]domain.TaskArtifactLocation(nil), s.locations...), nil
}
func (s *taskStoreStub) EnsureTaskPurgeReady(context.Context, string) error {
	return nil
}
func (s *taskStoreStub) GetInputSpoolFile(_ context.Context, taskID, inputID string) (domain.InputSpoolFile, error) {
	if s.inputFileErr != nil {
		return domain.InputSpoolFile{}, s.inputFileErr
	}
	if s.inputFile.TaskID == taskID && s.inputFile.ID == inputID {
		return s.inputFile, nil
	}
	return domain.InputSpoolFile{}, domain.ErrTaskNotFound
}

func TestWebRoutesRedirectAuthenticateAndServeEmbeddedAssets(t *testing.T) {
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}})

	root := serve(h, http.MethodGet, "/manager", "", "", "", "192.0.2.10:1", false)
	if root.Code != http.StatusPermanentRedirect || root.Header().Get("Location") != "/manager/" {
		t.Fatalf("root status=%d location=%q", root.Code, root.Header().Get("Location"))
	}
	loginPage := serve(h, http.MethodGet, "/manager/login", "", "", "", "192.0.2.10:1", false)
	if loginPage.Code != http.StatusOK || !strings.Contains(loginPage.Body.String(), "登录") {
		t.Fatalf("login status=%d body=%q", loginPage.Code, loginPage.Body.String())
	}
	protected := serve(h, http.MethodGet, "/manager/", "", "", "", "192.0.2.10:1", false)
	if protected.Code != http.StatusSeeOther || protected.Header().Get("Location") != "/manager/login" {
		t.Fatalf("protected status=%d location=%q", protected.Code, protected.Header().Get("Location"))
	}

	cookie := login(t, h, "admin", "secret", "192.0.2.10:1")
	authorized := serve(h, http.MethodGet, "/manager/", "", "", cookie, "192.0.2.10:1", false)
	if authorized.Code != http.StatusOK || !strings.Contains(authorized.Body.String(), "私有服务监控") {
		t.Fatalf("authorized status=%d body=%q", authorized.Code, authorized.Body.String())
	}
	alreadyLoggedIn := serve(h, http.MethodGet, "/manager/login", "", "", cookie, "192.0.2.10:1", false)
	if alreadyLoggedIn.Code != http.StatusSeeOther || alreadyLoggedIn.Header().Get("Location") != "/manager/" {
		t.Fatalf("logged-in login status=%d location=%q", alreadyLoggedIn.Code, alreadyLoggedIn.Header().Get("Location"))
	}

	for _, testCase := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{"/manager/assets/styles.css", "text/css; charset=utf-8", "@media"},
		{"/manager/assets/login.js", "text/javascript; charset=utf-8", "fetch"},
		{"/manager/assets/manager.js", "text/javascript; charset=utf-8", "fetch"},
	} {
		response := serve(h, http.MethodGet, testCase.path, "", "", "", "192.0.2.10:1", false)
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != testCase.contentType || !strings.Contains(response.Body.String(), testCase.contains) {
			t.Errorf("%s status=%d content-type=%q body=%q", testCase.path, response.Code, response.Header().Get("Content-Type"), response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s Cache-Control=%q", testCase.path, response.Header().Get("Cache-Control"))
		}
	}
	for _, response := range []*httptest.ResponseRecorder{root, loginPage, protected, authorized, alreadyLoggedIn} {
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("status=%d Cache-Control=%q", response.Code, response.Header().Get("Cache-Control"))
		}
	}
}

func TestManagerScriptGuardsTaskRenderingWithRequestGeneration(t *testing.T) {
	script, err := webAssets.ReadFile("web/manager.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, expected := range []string{
		"let taskRequestGeneration",
		"const requestGeneration = ++taskRequestGeneration",
		"requestGeneration !== taskRequestGeneration",
	} {
		if !strings.Contains(source, expected) {
			t.Errorf("manager.js missing stale task response guard %q", expected)
		}
	}
}

func TestManagerScriptUsesCapacityOnlyDetailForOfficialNodes(t *testing.T) {
	script, err := webAssets.ReadFile("web/manager.js")
	if err != nil {
		t.Fatal(err)
	}
	styles, err := webAssets.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, expected := range []string{
		`node.protocol_version === "minimax-v2"`,
		`官方节点 · ${nodeCapacityText(node)}`,
		`makeElement("span", "box-label", "运行任务")`,
		`elements.nodeDetail.replaceChildren(head, capacity)`,
	} {
		if !strings.Contains(source, expected) {
			t.Errorf("manager.js missing official capacity behavior %q", expected)
		}
	}
	if !strings.Contains(string(styles), ".official-capacity") {
		t.Error("styles.css missing official capacity style")
	}
}

func TestManagerPageConfirmsActionsAndPlaysVideoInDialog(t *testing.T) {
	page, err := webAssets.ReadFile("web/manager.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := webAssets.ReadFile("web/manager.js")
	if err != nil {
		t.Fatal(err)
	}
	markup := string(page)
	for _, expected := range []string{
		`id="video-player-dialog"`,
		`id="video-player" class="video-player"`,
		`controls`,
		`id="video-player-status"`,
		`id="close-video-player"`,
	} {
		if !strings.Contains(markup, expected) {
			t.Errorf("manager.html missing video player control %q", expected)
		}
	}
	source := string(script)
	for _, expected := range []string{
		"window.confirm",
		"/cancel`",
		`method: action === "cancel" ? "POST" : "DELETE"`,
		"openVideoPlayer(item)",
		"closeVideoPlayer()",
		"elements.videoPlayer.pause()",
		`elements.videoPlayer.removeAttribute("src")`,
		"elements.videoPlayer.load()",
	} {
		if !strings.Contains(source, expected) {
			t.Errorf("manager.js missing task action behavior %q", expected)
		}
	}
	if strings.Contains(source, "window.open(item.video_url") {
		t.Error("manager.js still opens task video in a new window")
	}
}

func TestManagerPageIncludesTaskDetailDialog(t *testing.T) {
	page, err := webAssets.ReadFile("web/manager.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := webAssets.ReadFile("web/manager.js")
	if err != nil {
		t.Fatal(err)
	}
	styles, err := webAssets.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`id="task-detail-dialog"`, `id="task-detail-title"`, `id="task-detail-body"`, `id="task-detail-status"`, `id="close-task-detail"`} {
		if !strings.Contains(string(page), expected) {
			t.Errorf("manager.html missing task detail dialog %q", expected)
		}
	}
	for _, expected := range []string{`openTaskDetail(item)`, `/manager/api/tasks/${encodeURIComponent(item.id)}`, `renderTaskDetail(detail)`, `closeTaskDetail()`, `mediaFileURL(detail.id, item.input_id)`, `download=1`, `查看`, `下载`, `上游反馈信息`, `upstream_feedback`, `无上游反馈`} {
		if !strings.Contains(string(script), expected) {
			t.Errorf("manager.js missing task detail behavior %q", expected)
		}
	}
	for _, expected := range []string{".task-detail-dialog", ".task-detail-grid", ".task-detail-media", ".task-detail-actions"} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("styles.css missing task detail style %q", expected)
		}
	}
}

func TestTaskDetailRequiresAuthenticationAndReturnsSanitizedRequest(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	store := &taskStoreStub{detail: domain.AdminTaskDetail{
		Task: domain.Task{
			TaskID: "task-detail", APIKeyID: "key-a", Status: domain.StatusQueuedOpen, Model: "MiniMax-H3",
			Scenario: "i2va", Resolution: "768P", RatioRequested: "adaptive", Duration: 5,
			RequestJSON: `{"content":[{"type":"text","text":"hello"},{"type":"image_url","role":"first_frame","image_url":{"url":"proxy-input://task-detail/input_abc"}}],"resolution":"768P","duration":5}`,
			CreatedAt:   now, UpdatedAt: now,
			UpstreamFeedback: &domain.UpstreamFeedback{
				HTTPStatus: 422, Code: "1027", Type: "unprocessable_entity_error",
				Message: "text content contains sensitive content (1027)", ResourceType: "text", RequestID: "req-sensitive",
			},
		},
		InputSpoolFiles: []domain.InputSpoolFile{{
			ID: "input_abc", TaskID: "task-detail", ContentIndex: 1, ContentType: "image_url", Role: "first_frame",
			SourceKind: "data_uri", MediaType: "image/png", Extension: ".png", RelativePath: "task-detail/input_abc.png",
			ObjectURL: "https://cdn.example.com/MiniMax-H3/inputs/request/input_abc.png", SizeBytes: 12,
			SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}},
	}}
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Store: store, Now: func() time.Time { return now }})
	if response := serve(h, http.MethodGet, "/manager/api/tasks/task-detail", "", "", "", "192.0.2.10:1", false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", response.Code)
	}
	cookie := login(t, h, "admin", "secret", "192.0.2.10:1")
	response := serve(h, http.MethodGet, "/manager/api/tasks/task-detail", "", "", cookie, "192.0.2.10:1", false)
	if response.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{`"id":"task-detail"`, `"text":"hello"`, `"input_ref":"proxy-input://task-detail/input_abc"`, `"source_kind":"object_storage"`, `"file_name":"input_abc.png"`, `"legacy_base64_present":false`, `"upstream_feedback":{"http_status":422,"code":"1027","type":"unprocessable_entity_error","message":"text content contains sensitive content (1027)","resource_type":"text","request_id":"req-sensitive"}`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("detail body missing %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "relative_path") || strings.Contains(body, "object_url") || strings.Contains(body, "cdn.example.com") || strings.Contains(body, ";base64,") {
		t.Fatalf("detail leaked path or base64: %s", body)
	}
	store.detail.Task.UpstreamFeedback = nil
	withoutFeedback := serve(h, http.MethodGet, "/manager/api/tasks/task-detail", "", "", cookie, "192.0.2.10:1", false)
	if strings.Contains(withoutFeedback.Body.String(), `"upstream_feedback"`) {
		t.Fatalf("detail returned absent upstream feedback: %s", withoutFeedback.Body.String())
	}
}

func TestDeleteTaskRemovesLocalInputSpoolDirectoryAfterStoreDelete(t *testing.T) {
	root := t.TempDir()
	taskDir := filepath.Join(root, "task-delete")
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "input.png"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &taskStoreStub{}
	h := testHandler(Dependencies{
		Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Store: store, InputSpooler: inputspool.New(root),
	})
	cookie := login(t, h, "admin", "secret", "192.0.2.10:1")
	response := serve(h, http.MethodDelete, "/manager/api/tasks/task-delete", "", "", cookie, "192.0.2.10:1", false)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(taskDir); !os.IsNotExist(err) {
		t.Fatalf("task dir still exists or stat err=%v", err)
	}
	if store.deletedTask != "task-delete" {
		t.Fatalf("deletedTask=%q", store.deletedTask)
	}
}

func TestDeleteTaskKeepsDBWhenRemoteArtifactNodeCannotDelete(t *testing.T) {
	store := &taskStoreStub{locations: []domain.TaskArtifactLocation{{
		ID:             "loc-1",
		TaskID:         "task-delete",
		NodeID:         "legacy-node",
		NodeArtifactID: "artifact-1",
		State:          "ready",
	}}}
	nodes := &nodeStoreStub{items: []domain.ModelNode{{ModelNodeInput: domain.ModelNodeInput{
		ID:              "legacy-node",
		ProtocolVersion: "legacy-gradio-v1",
	}}}}
	h := testHandler(Dependencies{
		Admin:       config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Store:       store,
		Nodes:       nodes,
		NodeSecrets: testNodeSecrets{},
	})
	cookie := login(t, h, "admin", "secret", "192.0.2.10:1")
	response := serve(h, http.MethodDelete, "/manager/api/tasks/task-delete", "", "", cookie, "192.0.2.10:1", false)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body.String())
	}
	if store.deletedTask != "" {
		t.Fatalf("DB delete must not run when remote artifact cannot be deleted, got %q", store.deletedTask)
	}
}

func TestTaskInputContentRequiresAuthenticationAndSupportsInlineAndDownload(t *testing.T) {
	root := t.TempDir()
	taskID := "task-media"
	inputID := "input_media"
	relativePath := filepath.ToSlash(filepath.Join(taskID, inputID+".png"))
	absolutePath := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if err := os.WriteFile(absolutePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	store := &taskStoreStub{inputFile: domain.InputSpoolFile{
		ID: inputID, TaskID: taskID, ContentIndex: 1, ContentType: "image_url", Role: "reference",
		MediaType: "image/png", Extension: ".png", RelativePath: relativePath, SizeBytes: int64(len(body)),
		SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}
	h := testHandler(Dependencies{
		Admin:        config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Store:        store,
		InputSpooler: inputspool.New(root),
	})
	path := "/manager/api/tasks/task-media/inputs/input_media/content"
	if response := serve(h, http.MethodGet, path, "", "", "", "192.0.2.10:1", false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", response.Code)
	}
	cookie := login(t, h, "admin", "secret", "192.0.2.10:1")
	inline := serve(h, http.MethodGet, path, "", "", cookie, "192.0.2.10:1", false)
	if inline.Code != http.StatusOK || !bytes.Equal(inline.Body.Bytes(), body) {
		t.Fatalf("inline status=%d body=%x", inline.Code, inline.Body.Bytes())
	}
	if contentType := inline.Header().Get("Content-Type"); contentType != "image/png" {
		t.Fatalf("inline content-type=%q", contentType)
	}
	if disposition := inline.Header().Get("Content-Disposition"); !strings.Contains(disposition, "inline") || !strings.Contains(disposition, "input_media.png") {
		t.Fatalf("inline disposition=%q", disposition)
	}
	download := serve(h, http.MethodGet, path+"?download=1", "", "", cookie, "192.0.2.10:1", false)
	if download.Code != http.StatusOK || !bytes.Equal(download.Body.Bytes(), body) {
		t.Fatalf("download status=%d body=%x", download.Code, download.Body.Bytes())
	}
	if disposition := download.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") || !strings.Contains(disposition, "input_media.png") {
		t.Fatalf("download disposition=%q", disposition)
	}
}

func TestTaskInputContentProxiesObjectStorageWithoutLocalSpool(t *testing.T) {
	body := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	store := &taskStoreStub{inputFile: domain.InputSpoolFile{
		ID: "input_object", TaskID: "task-object", ContentIndex: 0, ContentType: "image_url", Role: "reference_image",
		SourceKind: "data_uri", MediaType: "image/png", Extension: ".png", RelativePath: "MiniMax-H3/inputs/request/input_object.png",
		ObjectURL: "https://cdn.example.com/MiniMax-H3/inputs/request/input_object.png", SizeBytes: int64(len(body)),
		SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != store.inputFile.ObjectURL || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Fatalf("unsafe object request url=%s headers=%v", request.URL, request.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/png"}, "Content-Length": []string{"8"}}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	h := testHandler(Dependencies{
		Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Store: store, InputObjectClient: client,
	})
	cookie := login(t, h, "admin", "secret", "192.0.2.10:1")
	path := "/manager/api/tasks/task-object/inputs/input_object/content"
	inline := serve(h, http.MethodGet, path, "", "", cookie, "192.0.2.10:1", false)
	if inline.Code != http.StatusOK || !bytes.Equal(inline.Body.Bytes(), body) {
		t.Fatalf("inline status=%d body=%x", inline.Code, inline.Body.Bytes())
	}
	if disposition := inline.Header().Get("Content-Disposition"); !strings.Contains(disposition, "inline") || !strings.Contains(disposition, "input_object.png") {
		t.Fatalf("inline disposition=%q", disposition)
	}
	download := serve(h, http.MethodGet, path+"?download=1", "", "", cookie, "192.0.2.10:1", false)
	if download.Code != http.StatusOK || !bytes.Equal(download.Body.Bytes(), body) || !strings.Contains(download.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download status=%d disposition=%q body=%x", download.Code, download.Header().Get("Content-Disposition"), download.Body.Bytes())
	}
}

func TestTaskInputContentForwardsRangeToObjectStorage(t *testing.T) {
	body := []byte("234")
	store := &taskStoreStub{inputFile: domain.InputSpoolFile{
		ID: "input_video", TaskID: "task-range", ContentIndex: 0, ContentType: "video_url", Role: "input",
		SourceKind: "data_uri", MediaType: "video/mp4", Extension: ".mp4", RelativePath: "MiniMax-H3/inputs/request/input_video.mp4",
		ObjectURL: "https://cdn.example.com/MiniMax-H3/inputs/request/input_video.mp4", SizeBytes: 8,
	}}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Range"); got != "bytes=2-4" {
			t.Fatalf("upstream Range=%q", got)
		}
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header: http.Header{
				"Accept-Ranges":  []string{"bytes"},
				"Content-Length": []string{"3"},
				"Content-Range":  []string{"bytes 2-4/8"},
			},
			Body: io.NopCloser(bytes.NewReader(body)),
		}, nil
	})}
	h := testHandler(Dependencies{
		Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Store: store, InputObjectClient: client,
	})
	cookie := login(t, h, "admin", "secret", "192.0.2.10:1")
	request := httptest.NewRequest(http.MethodGet, "/manager/api/tasks/task-range/inputs/input_video/content", nil)
	request.RemoteAddr = "192.0.2.10:1"
	request.Header.Set("Range", "bytes=2-4")
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: cookie})
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)

	if response.Code != http.StatusPartialContent || !bytes.Equal(response.Body.Bytes(), body) {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.Bytes())
	}
	if response.Header().Get("Accept-Ranges") != "bytes" || response.Header().Get("Content-Length") != "3" || response.Header().Get("Content-Range") != "bytes 2-4/8" {
		t.Fatalf("range headers=%v", response.Header())
	}
}

func TestTaskInputContentRejectsChangedObjectLength(t *testing.T) {
	store := &taskStoreStub{inputFile: domain.InputSpoolFile{
		ID: "input_changed", TaskID: "task-changed", ContentType: "image_url", Role: "input",
		MediaType: "image/png", Extension: ".png", RelativePath: "MiniMax-H3/inputs/request/input_changed.png",
		ObjectURL: "https://cdn.example.com/MiniMax-H3/inputs/request/input_changed.png", SizeBytes: 8,
	}}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, ContentLength: 9,
			Header: http.Header{"Content-Length": []string{"9"}}, Body: io.NopCloser(strings.NewReader("123456789")),
		}, nil
	})}
	h := testHandler(Dependencies{
		Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Store: store, InputObjectClient: client,
	})
	cookie := login(t, h, "admin", "secret", "192.0.2.10:1")
	response := serve(h, http.MethodGet, "/manager/api/tasks/task-changed/inputs/input_changed/content", "", "", cookie, "192.0.2.10:1", false)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "input_object_changed") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTaskInputContentRejectsChangedObjectLengthForRangeRequests(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		statusCode    int
		contentLength int64
		contentRange  string
	}{
		{name: "upstream ignored range", statusCode: http.StatusOK, contentLength: 9},
		{name: "partial response total changed", statusCode: http.StatusPartialContent, contentLength: 3, contentRange: "bytes 2-4/9"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := &taskStoreStub{inputFile: domain.InputSpoolFile{
				ID: "input_changed_range", TaskID: "task-changed-range", ContentType: "video_url", Role: "input",
				MediaType: "video/mp4", Extension: ".mp4", RelativePath: "MiniMax-H3/inputs/request/input_changed_range.mp4",
				ObjectURL: "https://cdn.example.com/MiniMax-H3/inputs/request/input_changed_range.mp4", SizeBytes: 8,
			}}
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				header := http.Header{"Content-Length": []string{strconv.FormatInt(testCase.contentLength, 10)}}
				if testCase.contentRange != "" {
					header.Set("Content-Range", testCase.contentRange)
				}
				return &http.Response{
					StatusCode: testCase.statusCode, ContentLength: testCase.contentLength,
					Header: header, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", int(testCase.contentLength)))),
				}, nil
			})}
			h := testHandler(Dependencies{
				Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
				Store: store, InputObjectClient: client,
			})
			cookie := login(t, h, "admin", "secret", "192.0.2.10:1")
			request := httptest.NewRequest(http.MethodGet, "/manager/api/tasks/task-changed-range/inputs/input_changed_range/content", nil)
			request.RemoteAddr = "192.0.2.10:1"
			request.Header.Set("Range", "bytes=2-4")
			request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: cookie})
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "input_object_changed") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestManagerPageIncludesNodeConfigurationWorkflow(t *testing.T) {
	page, err := webAssets.ReadFile("web/manager.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := webAssets.ReadFile("web/manager.js")
	if err != nil {
		t.Fatal(err)
	}
	styles, err := webAssets.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"open-node-config", "node-config-dialog", "node-config-form", "service_url", "pattern=\"[A-Za-z0-9._\\-]{1,64}\"", "节点 ID 仅支持 1 至 64 位字母、数字、点、下划线或短横线", "minlength=\"32\"", "maxlength=\"32\"", "pattern=\"[A-Za-z0-9]{32}\"", "test-node", "save-node", "delete-node", "profile-config-dialog", "cleanup-dialog", "逻辑分辨率", "保存并生效", "delete-profile"} {
		if !strings.Contains(string(page), expected) {
			t.Errorf("manager.html missing node configuration control %q", expected)
		}
	}
	if !strings.Contains(string(script), `detailRow("格式", item.extension)`) {
		t.Error("manager.js missing media format detail row")
	}
	if strings.Contains(string(page), "name=\"api_key_id\"") || strings.Contains(string(page), "<span>Key ID</span>") {
		t.Error("manager.html still exposes node Key ID")
	}
	for _, removed := range []string{"配置版本", "生成场景", "生成帧率", "主模型", ">CFG<", "AIGC 水印（强制开启）", "音画同步阈值", "发布门禁", "复制为草稿"} {
		if strings.Contains(string(page), removed) {
			t.Errorf("manager.html still contains removed profile control %q", removed)
		}
	}
	for _, expected := range []string{"/manager/api/nodes", "/manager/api/request-profiles", "/manager/api/artifact-cleanups", "method: \"DELETE\"", "window.confirm", "formDirty", "profileFormDirty", "confirmDiscardProfileChanges", "cloneProfileConfig", "profile_template", `formField("api_key").required = true`, `formField("api_key").required = false`, "requestJSON"} {
		if !strings.Contains(string(script), expected) {
			t.Errorf("manager.js missing node configuration behavior %q", expected)
		}
	}
	if !strings.Contains(string(script), "节点 ID 仅支持 1 至 64 位字母、数字、点、下划线或短横线") {
		t.Error("manager.js missing the node ID validation message")
	}
	if !strings.Contains(string(styles), `[hidden] { display: none !important; }`) {
		t.Error("manager styles must keep hidden profile template fields hidden")
	}
	for _, removed := range []string{"/publish", "/clone", "/tests", "profile_version", "aigc_watermark", "av_sync_tolerance_ms", "main_model", "cfg:"} {
		if strings.Contains(string(script), removed) {
			t.Errorf("manager.js still references removed profile behavior %q", removed)
		}
	}
}

func TestManagerDefaultsNewProfilesToFlashVSRWithoutOverridingSavedEngine(t *testing.T) {
	page, err := webAssets.ReadFile("web/manager.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := webAssets.ReadFile("web/manager.js")
	if err != nil {
		t.Fatal(err)
	}

	for _, expected := range []string{
		`restoration: { enabled: true, engine: "flashvsr", scale: 3 }`,
		`config.restoration.engine || "flashvsr"`,
	} {
		if !strings.Contains(string(script), expected) {
			t.Errorf("manager.js missing FlashVSR default contract %q", expected)
		}
	}
	if !strings.Contains(string(page), `<option value="flashvsr" selected>FlashVSR</option>`) {
		t.Error("manager.html must select FlashVSR before JavaScript initializes the form")
	}
}

func TestManagerPageIncludesAPIKeyManagementWorkflow(t *testing.T) {
	page, err := webAssets.ReadFile("web/manager.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := webAssets.ReadFile("web/manager.js")
	if err != nil {
		t.Fatal(err)
	}
	styles, err := webAssets.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}

	for _, expected := range []string{
		"open-api-keys", "密钥管理", "api-key-dialog", "api-key-list", "new-api-key",
		"api-key-name", "api-key-secret-dialog", "api-key-secret-title", "api-key-secret-description",
		"api-key-secret", "copy-api-key",
		"close-api-key-secret", "当前无可用对外密钥", "正在加载密钥", "暂无对外 API Key",
	} {
		if !strings.Contains(string(page), expected) {
			t.Errorf("manager.html missing API key control %q", expected)
		}
	}

	for _, expected := range []string{
		`const apiKeysPath = "/manager/api/api-keys"`,
		"apiKeyRequestGeneration", "apiKeyBusy", "enabled_count", "masked_key",
		`navigator.clipboard.writeText`, `document.execCommand("copy")`,
		`const view = makeElement("button", "", "查看")`,
		`view.addEventListener("click", () => viewStoredAPIKey(item))`, "showAPIKeySecret",
		"clearVisibleAPIKey", `elements.apiKeySecret.textContent = ""`, "state.visibleAPIKey = null",
		"api_key_name_conflict", "api_key_version_conflict", "key_in_use", "cache_refresh_failed",
	} {
		if !strings.Contains(string(script), expected) {
			t.Errorf("manager.js missing API key behavior %q", expected)
		}
	}
	if strings.Contains(string(script), `innerHTML`) {
		t.Error("manager.js must not use innerHTML for dynamic API key content")
	}
	if got := strings.Count(string(script), `"/manager/api/api-keys`); got != 1 {
		t.Errorf("manager.js must centralize the exact API key collection path, occurrences=%d", got)
	}

	for _, expected := range []string{
		".api-key-dialog", ".api-key-list", ".api-key-secret", "overflow-wrap: anywhere", "overflow-x: auto",
		"@media (max-width: 480px)",
	} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("styles.css missing API key responsive contract %q", expected)
		}
	}
}

func (s *taskStoreStub) ListAdminTasks(_ context.Context, filter domain.AdminTaskFilter) ([]domain.AdminTaskSummary, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filter = filter
	return s.items, s.total, s.err
}

func (s *taskStoreStub) GetResultUploadJob(context.Context, string) (domain.ResultUploadJob, error) {
	if s.uploadJob.ID == "" {
		return domain.ResultUploadJob{}, domain.ErrResultUploadNotFound
	}
	return s.uploadJob, nil
}

func (s *taskStoreStub) RetryResultUpload(_ context.Context, taskID string) error {
	s.uploadRetryTask = taskID
	if s.uploadRetryError != nil {
		return s.uploadRetryError
	}
	s.uploadJob.Status = domain.UploadPending
	s.uploadJob.RoundNo++
	s.uploadJob.AttemptNo = 0
	return nil
}

func TestSessionLoginIsStrictAndLogoutInvalidatesCookie(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	h := testHandler(Dependencies{
		Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Now:   func() time.Time { return now },
		Rand:  bytes.NewReader(bytes.Repeat([]byte{0xab}, 32)),
	})

	for name, testCase := range map[string]struct {
		contentType string
		body        string
		want        int
		remote      string
	}{
		"wrong username": {"application/json", `{"username":"other","password":"secret"}`, http.StatusUnauthorized, "192.0.2.1:5000"},
		"wrong password": {"application/json", `{"username":"admin","password":"wrong"}`, http.StatusUnauthorized, "192.0.2.2:5000"},
		"unknown field":  {"application/json", `{"username":"admin","password":"secret","extra":true}`, http.StatusBadRequest, "192.0.2.3:5000"},
		"second object":  {"application/json", `{"username":"admin","password":"secret"}{}`, http.StatusBadRequest, "192.0.2.4:5000"},
		"wrong media":    {"text/plain", `{"username":"admin","password":"secret"}`, http.StatusBadRequest, "192.0.2.5:5000"},
	} {
		t.Run(name, func(t *testing.T) {
			response := serve(h, http.MethodPost, "/manager/api/session", testCase.body, testCase.contentType, "", testCase.remote, false)
			if response.Code != testCase.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
			}
		})
	}

	login := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json; charset=utf-8", "", "192.0.2.200:5000", false)
	if login.Code != http.StatusNoContent || login.Body.Len() != 0 {
		t.Fatalf("login status=%d body=%q", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	decoded, err := hex.DecodeString(cookie.Value)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("cookie token=%q err=%v", cookie.Value, err)
	}
	if cookie.Name != SessionCookieName || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/manager" || cookie.Secure {
		t.Fatalf("cookie=%+v", cookie)
	}

	authorized := serve(h, http.MethodGet, "/manager/api/snapshot", "", "", cookie.Value, "192.0.2.2:5000", false)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status=%d body=%s", authorized.Code, authorized.Body.String())
	}
	logout := serve(h, http.MethodDelete, "/manager/api/session", "", "", cookie.Value, "192.0.2.2:5000", false)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d", logout.Code)
	}
	expired := logout.Result().Cookies()[0]
	if expired.Name != SessionCookieName || expired.MaxAge >= 0 || expired.Path != "/manager" {
		t.Fatalf("expired cookie=%+v", expired)
	}
	if response := serve(h, http.MethodGet, "/manager/api/snapshot", "", "", cookie.Value, "192.0.2.2:5000", false); response.Code != http.StatusUnauthorized {
		t.Fatalf("post-logout status=%d", response.Code)
	}
}

func TestSessionRejectsDuplicateCredentialFields(t *testing.T) {
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}})
	for index, body := range []string{
		`{"username":"admin","username":"admin","password":"secret"}`,
		`{"username":"admin","password":"secret","password":"secret"}`,
	} {
		response := serve(h, http.MethodPost, "/manager/api/session", body, "application/json", "", "198.51.100."+strconv.Itoa(index+1)+":1", false)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("duplicate body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
}

func TestMalformedLoginFailuresAreRateLimited(t *testing.T) {
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}})
	for attempt := 1; attempt <= 5; attempt++ {
		response := serve(h, http.MethodPost, "/manager/api/session", `{"username":`, "application/json", "", "203.0.113.50:1000", false)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("attempt=%d status=%d body=%s", attempt, response.Code, response.Body.String())
		}
	}
	limited := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", "203.0.113.50:2000", false)
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("limited status=%d body=%s", limited.Code, limited.Body.String())
	}
}

func TestExpiredSourceBlockIsClearedBeforeGlobalCleanupIsDue(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	h := testHandler(Dependencies{
		Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Now:   func() time.Time { return now },
	})
	blockedSource := "203.0.113.51:1000"
	for attempt := 1; attempt <= 4; attempt++ {
		response := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"wrong"}`, "application/json", "", blockedSource, false)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt=%d status=%d", attempt, response.Code)
		}
	}
	now = now.Add(50 * time.Second)
	if response := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"wrong"}`, "application/json", "", blockedSource, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("fifth failure status=%d", response.Code)
	}

	now = now.Add(11 * time.Second)
	if response := serve(h, http.MethodPost, "/manager/api/session", `{"username":"other","password":"wrong"}`, "application/json", "", "203.0.113.52:1000", false); response.Code != http.StatusUnauthorized {
		t.Fatalf("cleanup trigger status=%d", response.Code)
	}

	now = now.Add(50 * time.Second)
	response := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", blockedSource, false)
	if response.Code != http.StatusNoContent {
		t.Fatalf("expired source block status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLoginRequiresExactCredentialKeysAndCountsFailures(t *testing.T) {
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}})
	for _, body := range []string{
		`{"Username":"admin","password":"secret"}`,
		`{"username":"admin","Password":"secret"}`,
		`{"password":"secret"}`,
		`{"username":"admin"}`,
		`{"username":"admin","password":"secret","extra":true}`,
	} {
		response := serve(h, http.MethodPost, "/manager/api/session", body, "application/json", "", "203.0.113.60:1000", false)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
	limited := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", "203.0.113.60:2000", false)
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("limited status=%d body=%s", limited.Code, limited.Body.String())
	}
}

func TestLoginRejectsNonStringCredentialValuesAndCountsFailures(t *testing.T) {
	values := []string{"null", "123", "true", `{}`, `[]`}
	for index, field := range []string{"username", "password"} {
		h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}})
		remote := "203.0.113." + strconv.Itoa(70+index) + ":1000"
		for _, value := range values {
			body := `{"username":"admin","password":"secret"}`
			if field == "username" {
				body = `{"username":` + value + `,"password":"secret"}`
			} else {
				body = `{"username":"admin","password":` + value + `}`
			}
			response := serve(h, http.MethodPost, "/manager/api/session", body, "application/json", "", remote, false)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("field=%s value=%s status=%d response=%s", field, value, response.Code, response.Body.String())
			}
		}
		limited := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", remote, false)
		if limited.Code != http.StatusTooManyRequests {
			t.Fatalf("field=%s limited status=%d body=%s", field, limited.Code, limited.Body.String())
		}
	}
}

func TestExpiredSessionIsRemovedWithoutAnotherRequest(t *testing.T) {
	raw, ok := NewHandler(Dependencies{
		Admin:  config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: 40 * time.Millisecond},
		Cache:  monitorcache.NewCache(nil),
		Store:  &taskStoreStub{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).(*handler)
	if !ok {
		t.Fatal("NewHandler must return the monitor handler")
	}
	if cookie := loginResponse(raw, "admin", "secret", "192.0.2.80:1"); cookie == "" {
		t.Fatal("login failed")
	}
	raw.sessionMu.Lock()
	initial := len(raw.sessions)
	raw.sessionMu.Unlock()
	if initial != 1 {
		t.Fatalf("initial sessions=%d", initial)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		raw.sessionMu.Lock()
		remaining := len(raw.sessions)
		raw.sessionMu.Unlock()
		if remaining == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expired session was not removed in the background")
}

func TestSessionRejectsForgedAndExpiredCookiesAndSetsSecureOverTLS(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Minute}, Now: func() time.Time { return now }})
	login := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", "192.0.2.1:1", true)
	cookie := login.Result().Cookies()[0]
	if !cookie.Secure {
		t.Fatalf("secure=%v", cookie.Secure)
	}
	if forged := serve(h, http.MethodGet, "/manager/api/snapshot", "", "", strings.Repeat("0", 64), "192.0.2.1:1", true); forged.Code != http.StatusUnauthorized {
		t.Fatalf("forged status=%d", forged.Code)
	}
	now = now.Add(time.Minute)
	if expired := serve(h, http.MethodGet, "/manager/api/snapshot", "", "", cookie.Value, "192.0.2.1:1", true); expired.Code != http.StatusUnauthorized {
		t.Fatalf("expired status=%d", expired.Code)
	}
}

func TestSessionSetsSecureCookieWhenConfiguredBehindTLSProxy(t *testing.T) {
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Minute, SecureCookie: true}})
	login := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", "192.0.2.1:1", false)
	cookie := login.Result().Cookies()[0]
	if !cookie.Secure {
		t.Fatal("configured secure cookie must be set without direct TLS")
	}
}

func TestSnapshotMapsOnlyWhitelistedFieldsAndSummarizesNodes(t *testing.T) {
	queue, cpu := 2, 12.5
	updated := time.Unix(2_000_000_100, 0)
	cache := monitorcache.NewCache([]monitorcache.NodeSnapshot{
		{ID: "gpu-1", Address: "private.local:7860", Health: monitorcache.HealthHealthy, Runtime: monitorcache.RuntimeRunning, PrivateQueue: &queue, CPUPercent: &cpu, CheckedAt: updated.Add(-time.Second), LastHealthyAt: updated.Add(-2 * time.Second), UpdatedAt: updated, CurrentTask: &monitorcache.CurrentTaskSnapshot{ID: "task-running", Status: "running", StartedAt: updated.Add(-time.Minute)}, LatestFinishedTask: &monitorcache.FinishedTaskSnapshot{ID: "task-done", APIKeyID: "customer", Status: "succeeded", DurationSeconds: 9, FinishedAt: updated.Add(-time.Hour)}, LastError: &monitorcache.ErrorSnapshot{Code: "upstream_poll_error", Summary: "raw secret https://private"}},
		{ID: "gpu-2", Health: monitorcache.HealthUnhealthy, Runtime: monitorcache.RuntimeIdle, UpdatedAt: updated.Add(-time.Minute)},
		{ID: "gpu-3"},
		{ID: "gpu-4", Disabled: true, Applying: true, Health: monitorcache.HealthHealthy, Runtime: monitorcache.RuntimeRunning},
	})
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "a", Password: "b", SessionTTL: time.Hour, MonitorInterval: 2 * time.Second}, Cache: cache})
	cookie := login(t, h, "a", "b", "198.51.100.1:1")
	response := serve(h, http.MethodGet, "/manager/api/snapshot", "", "", cookie, "198.51.100.1:1", false)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var body struct {
		UpdatedAt        int64                                              `json:"updated_at"`
		StaleAfterSecond int64                                              `json:"stale_after_seconds"`
		Summary          struct{ Healthy, Unhealthy, Unknown, Running int } `json:"summary"`
		Upstreams        []struct {
			ID, Address, Health, Runtime string
			Enabled, Applying            bool
			PrivateQueue                 *int                            `json:"private_queue"`
			CheckedAt                    int64                           `json:"checked_at"`
			LastError                    *struct{ Code, Summary string } `json:"last_error"`
		} `json:"upstreams"`
	}
	decodeResponse(t, response, &body)
	if body.UpdatedAt != updated.Unix() || body.StaleAfterSecond != 6 || body.Summary.Healthy != 1 || body.Summary.Unhealthy != 1 || body.Summary.Unknown != 1 || body.Summary.Running != 1 || len(body.Upstreams) != 4 {
		t.Fatalf("snapshot=%+v", body)
	}
	if body.Upstreams[0].LastError == nil || body.Upstreams[0].LastError.Summary != "私有服务状态查询失败" || body.Upstreams[2].CheckedAt != 0 || body.Upstreams[3].Enabled || !body.Upstreams[3].Applying {
		t.Fatalf("upstreams=%+v", body.Upstreams)
	}
	output := response.Body.String()
	for _, sensitive := range []string{"raw secret", "https://private", "password", "result_internal_url", "request_json"} {
		if strings.Contains(output, sensitive) {
			t.Fatalf("response leaked %q: %s", sensitive, output)
		}
	}
}

func TestSnapshotIncludesOfficialNodeProtocolAndCapacity(t *testing.T) {
	updated := time.Unix(2_000_000_100, 0)
	cache := monitorcache.NewCache([]monitorcache.NodeSnapshot{
		{ID: "internal-1", Health: monitorcache.HealthHealthy, Runtime: monitorcache.RuntimeIdle, UpdatedAt: updated},
		{ID: "official-1", Health: monitorcache.HealthHealthy, Runtime: monitorcache.RuntimeRunning, UpdatedAt: updated},
	})
	nodes := &nodeStoreStub{
		items: []domain.ModelNode{
			{ModelNodeInput: domain.ModelNodeInput{ID: "internal-1", ProtocolVersion: "h3-node-v1", MaxConcurrency: 1}},
			{ModelNodeInput: domain.ModelNodeInput{ID: "official-1", ProtocolVersion: "minimax-v2", MaxConcurrency: 3}},
		},
		activeTasks: map[string]int{"official-1": 1},
	}
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "a", Password: "b", SessionTTL: time.Hour}, Cache: cache, Nodes: nodes})
	cookie := login(t, h, "a", "b", "198.51.100.2:1")
	response := serve(h, http.MethodGet, "/manager/api/snapshot", "", "", cookie, "198.51.100.2:1", false)

	var body struct {
		Upstreams []struct {
			ID              string `json:"id"`
			ProtocolVersion string `json:"protocol_version"`
			ActiveTasks     int    `json:"active_tasks"`
			MaxConcurrency  int    `json:"max_concurrency"`
		} `json:"upstreams"`
	}
	decodeResponse(t, response, &body)
	if response.Code != http.StatusOK || len(body.Upstreams) != 2 {
		t.Fatalf("status=%d body=%+v", response.Code, body)
	}
	if body.Upstreams[0].ProtocolVersion != "h3-node-v1" || body.Upstreams[0].ActiveTasks != 0 || body.Upstreams[0].MaxConcurrency != 1 {
		t.Fatalf("internal=%+v", body.Upstreams[0])
	}
	if body.Upstreams[1].ProtocolVersion != "minimax-v2" || body.Upstreams[1].ActiveTasks != 1 || body.Upstreams[1].MaxConcurrency != 3 {
		t.Fatalf("official=%+v", body.Upstreams[1])
	}
}

func TestTasksValidatesFiltersAndReturnsMinimalShape(t *testing.T) {
	created := time.Unix(2_000_000_000, 0)
	started := created.Add(10 * time.Minute)
	store := &taskStoreStub{total: 1, items: []domain.AdminTaskSummary{{TaskID: "task-1", APIKeyID: "customer", Status: domain.V2Running, InternalStatus: domain.StatusReconciling, RetryCount: 1, UpstreamID: "gpu-1", Scenario: "t2va", Resolution: "768P", Duration: 99, CreatedAt: created, StartedAt: started}}}
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "a", Password: "b", SessionTTL: time.Hour}, Store: store, Now: func() time.Time { return started.Add(65 * time.Second) }})
	cookie := login(t, h, "a", "b", "203.0.113.1:1")
	response := serve(h, http.MethodGet, "/manager/api/tasks?page_num=2&page_size=20&status=running&upstream_id=gpu-1&search=task", "", "", cookie, "203.0.113.1:1", false)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	store.mu.Lock()
	filter := store.filter
	store.mu.Unlock()
	if filter != (domain.AdminTaskFilter{PageNum: 2, PageSize: 20, Status: domain.V2Running, UpstreamID: "gpu-1", Search: "task"}) {
		t.Fatalf("filter=%+v", filter)
	}
	var raw map[string]any
	decodeResponse(t, response, &raw)
	items := raw["items"].([]any)
	item := items[0].(map[string]any)
	if len(item) != 18 || item["id"] != "task-1" || item["created_at"] != float64(created.Unix()) || item["duration_seconds"] != float64(65) || item["phase"] != "retrying" || item["retry_count"] != float64(1) || item["can_cancel"] != true || item["can_delete"] != false || item["video_url"] != nil || item["result_delivery_status"] != "not_required" || item["can_retry_upload"] != false || raw["page_num"] != float64(2) || raw["page_size"] != float64(20) {
		t.Fatalf("body=%v", raw)
	}
	for _, forbidden := range []string{"duration", "started_at", "finished_at", "request_json", "result_internal_url"} {
		if _, ok := item[forbidden]; ok {
			t.Fatalf("unexpected field %q in %v", forbidden, item)
		}
	}

	for _, path := range []string{
		"/manager/api/tasks?page_num=0",
		"/manager/api/tasks?page_size=15",
		"/manager/api/tasks?status=unknown",
		"/manager/api/tasks?extra=1",
		"/manager/api/tasks?page_num=1&page_num=2",
	} {
		if invalid := serve(h, http.MethodGet, path, "", "", cookie, "203.0.113.1:1", false); invalid.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d body=%s", path, invalid.Code, invalid.Body.String())
		}
	}
	defaults := serve(h, http.MethodGet, "/manager/api/tasks", "", "", cookie, "203.0.113.1:1", false)
	if defaults.Code != http.StatusOK {
		t.Fatalf("defaults status=%d", defaults.Code)
	}
	store.mu.Lock()
	filter = store.filter
	store.mu.Unlock()
	if filter.PageNum != 1 || filter.PageSize != 10 {
		t.Fatalf("defaults=%+v", filter)
	}
}

func TestTasksSignsArtifactPlaybackURLWithoutExposingArtifactID(t *testing.T) {
	created := time.Unix(2_000_000_000, 0)
	store := &taskStoreStub{total: 1, items: []domain.AdminTaskSummary{{
		TaskID: "task-1", APIKeyID: "customer", Status: domain.V2Succeeded, InternalStatus: domain.StatusSucceeded,
		ResultArtifactID: "artifact-1", CreatedAt: created,
	}}}
	signer := &managerSignerSpy{url: "https://proxy.example/v2/files/artifact-1/content?expires=1&signature=signed"}
	h := testHandler(Dependencies{
		Admin: config.AdminConfig{Username: "a", Password: "b", SessionTTL: time.Hour},
		Store: store, ArtifactURLs: signer,
	})
	cookie := login(t, h, "a", "b", "203.0.113.8:1")
	response := serve(h, http.MethodGet, "/manager/api/tasks", "", "", cookie, "203.0.113.8:1", false)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	decodeResponse(t, response, &body)
	if len(body.Items) != 1 || body.Items[0]["video_url"] != signer.url {
		t.Fatalf("body=%+v", body)
	}
	if signer.artifactID != "artifact-1" || signer.ownerID != "customer" {
		t.Fatalf("signer=%+v", signer)
	}
	if _, exists := body.Items[0]["result_artifact_id"]; exists {
		t.Fatalf("artifact id leaked: %+v", body.Items[0])
	}
}

func TestTaskPhaseShowsWaitingForUnassignedRunningStageTask(t *testing.T) {
	item := domain.AdminTaskSummary{InternalStatus: domain.StatusRunning}
	if got := taskPhase(item); got != "waiting" {
		t.Fatalf("taskPhase() = %q, want waiting", got)
	}
}

func TestPublicVideoURLDoesNotExposePrivateLegacyNodeAddress(t *testing.T) {
	h := &handler{}
	unsafe, err := h.publicVideoURL(context.Background(), domain.AdminTaskSummary{
		Status: domain.V2Succeeded, ResultPublicURL: "http://127.0.0.1:7860/gradio_api/file=video.mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if unsafe != nil {
		t.Fatalf("private legacy URL exposed: %q", *unsafe)
	}

	safe, err := h.publicVideoURL(context.Background(), domain.AdminTaskSummary{
		Status: domain.V2Succeeded, ResultPublicURL: "https://cdn.example/video.mp4?token=legacy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if safe == nil || *safe != "https://cdn.example/video.mp4?token=legacy" {
		t.Fatalf("safe legacy URL missing: %v", safe)
	}
}

func TestPublicVideoURLSigningFailureDoesNotFallBackToLegacyURL(t *testing.T) {
	signerErr := errors.New("signing unavailable")
	h := &handler{artifactURLs: &managerSignerSpy{err: signerErr}}
	videoURL, err := h.publicVideoURL(context.Background(), domain.AdminTaskSummary{
		APIKeyID: "owner-a", Status: domain.V2Succeeded,
		ResultArtifactID: "artifact-1", ResultPublicURL: "https://cdn.example/legacy.mp4",
	})
	if !errors.Is(err, signerErr) || videoURL != nil {
		t.Fatalf("publicVideoURL() = %v, %v; want signing error without fallback", videoURL, err)
	}
}

func TestTaskActionsRequireSessionAndMapStoreResults(t *testing.T) {
	wakeCount := 0
	store := &taskStoreStub{}
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "a", Password: "b", SessionTTL: time.Hour}, Store: store, Wake: func() { wakeCount++ }})
	cookie := login(t, h, "a", "b", "203.0.113.2:1")

	unauthorized := serve(h, http.MethodPost, "/manager/api/tasks/task-1/cancel", "", "", "", "203.0.113.2:1", false)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	cancelled := serve(h, http.MethodPost, "/manager/api/tasks/task-1/cancel", "", "", cookie, "203.0.113.2:1", false)
	if cancelled.Code != http.StatusAccepted || wakeCount != 1 || store.cancelledTask != "task-1" {
		t.Fatalf("cancel status=%d wake=%d task=%q body=%s", cancelled.Code, wakeCount, store.cancelledTask, cancelled.Body.String())
	}
	deleted := serve(h, http.MethodDelete, "/manager/api/tasks/task-1", "", "", cookie, "203.0.113.2:1", false)
	if deleted.Code != http.StatusNoContent || store.deletedTask != "task-1" || deleted.Body.Len() != 0 {
		t.Fatalf("delete status=%d task=%q body=%s", deleted.Code, store.deletedTask, deleted.Body.String())
	}

	store.cancelError = domain.ErrTaskNotOperable
	if response := serve(h, http.MethodPost, "/manager/api/tasks/task-2/cancel", "", "", cookie, "203.0.113.2:1", false); response.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", response.Code, response.Body.String())
	}
	store.deleteError = domain.ErrTaskNotFound
	if response := serve(h, http.MethodDelete, "/manager/api/tasks/task-2", "", "", cookie, "203.0.113.2:1", false); response.Code != http.StatusNotFound {
		t.Fatalf("not found status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRetryResultUploadStartsNewRound(t *testing.T) {
	store := &taskStoreStub{uploadJob: domain.ResultUploadJob{ID: "upload-1", TaskID: "task-1", Status: domain.UploadFailed, RoundNo: 1, AttemptNo: 3}}
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "a", Password: "b", SessionTTL: time.Hour}, Store: store})
	cookie := login(t, h, "a", "b", "203.0.113.21:1")
	response := serve(h, http.MethodPost, "/manager/api/tasks/task-1/result-upload/retry", "", "", cookie, "203.0.113.21:1", false)
	if response.Code != http.StatusAccepted || store.uploadRetryTask != "task-1" {
		t.Fatalf("status=%d task=%q body=%s", response.Code, store.uploadRetryTask, response.Body.String())
	}
	var body map[string]any
	decodeResponse(t, response, &body)
	if body["result_delivery_status"] != "pending" || body["result_upload_round"] != float64(2) || body["result_upload_attempts"] != float64(0) {
		t.Fatalf("body=%v", body)
	}

	store.uploadRetryError = domain.ErrResultUploadNotRetryable
	conflict := serve(h, http.MethodPost, "/manager/api/tasks/task-1/result-upload/retry", "", "", cookie, "203.0.113.21:1", false)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestTaskDurationSecondsHandlesQueuedAndFinishedTasks(t *testing.T) {
	started := time.Unix(2_000_000_000, 0)
	if got := taskDurationSeconds(domain.AdminTaskSummary{Status: domain.V2Queued}, started); got != nil {
		t.Fatalf("queued duration = %v", *got)
	}
	got := taskDurationSeconds(domain.AdminTaskSummary{Status: domain.V2Succeeded, StartedAt: started, FinishedAt: started.Add(125 * time.Second)}, started.Add(time.Hour))
	if got == nil || *got != 125 {
		t.Fatalf("finished duration = %v", got)
	}
}

func TestProtectedRoutesMethodsRateLimitAndConcurrentSessions(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Now: func() time.Time { return now }})
	for _, path := range []string{"/manager/api/snapshot", "/manager/api/tasks"} {
		response := serve(h, http.MethodGet, path, "", "", "", "192.0.2.1:1", false)
		if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d headers=%v", path, response.Code, response.Header())
		}
	}
	if response := serve(h, http.MethodPut, "/manager/api/session", "", "", "", "192.0.2.1:1", false); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status=%d", response.Code)
	}
	for i := 0; i < 5; i++ {
		response := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"wrong"}`, "application/json", "", "192.0.2.20:1000", false)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d status=%d", i+1, response.Code)
		}
	}
	if limited := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", "192.0.2.20:2000", false); limited.Code != http.StatusTooManyRequests {
		t.Fatalf("limited status=%d body=%s", limited.Code, limited.Body.String())
	}
	now = now.Add(time.Minute)
	if recovered := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", "192.0.2.20:3000", false); recovered.Code != http.StatusNoContent {
		t.Fatalf("recovered status=%d", recovered.Code)
	}

	var wg sync.WaitGroup
	errors := make(chan string, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			remote := "198.51.100." + string(rune('A'+i)) + ":1"
			cookie := loginResponse(h, "admin", "secret", remote)
			if cookie == "" {
				errors <- "login failed"
				return
			}
			if got := serve(h, http.MethodGet, "/manager/api/snapshot", "", "", cookie, remote, false); got.Code != http.StatusOK {
				errors <- "authorization failed"
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestConcurrentLoginFailuresReserveRateLimitCapacity(t *testing.T) {
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}})
	start := make(chan struct{})
	statuses := make(chan int, 20)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			response := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"wrong"}`, "application/json", "", "198.51.100.200:1000", false)
			statuses <- response.Code
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	notLimited := 0
	for status := range statuses {
		if status != http.StatusTooManyRequests {
			notLimited++
		}
	}
	if notLimited > loginFailureLimit {
		t.Fatalf("non-429 responses=%d, want at most %d", notLimited, loginFailureLimit)
	}
	if response := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"wrong"}`, "application/json", "", "198.51.100.200:2000", false); response.Code != http.StatusTooManyRequests {
		t.Fatalf("subsequent status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLoginFailureSourcesHaveHardCapacity(t *testing.T) {
	raw, ok := NewHandler(Dependencies{
		Admin:  config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		Cache:  monitorcache.NewCache(nil),
		Store:  &taskStoreStub{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).(*handler)
	if !ok {
		t.Fatal("NewHandler must return the monitor handler")
	}
	const capacity = 4096
	for i := 0; i < capacity; i++ {
		remote := "198.18." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256) + ":1"
		response := serve(raw, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"wrong"}`, "application/json", "", remote, false)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("source=%d status=%d body=%s", i, response.Code, response.Body.String())
		}
	}
	overflow := serve(raw, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"wrong"}`, "application/json", "", "203.0.113.250:1", false)
	if overflow.Code != http.StatusTooManyRequests {
		t.Fatalf("overflow status=%d body=%s", overflow.Code, overflow.Body.String())
	}
	raw.failureMu.Lock()
	sources := len(raw.failures)
	raw.failureMu.Unlock()
	if sources > capacity {
		t.Fatalf("failure sources=%d, want at most %d", sources, capacity)
	}
}

func testHandler(dependencies Dependencies) http.Handler {
	if dependencies.Cache == nil {
		dependencies.Cache = monitorcache.NewCache(nil)
	}
	if dependencies.Store == nil {
		dependencies.Store = &taskStoreStub{}
	}
	dependencies.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewHandler(dependencies)
}

func login(t *testing.T, handler http.Handler, username, password, remote string) string {
	t.Helper()
	cookie := loginResponse(handler, username, password, remote)
	if cookie == "" {
		t.Fatal("login failed")
	}
	return cookie
}

func loginResponse(handler http.Handler, username, password, remote string) string {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	response := serve(handler, http.MethodPost, "/manager/api/session", string(body), "application/json", "", remote, false)
	if response.Code != http.StatusNoContent || len(response.Result().Cookies()) != 1 {
		return ""
	}
	return response.Result().Cookies()[0].Value
}

func serve(handler http.Handler, method, path, body, contentType, cookie, remote string, useTLS bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = remote
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: cookie})
	}
	if useTLS {
		request.TLS = &tls.ConnectionState{}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode %q: %v", response.Body.String(), err)
	}
}
