package remote

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/upstream/tk2sd"
)

func TestSubmitHTTPRecoveryWithRealClientAndPersistentAssets(t *testing.T) {
	p, s, _, task, _ := processorFixture(t)
	var creates, uploads int
	var savedBody, savedKey string
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing node authorization")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/assets":
			uploads++
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			defer r.MultipartForm.RemoveAll()
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			defer file.Close()
			data, err := io.ReadAll(file)
			if err != nil {
				t.Error(err)
			}
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(tk2sd.Asset{ID: assetID, URL: "asset://" + assetID, Kind: "image", Filename: header.Filename, Width: 1280, Height: 720, Size: int64(len(data))})
		case "POST /api/v3/contents/generations/tasks":
			creates++
			body, _ := io.ReadAll(r.Body)
			if creates == 1 {
				savedBody, savedKey = string(body), r.Header.Get("Idempotency-Key")
				w.Write([]byte("{"))
				return
			}
			if savedBody != string(body) || savedKey != r.Header.Get("Idempotency-Key") {
				t.Error("recovery changed key or uploaded assets")
			}
			json.NewEncoder(w).Encode(map[string]string{"id": upstreamID})
		case "GET /api/v3/contents/generations/tasks/" + upstreamID:
			json.NewEncoder(w).Encode(map[string]any{"id": upstreamID, "model": "seed-model", "status": "succeeded", "duration": 5, "content": map[string]string{"video_url": serverURL + "/media/tasks/" + upstreamID + "?expires=9999999999&signature=signed"}})
		case "GET /v1/tasks/" + upstreamID:
			json.NewEncoder(w).Encode(map[string]any{"id": upstreamID, "status": "succeeded", "content": tk2sd.Metadata{Duration: 5.062, Width: 1280, Height: 720}})
		default:
			t.Error("unexpected endpoint", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	serverURL = server.URL
	p.NodeURL, _ = url.Parse(server.URL)
	p.Client = tk2sd.NewClient(p.NodeURL, "test-key", &http.Client{Timeout: 2 * time.Second}, 1<<20)
	task.RequestJSON = imageRequest("data:image/png;base64," + base64.StdEncoding.EncodeToString(pngInput))
	if err := p.ProcessTask(context.Background(), task); !errors.Is(err, domain.ErrRemotePending) {
		t.Fatal(err)
	}
	if err := p.ProcessTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(context.Background(), "owner", task.TaskID)
	if creates != 2 || uploads != 1 || got.Status != domain.StatusSucceeded || got.MetadataStatus != "ready" {
		t.Fatal(creates, uploads, got.Status, got.MetadataStatus)
	}
}
