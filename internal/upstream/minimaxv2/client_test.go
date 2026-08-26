package minimaxv2

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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestClientSubmitsOfficialV2RequestWithoutCallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/video_generation" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer official-key" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "configured-model" || body["resolution"] != "2K" || body["ratio"] != "16:9" {
			t.Fatalf("body=%v", body)
		}
		content, _ := body["content"].([]any)
		if len(content) != 4 {
			t.Fatalf("content=%v", content)
		}
		for index, want := range []string{"data:image/png;base64,AAAA", "data:video/mp4;base64,AAAA", "data:audio/wav;base64,AAAA"} {
			item, _ := content[index+1].(map[string]any)
			kind, _ := item["type"].(string)
			value, _ := item[kind].(map[string]any)
			if value["url"] != want {
				t.Fatalf("content[%d]=%v, want URL %q", index+1, item, want)
			}
		}
		for _, field := range []string{"callback_url", "width", "height", "steps", "loras", "interpolation", "restoration", "profile_id"} {
			if _, exists := body[field]; exists {
				t.Fatalf("official request contains internal field %q: %v", field, body)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"task_id": "upstream-1"})
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, "official-key", "configured-model", server.Client(), 1<<20)

	taskID, err := client.Submit(context.Background(), []byte(`{"model":"north-model","content":[{"type":"text","text":"hello"},{"type":"image_url","role":"reference_image","image_url":{"url":"data:image/png;base64,AAAA"}},{"type":"video_url","role":"reference_video","video_url":{"url":"data:video/mp4;base64,AAAA"}},{"type":"audio_url","role":"reference_audio","audio_url":{"url":"data:audio/wav;base64,AAAA"}}],"resolution":"2K","duration":5,"ratio":"16:9","callback_url":"https://callback.example.com","aigc_watermark":true,"width":2048,"height":1152,"steps":30,"loras":[{"name":"style"}],"interpolation":{"enabled":true},"restoration":{"enabled":true},"profile_id":"internal-profile"}`))
	if err != nil || taskID != "upstream-1" {
		t.Fatalf("Submit()=%q,%v", taskID, err)
	}
}

func TestClientQueriesListsAndDeletes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/query/video_generation/upstream-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"id": "upstream-1", "status": "succeeded", "content": map[string]string{"url": "https://video.example.com/result.mp4"}, "ratio": "16:9"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v2/query/video_generation":
			_ = json.NewEncoder(w).Encode(map[string]any{"tasks": []map[string]any{{"id": "upstream-1", "status": "running"}}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v2/video_generation/upstream-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, "key", "model", server.Client(), 1<<20)

	result, err := client.Query(context.Background(), "upstream-1")
	if err != nil || result.Status != StatusSucceeded || result.Content.URL != "https://video.example.com/result.mp4" {
		t.Fatalf("Query()=%+v,%v", result, err)
	}
	items, err := client.List(context.Background())
	if err != nil || len(items) != 1 || items[0].Status != StatusRunning {
		t.Fatalf("List()=%+v,%v", items, err)
	}
	if err := client.Delete(context.Background(), "upstream-1"); err != nil {
		t.Fatalf("Delete()=%v", err)
	}
}

func TestClientListAcceptsItemsWrapper(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{"id": "upstream-1", "status": "running"}},
			"total": 1,
		})
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, "key", "model", server.Client(), 1<<20)

	items, err := client.List(context.Background())
	if err != nil || len(items) != 1 || items[0].ID != "upstream-1" {
		t.Fatalf("List()=%+v,%v", items, err)
	}
}

func TestClientListPrioritizesTasksAndRejectsMissingWrappers(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    string
		wantID  string
		wantErr bool
	}{
		{name: "tasks takes priority", body: `{"tasks":[{"id":"tasks-1","status":"running"}],"items":[{"id":"items-1","status":"running"}]}`, wantID: "tasks-1"},
		{name: "missing wrappers", body: `{"total":0}`, wantErr: true},
		{name: "tasks must be an array", body: `{"tasks":null,"items":[{"id":"items-1","status":"running"}]}`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			client := NewClient(base, "key", "model", server.Client(), 1<<20)

			items, err := client.List(context.Background())
			if test.wantErr {
				if err == nil {
					t.Fatalf("List()=%+v,nil", items)
				}
				return
			}
			if err != nil || len(items) != 1 || items[0].ID != test.wantID {
				t.Fatalf("List()=%+v,%v", items, err)
			}
		})
	}
}

