package minimaxv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestSubmitPreservesModelWithoutLegacyOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["model"] != "other-model" {
			t.Errorf("model=%v", payload["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task_id":"task-1"}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, "test", "", server.Client(), 1<<20)
	if _, err := client.Submit(context.Background(), []byte(`{"model":"other-model","content":[{"type":"text","text":"test"}],"resolution":"768P","duration":5,"ratio":"16:9"}`)); err != nil {
		t.Fatal(err)
	}
}
