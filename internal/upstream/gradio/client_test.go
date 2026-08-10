package gradio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/httpapi/v2"
)

func TestClientUsesGradioTwoPhaseProtocol(t *testing.T) {
	var posted struct {
		Data []any `json:"data"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/gradio_api/call/check_and_get_video":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"event_id": "event-1"})
		case r.Method == http.MethodGet && r.URL.Path == "/gradio_api/call/check_and_get_video/event-1":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: complete\ndata: [[{\"video\":{\"url\":\"http://private/video.mp4\"}}],\"已完成\",\"\",\"\",\"\"]\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, server.Client(), 1<<20)
	result, err := client.Call(context.Background(), "check_and_get_video", []any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(posted.Data) != 0 || len(result) != 5 || result[1] != "已完成" {
		t.Fatalf("posted=%+v result=%+v", posted, result)
	}
}

func TestClientHealthUsesConfiguredPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	if err := NewClient(base, server.Client(), 1024).Healthy(context.Background(), "/ready"); err != nil {
		t.Fatal(err)
	}
}

func TestClientReturnsSSEError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"event_id":"failed-1"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: error\ndata: upstream failed\n\n"))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	_, err := NewClient(base, server.Client(), 1024).Call(context.Background(), "submit", []any{})
	if err == nil || !errors.Is(err, ErrRequestRejected) || !strings.Contains(err.Error(), "upstream failed") {
		t.Fatalf("Call() error = %v", err)
	}
}

func TestClientLetsSSEResponseFinishAfterTerminalEvent(t *testing.T) {
	closedEarly := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"event_id":"complete-1"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: complete\ndata: [\"done\"]\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			closedEarly <- true
		case <-time.After(50 * time.Millisecond):
			closedEarly <- false
		}
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)

	result, err := NewClient(base, server.Client(), 1024).Call(context.Background(), "submit", []any{})
	if err != nil || len(result) != 1 || result[0] != "done" {
		t.Fatalf("Call() result=%v error=%v", result, err)
	}
	if <-closedEarly {
		t.Fatal("客户端在 Gradio 完成 HTTP 响应前关闭了 SSE 连接")
	}
}

func TestClientRejectsOversizedPostResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"event_id":"` + strings.Repeat("x", 128) + `"}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	_, err := NewClient(base, server.Client(), 64).Call(context.Background(), "submit", []any{})
	if err == nil || !strings.Contains(err.Error(), "响应体超过限制") {
		t.Fatalf("Call() error = %v", err)
	}
}

func TestClientListsGetsAndCancelsJobs(t *testing.T) {
	const jobID = "11111111-1111-1111-1111-111111111111"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/jobs":
			if r.URL.Query().Get("limit") != "256" {
				t.Fatalf("limit = %q", r.URL.Query().Get("limit"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jobs":       []map[string]any{{"id": jobID, "status": "in_progress", "create_time": 123}},
				"pagination": map[string]any{"offset": 0, "limit": nil, "total": 1, "has_more": false},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/jobs/"+jobID:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": jobID, "status": "completed", "create_time": 123})
		case r.Method == http.MethodPost && r.URL.Path == "/api/jobs/"+jobID+"/cancel":
			_ = json.NewEncoder(w).Encode(map[string]bool{"cancelled": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, server.Client(), 1<<20)

	jobs, err := client.ListJobs(context.Background())
	if err != nil || len(jobs) != 1 || jobs[0].ID != jobID || jobs[0].Status != JobInProgress || jobs[0].CreateTime != 123 {
		t.Fatalf("ListJobs() = %+v, %v", jobs, err)
	}
	job, err := client.GetJob(context.Background(), jobID)
	if err != nil || job.Status != JobCompleted {
		t.Fatalf("GetJob() = %+v, %v", job, err)
	}
	cancelled, err := client.CancelJob(context.Background(), jobID)
	if err != nil || !cancelled {
		t.Fatalf("CancelJob() = %v, %v", cancelled, err)
	}
}

func TestClientUsesSeparateJobsBaseURL(t *testing.T) {
	jobsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/jobs" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"jobs":[]}`)
	}))
	defer jobsServer.Close()
	gradioURL, _ := url.Parse("http://127.0.0.1:7860")
	jobsURL, _ := url.Parse(jobsServer.URL)
	client := NewClientWithJobs(gradioURL, jobsURL, jobsServer.Client(), 1024)

	if _, err := client.ListJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClientClassifiesMissingJobAndRejectsInvalidID(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, server.Client(), 1024)

	_, err := client.GetJob(context.Background(), "11111111-1111-1111-1111-111111111111")
	if !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("GetJob() error = %v", err)
	}
	if _, err := client.CancelJob(context.Background(), "not-a-uuid"); err == nil || !strings.Contains(err.Error(), "job_id") {
		t.Fatalf("CancelJob(invalid) error = %v", err)
	}
}