func TestClientRejectsMalformedAndOversizedResponses(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing task id", body: `{}`},
		{name: "oversized", body: `{"task_id":"` + strings.Repeat("a", 128) + `"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(test.body)) }))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			client := NewClient(base, "key", "model", server.Client(), 64)
			if _, err := client.Submit(context.Background(), []byte(`{"content":[]}`)); err == nil {
				t.Fatal("Submit() unexpectedly succeeded")
			}
		})
	}
}

func TestClientReturnsSanitizedHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authorized_error","message":"secret upstream detail","http_code":"401"},"request_id":"req-1"}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, "key", "model", server.Client(), 1<<20)
	_, err := client.List(context.Background())
	httpErr, ok := err.(*HTTPError)
	if !ok || httpErr.StatusCode != http.StatusUnauthorized || httpErr.Code != "" || httpErr.Type != "authorized_error" || httpErr.RequestID != "req-1" {
		t.Fatalf("error=%#v", err)
	}
	if httpErr.Error() == "" || httpErr.Error() == "secret upstream detail" {
		t.Fatalf("unsafe error=%q", httpErr.Error())
	}
}

func TestClientSanitizesFailedTaskFeedback(t *testing.T) {
	for _, test := range []struct {
		name, message, want, forbidden string
	}{
		{name: "API key", message: "prefix official-key suffix", want: "prefix [redacted] suffix", forbidden: "official-key"},
		{name: "data URI", message: "prefix data:image/png;base64,PRIVATE", want: "[redacted-embedded-data-uri]", forbidden: "PRIVATE"},
		{name: "length cap", message: strings.Repeat("x", 2048), want: strings.Repeat("x", 1024)},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{
					"id": "upstream-1", "status": "failed",
					"error": map[string]string{"code": "1027", "message": test.message},
				}})
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			client := NewClient(base, "official-key", "model", server.Client(), 1<<20)

			task, err := client.Query(context.Background(), "upstream-1")
			if err != nil {
				t.Fatal(err)
			}
			if task.Error == nil || task.Error.Code != "1027" || task.Error.Message != test.want ||
				(test.forbidden != "" && strings.Contains(task.Error.Message, test.forbidden)) {
				t.Fatalf("task error=%+v", task.Error)
			}
		})
	}
}

func TestClientReturnsStructuredUpstreamFeedback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"type":"error","error":{"code":"1027","type":"unprocessable_entity_error","message":"text content contains sensitive content (1027)","resource_type":"text"},"request_id":"req-sensitive"}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, "key", "model", server.Client(), 1<<20)

	_, err := client.Submit(context.Background(), validSubmitBody())
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("error=%#v", err)
	}
	if httpErr.StatusCode != http.StatusUnprocessableEntity || httpErr.Code != "1027" ||
		httpErr.Type != "unprocessable_entity_error" || httpErr.Message != "text content contains sensitive content (1027)" ||
		httpErr.ResourceType != "text" || httpErr.RequestID != "req-sensitive" {
		t.Fatalf("HTTP error=%+v", httpErr)
	}
}

func TestClientLogsSanitizedOfficialSubmitRequestAndResponse(t *testing.T) {
	payload := []byte("private-image-bytes")
	encoded := base64.StdEncoding.EncodeToString(payload)
	dataURI := "data:image/png;base64," + encoded
	receivedBody := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		receivedBody <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task_id":"upstream-1","request_id":"req-success","api_key":"official-secret-key","output_url":"https://video.example.com/result.mp4"}`))
	}))
	defer server.Close()

	var logs bytes.Buffer
	base, _ := url.Parse(server.URL)
	client := NewClient(base, "official-secret-key", "configured-model", server.Client(), 1<<20)
	client.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	requestBody, err := json.Marshal(map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "diagnostic prompt"},
			map[string]any{"type": "image_url", "role": "reference_image", "image_url": map[string]string{"url": dataURI}},
			map[string]any{"type": "video_url", "role": "reference_video", "video_url": map[string]string{"url": "https://media.example.com/reference.mp4"}},
			map[string]any{"type": "audio_url", "role": "reference_audio", "audio_url": map[string]string{"url": "mm_file:/inputs/reference.wav"}},
		},
		"resolution": "2K",
		"duration":   8,
		"ratio":      "16:9",
	})
	if err != nil {
		t.Fatal(err)
	}

	taskID, err := client.Submit(WithProxyTaskID(context.Background(), "proxy-123"), requestBody)
	if err != nil || taskID != "upstream-1" {
		t.Fatalf("Submit()=%q,%v", taskID, err)
	}
	if got := string(<-receivedBody); !strings.Contains(got, dataURI) {
		t.Fatalf("official request lost data URI: %s", got)
	}

	digest := sha256.Sum256(payload)
	output := logs.String()
	for _, want := range []string{
		`"stage":"official_submit"`, `"event":"request"`, `"event":"response"`,
		`"task_id":"proxy-123"`, `"model":"configured-model"`,
		`"resolution":"2K"`, `"duration":8`, `"ratio":"16:9"`,
		`"text":"diagnostic prompt"`, `"role":"reference_image"`,
		`"source":"data_uri"`, `"media_type":"image/png"`,
		`"decoded_bytes":19`, `"sha256":"` + hex.EncodeToString(digest[:]) + `"`,
		`https://media.example.com/reference.mp4`, `mm_file:/inputs/reference.wav`,
		`"status_code":200`, `req-success`, `upstream-1`, `https://video.example.com/result.mp4`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("logs missing %q:\n%s", want, output)
		}
	}
	for _, forbidden := range []string{encoded, "official-secret-key", `"Authorization"`} {
		if strings.Contains(output, forbidden) {
			t.Errorf("logs contain secret %q:\n%s", forbidden, output)
		}
	}
}

