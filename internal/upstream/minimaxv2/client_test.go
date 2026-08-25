package minimaxv2

import (
	"context"
	"encoding/json"
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

	taskID, err := client.Submit(context.Background(), []byte(`{"model":"north-model","content":[{"type":"text","text":"hello"}],"resolution":"2K","duration":5,"ratio":"16:9","callback_url":"https://callback.example.com","aigc_watermark":true,"width":2048,"height":1152,"steps":30,"loras":[{"name":"style"}],"interpolation":{"enabled":true},"restoration":{"enabled":true},"profile_id":"internal-profile"}`))
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
	if !ok || httpErr.StatusCode != http.StatusUnauthorized || httpErr.Code != "authorized_error" || httpErr.RequestID != "req-1" {
		t.Fatalf("error=%#v", err)
	}
	if httpErr.Error() == "" || httpErr.Error() == "secret upstream detail" {
		t.Fatalf("unsafe error=%q", httpErr.Error())
	}
}