func TestBuildArgumentsKeepsAll32Positions(t *testing.T) {
	request := v2.ValidatedRequest{CreateRequest: v2.CreateRequest{Model: "MiniMax-H3", Content: []v2.ContentItem{{Type: "text", Text: "保持一致"}, {Type: "image_url", ImageURL: &v2.URLValue{URL: "https://media.example/ref.png"}, Role: "reference_image"}}, Resolution: "2K", Duration: 5, Ratio: "16:9"}, Scenario: "r2va", Prompt: "保持一致", Width: 1920, Height: 1080, InputImageCount: 1}
	profile := config.GenerationProfile{ModelMode: "custom", CustomModel: "__follow_model_mode__", CustomModelHigh: "__follow_model_mode__", EasyCache: true, Steps: 20}
	args, err := BuildArguments(request, profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 32 {
		t.Fatalf("len(args) = %d", len(args))
	}
	if args[0] != "全能参考生成视频" || args[1] != "保持一致" || args[12] != 1920 || args[13] != 1080 || args[14] != 5 {
		t.Fatalf("args = %#v", args)
	}
	file, ok := args[17].(FileData)
	if !ok || file.Path != "https://media.example/ref.png" || file.Meta.Type != "gradio.FileData" {
		t.Fatalf("reference = %#v", args[17])
	}
	if args[18] != nil || args[31] != nil {
		t.Fatalf("unused slots are not nil")
	}
}

func TestBuildArgumentsUsesOfficialImageToVideoModeForFirstAndLastFrames(t *testing.T) {
	request := v2.ValidatedRequest{CreateRequest: v2.CreateRequest{Model: "MiniMax-H3", Content: []v2.ContentItem{{Type: "text", Text: "首尾帧过渡"}, {Type: "image_url", ImageURL: &v2.URLValue{URL: "https://media.example/first.png"}, Role: "first_frame"}, {Type: "image_url", ImageURL: &v2.URLValue{URL: "https://media.example/last.png"}, Role: "last_frame"}}, Resolution: "2K", Duration: 5, Ratio: "16:9"}, Scenario: "i2va", Prompt: "首尾帧过渡", Width: 1920, Height: 1080, InputImageCount: 2}
	args, err := BuildArguments(request, config.GenerationProfile{ModelMode: "high_quality", Steps: 20})
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != "图生视频（首帧/可选尾帧）" {
		t.Fatalf("mode = %q", args[0])
	}
}

func TestBuildArgumentsUsesURLFieldForBase64Image(t *testing.T) {
	const imageData = "data:image/png;base64,iVBORw0KGgo="
	request := v2.ValidatedRequest{CreateRequest: v2.CreateRequest{Model: "MiniMax-H3", Content: []v2.ContentItem{{Type: "text", Text: "首帧"}, {Type: "image_url", ImageURL: &v2.URLValue{URL: imageData}, Role: "first_frame"}}, Resolution: "768P", Duration: 4, Ratio: "adaptive"}, Scenario: "i2va", Prompt: "首帧", Width: 768, Height: 768, InputImageCount: 1}
	args, err := BuildArguments(request, config.GenerationProfile{ModelMode: "high_quality", Steps: 20})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(args[5])
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"url":"data:image/png;base64,iVBORw0KGgo=","meta":{"_type":"gradio.FileData"}}` {
		t.Fatalf("base64 image = %s, want Gradio ImageData url", encoded)
	}
}

func TestClientPrepareArgumentsUploadsBase64Audio(t *testing.T) {
	var uploadCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/gradio_api/upload" {
			http.NotFound(w, r)
			return
		}
		uploadCalls++
		if err := r.ParseMultipartForm(16 << 20); err != nil {
			t.Fatal(err)
		}
		file, header, err := r.FormFile("files")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		content, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		if header.Filename != "reference-audio-1.wav" || string(content) != "RIFF" {
			t.Fatalf("filename=%q content=%q", header.Filename, content)
		}
		_ = json.NewEncoder(w).Encode([]string{`C:\gradio-cache\reference-audio-1.wav`})
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, server.Client(), 1<<20)
	request := v2.ValidatedRequest{
		CreateRequest: v2.CreateRequest{Model: "MiniMax-H3", Content: []v2.ContentItem{
			{Type: "text", Text: "跟随音频"},
			{Type: "audio_url", AudioURL: &v2.URLValue{URL: "data:audio/wav;base64,UklGRg=="}, Role: "reference_audio"},
		}, Resolution: "768P", Duration: 4, Ratio: "adaptive"},
		Scenario: "r2va", Prompt: "跟随音频", Width: 768, Height: 768,
	}

	args, err := client.PrepareArguments(context.Background(), request, config.GenerationProfile{ModelMode: "high_quality", Steps: 20})
	if err != nil {
		t.Fatal(err)
	}
	fileData, ok := args[29].(FileData)
	if !ok || fileData.Path != `C:\gradio-cache\reference-audio-1.wav` || fileData.URL != "" || uploadCalls != 1 {
		t.Fatalf("audio=%#v upload calls=%d", args[29], uploadCalls)
	}
}

func TestClientPrepareArgumentsKeepsHTTPAudioWithoutUpload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, server.Client(), 1<<20)
	request := v2.ValidatedRequest{
		CreateRequest: v2.CreateRequest{Model: "MiniMax-H3", Content: []v2.ContentItem{
			{Type: "text", Text: "跟随音频"},
			{Type: "audio_url", AudioURL: &v2.URLValue{URL: "https://media.example/reference.mp3"}, Role: "reference_audio"},
		}, Resolution: "768P", Duration: 4, Ratio: "adaptive"},
		Scenario: "r2va", Prompt: "跟随音频", Width: 768, Height: 768,
	}

	args, err := client.PrepareArguments(context.Background(), request, config.GenerationProfile{ModelMode: "high_quality", Steps: 20})
	if err != nil {
		t.Fatal(err)
	}
	fileData, ok := args[29].(FileData)
	if !ok || fileData.Path != "https://media.example/reference.mp3" {
		t.Fatalf("audio = %#v", args[29])
	}
}

func TestClientPrepareArgumentsRejectsInvalidUploadResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]string{})
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, server.Client(), 1<<20)
	request := v2.ValidatedRequest{
		CreateRequest: v2.CreateRequest{Content: []v2.ContentItem{
			{Type: "text", Text: "跟随音频"},
			{Type: "audio_url", AudioURL: &v2.URLValue{URL: "data:audio/mpeg;base64,SUQz"}, Role: "reference_audio"},
		}, Resolution: "768P", Duration: 4, Ratio: "adaptive"},
		Scenario: "r2va", Prompt: "跟随音频", Width: 768, Height: 768,
	}

	_, err := client.PrepareArguments(context.Background(), request, config.GenerationProfile{ModelMode: "high_quality", Steps: 20})
	if err == nil || !strings.Contains(err.Error(), "未返回唯一文件路径") {
		t.Fatalf("PrepareArguments() error = %v", err)
	}
}

func TestGalleryDeltaAndPublicURLMapping(t *testing.T) {
	before := []string{"http://private.local/gradio_api/file=/old.mp4"}
	gallery := []any{map[string]any{"video": map[string]any{"url": "http://private.local/gradio_api/file=/old.mp4"}}, map[string]any{"path": "http://private.local/gradio_api/file=/new.mp4?token=x"}}
	result, err := UniqueNewVideo(before, gallery)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("http://private.local")
	public, _ := url.Parse("https://video.example.com/base")
	mapped, err := RewritePublicURL(result, base, public)
	if err != nil {
		t.Fatal(err)
	}
	if mapped != "https://video.example.com/base/gradio_api/file=/new.mp4?token=x" {
		t.Fatalf("mapped = %s", mapped)
	}
}