func TestClientRedactsEmbeddedDataURIFromOfficialLogs(t *testing.T) {
	secretPayload := base64.StdEncoding.EncodeToString([]byte("embedded-private-bytes"))
	embedded := "prefix data:image/png;base64," + secretPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"task_id": "upstream-1", "echo": embedded})
	}))
	defer server.Close()

	var logs bytes.Buffer
	base, _ := url.Parse(server.URL)
	client := NewClient(base, "key", "model", server.Client(), 1<<20)
	client.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	requestBody, err := json.Marshal(map[string]any{
		"content":    []any{map[string]string{"type": "text", "text": embedded}},
		"resolution": "2K",
		"duration":   8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Submit(context.Background(), requestBody); err != nil {
		t.Fatal(err)
	}
	output := logs.String()
	if strings.Contains(output, secretPayload) || strings.Contains(output, embedded) {
		t.Fatalf("logs contain embedded data URI:\n%s", output)
	}
	if !strings.Contains(output, "[redacted-embedded-data-uri]") {
		t.Fatalf("logs do not mark embedded data URI redaction:\n%s", output)
	}
}

func TestClientLogsOfficialSubmitHTTPFailureResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_params","message":"resolution is unsupported"},"request_id":"req-failure"}`))
	}))
	defer server.Close()

	var logs bytes.Buffer
	base, _ := url.Parse(server.URL)
	client := NewClient(base, "official-secret-key", "configured-model", server.Client(), 1<<20)
	client.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	_, err := client.Submit(WithProxyTaskID(context.Background(), "proxy-400"), validSubmitBody())
	if err == nil {
		t.Fatal("Submit() unexpectedly succeeded")
	}
	output := logs.String()
	for _, want := range []string{
		`"level":"ERROR"`, `"event":"response"`, `"task_id":"proxy-400"`,
		`"status_code":400`, `"request_id":"req-failure"`,
		`"error_type":"invalid_params"`, `resolution is unsupported`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("logs missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "official-secret-key") {
		t.Fatalf("logs contain API key:\n%s", output)
	}
}

func TestClientLogsOfficialSubmitNetworkFailure(t *testing.T) {
	var logs bytes.Buffer
	base, _ := url.Parse("https://official.example.com")
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp 10.0.0.1:443: connection refused")
	})}
	client := NewClient(base, "official-secret-key", "configured-model", httpClient, 1<<20)
	client.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	_, err := client.Submit(WithProxyTaskID(context.Background(), "proxy-network"), validSubmitBody())
	if err == nil {
		t.Fatal("Submit() unexpectedly succeeded")
	}
	output := logs.String()
	for _, want := range []string{`"level":"ERROR"`, `"event":"response"`, `"task_id":"proxy-network"`, `connection refused`, `[redacted-address]`} {
		if !strings.Contains(output, want) {
			t.Errorf("logs missing %q:\n%s", want, output)
		}
	}
	for _, forbidden := range []string{"10.0.0.1", "official-secret-key"} {
		if strings.Contains(output, forbidden) {
			t.Errorf("logs contain secret %q:\n%s", forbidden, output)
		}
	}
}

func TestClientCapsOfficialSubmitResponseLog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":      map[string]string{"type": "invalid_params", "message": strings.Repeat("x", 40<<10)},
			"request_id": "req-large",
		})
	}))
	defer server.Close()

	var logs bytes.Buffer
	base, _ := url.Parse(server.URL)
	client := NewClient(base, "key", "model", server.Client(), 1<<20)
	client.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	_, _ = client.Submit(context.Background(), validSubmitBody())
	output := logs.String()
	if !strings.Contains(output, `"response_truncated":true`) {
		t.Fatalf("response log was not marked truncated:\n%s", output)
	}
	if logs.Len() >= 40<<10 {
		t.Fatalf("response log was not capped: %d bytes", logs.Len())
	}
}

func validSubmitBody() []byte {
	return []byte(`{"content":[{"type":"text","text":"hello"}],"resolution":"2K","duration":8,"ratio":"16:9"}`)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
