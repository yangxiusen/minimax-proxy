package nodeapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testNodeAPIKey = "Abcdefghijklmnopqrstuvwx12345678"

func TestArtifactMetadataAndRangeContentStayStreaming(t *testing.T) {
	payload := []byte("0123456789")
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testNodeAPIKey {
			t.Fatalf("Authorization=%q", got)
		}
		switch r.URL.Path {
		case "/internal/v1/artifacts/art_1":
			_ = json.NewEncoder(w).Encode(Artifact{ArtifactID: "art_1", Kind: "video", SizeBytes: int64(len(payload)), SHA256: digest, State: "active"})
		case "/internal/v1/artifacts/art_1/content":
			if got := r.Header.Get("Range"); got != "bytes=2-5" {
				t.Fatalf("Range=%q", got)
			}
			w.Header().Set("Content-Range", "bytes 2-5/10")
			w.Header().Set("Content-Length", "4")
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("ETag", `"`+digest+`"`)
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[2:6])
		default:
			t.Fatalf("path=%q", r.URL.Path)
		}
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, testNodeAPIKey, server.Client(), 1<<20)

	metadata, err := client.GetArtifact(context.Background(), "request-1", "art_1")
	if err != nil || metadata.SHA256 != digest {
		t.Fatalf("GetArtifact()=%+v, %v", metadata, err)
	}
	content, err := client.GetArtifactContent(context.Background(), "request-2", "art_1", "bytes=2-5")
	if err != nil {
		t.Fatal(err)
	}
	defer content.Body.Close()
	data, err := io.ReadAll(content.Body)
	if err != nil || string(data) != "2345" || content.StatusCode != http.StatusPartialContent || content.ContentRange != "bytes 2-5/10" || content.ContentLength != 4 {
		t.Fatalf("content=%+v body=%q err=%v", content, data, err)
	}
}

func TestImportArtifactStreamsMultipartAndCarriesIntegrityMetadata(t *testing.T) {
	payload := bytes.Repeat([]byte("streamed-content"), 64*1024)
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testNodeAPIKey {
			t.Fatalf("Authorization=%q", got)
		}
		if r.Header.Get("Idempotency-Key") != "import-1" {
			t.Fatalf("Idempotency-Key=%q", r.Header.Get("Idempotency-Key"))
		}
		reader, err := multipart.NewReader(r.Body, boundary(r.Header.Get("Content-Type"))).ReadForm(2 << 20)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.RemoveAll()
		if reader.Value["operation_id"][0] != "import-1" || reader.Value["source_artifact_id"][0] != "source-1" || reader.Value["expected_sha256"][0] != digest {
			t.Fatalf("form=%+v", reader.Value)
		}
		file, err := reader.File["file"][0].Open()
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(data, payload) {
			t.Fatalf("uploaded=%d err=%v", len(data), err)
		}
		_ = json.NewEncoder(w).Encode(Artifact{ArtifactID: "target-1", Kind: "video", SizeBytes: int64(len(payload)), SHA256: digest, State: "active"})
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, testNodeAPIKey, server.Client(), 1<<20)

	result, err := client.ImportArtifact(context.Background(), "request-1", ImportArtifactRequest{
		OperationID: "import-1", SourceArtifactID: "source-1", ExpectedSize: int64(len(payload)),
		ExpectedSHA256: digest, Kind: "video", Filename: "source.mp4", Content: bytes.NewReader(payload),
	})
	if err != nil || result.ArtifactID != "target-1" {
		t.Fatalf("ImportArtifact()=%+v, %v", result, err)
	}
}

func boundary(contentType string) string {
	_, parameters, _ := mime.ParseMediaType(contentType)
	return parameters["boundary"]
}

func TestClientUsesSingleServiceURLAndBearerKey(t *testing.T) {
	queueRunning, queuePending := 1, 2
	memoryTotal, memoryFree := int64(1000), int64(250)
	vramTotal, vramFree := int64(800), int64(200)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/base/internal/v1/health" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+testNodeAPIKey {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Request-Id") != "req-1" {
			t.Fatalf("request id=%q", r.Header.Get("X-Request-Id"))
		}
		_ = json.NewEncoder(w).Encode(Health{
			Status: "healthy", ProtocolVersion: ProtocolVersion,
			Runtime: &HealthRuntime{
				QueueRunning: &queueRunning, QueuePending: &queuePending,
				MemoryTotalBytes: &memoryTotal, MemoryFreeBytes: &memoryFree,
				VRAMTotalBytes: &vramTotal, VRAMFreeBytes: &vramFree,
			},
		})
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL + "/base")
	client := NewClient(base, testNodeAPIKey, server.Client(), 1<<20)

	health, err := client.Health(context.Background(), "req-1")
	if err != nil || health.ProtocolVersion != ProtocolVersion || health.Runtime == nil || *health.Runtime.QueuePending != 2 || *health.Runtime.VRAMTotalBytes != 800 {
		t.Fatalf("Health()=%+v, %v", health, err)
	}
}

func TestHealthAcceptsRuntimeDevicesAndExecutionSlots(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"status": "healthy",
			"protocol_version": "h3-node-v1",
			"node_time": 1797299513,
			"components": {"comfyui": "healthy"},
			"comfyui_exposure": "private",
			"runtime": {
				"queue_running": 1,
				"queue_pending": 0,
				"memory_total_bytes": 1000,
				"memory_free_bytes": 500,
				"vram_total_bytes": 800,
				"vram_free_bytes": 300,
				"cpu_percent": 12.5,
				"gpu_percent": 34.5,
				"devices": [
					{
						"index": 0,
						"name": "NVIDIA",
						"vram_total_bytes": 800,
						"vram_free_bytes": 300,
						"gpu_percent": 34.5
					}
				],
				"execution_slots": {
					"used": 1,
					"capacity": 1
				}
			}
		}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, testNodeAPIKey, server.Client(), 1<<20)

	health, err := client.Health(context.Background(), "req-1")
	if err != nil {
		t.Fatalf("Health() error=%v", err)
	}
	if health.Runtime == nil || health.Runtime.GPUPercent == nil || *health.Runtime.GPUPercent != 34.5 {
		t.Fatalf("runtime=%+v", health.Runtime)
	}
	if len(health.Runtime.Devices) != 1 || health.Runtime.Devices[0].Name != "NVIDIA" || health.Runtime.ExecutionSlots == nil || health.Runtime.ExecutionSlots.Capacity != 1 {
		t.Fatalf("runtime=%+v", health.Runtime)
	}
}

func TestCreateExecutionSendsIdempotencyAndStrictJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/executions" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") != "op_1" {
			t.Fatalf("idempotency=%q", r.Header.Get("Idempotency-Key"))
		}
		var request ExecutionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(ExecutionReference{ExecutionID: "exe_1", Status: "accepted", OperationID: "op_1"})
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, testNodeAPIKey, server.Client(), 1<<20)

	response, err := client.CreateExecution(context.Background(), "req-1", ExecutionRequest{
		OperationID: "op_1", ExternalTaskID: "task_1", StageID: "stage_1", StageType: "generation",
		InputArtifacts: []InputArtifact{}, Parameters: json.RawMessage(`{"scenario":"t2va"}`),
	})
	if err != nil || response.ExecutionID != "exe_1" {
		t.Fatalf("CreateExecution()=%+v, %v", response, err)
	}
}

func TestGetExecutionAcceptsCompleteNodeResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"execution_id":"exe_1","operation_id":"op_1","external_task_id":"task_1","stage_id":"stage_1","stage_type":"generation","status":"running","comfy_job_id":"job_1","progress_json":"{}","result_artifact_id":null,"error_code":null,"error_message":null,"cancel_requested_at":null,"created_at":1,"updated_at":2,"started_at":1,"heartbeat_at":2,"finished_at":null,"error":null}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, testNodeAPIKey, server.Client(), 1<<20)

	result, err := client.GetExecution(context.Background(), "request-1", "exe_1")
	if err != nil || result.ExecutionID != "exe_1" || result.HeartbeatAt != 2 {
		t.Fatalf("GetExecution()=%+v, %v", result, err)
	}
}

func TestClientRejectsOversizedAndErrorResponsesWithoutLeakingKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(strings.Repeat("x", 512)))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, testNodeAPIKey, server.Client(), 64)

	_, err := client.Health(context.Background(), "req-1")
	if err == nil || !strings.Contains(err.Error(), "响应体超过限制") || strings.Contains(err.Error(), testNodeAPIKey) {
		t.Fatalf("Health() error=%v", err)
	}
}

func TestClientNeverForwardsNodeKeyAcrossRedirect(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected = true
		if r.Header.Get("Authorization") != "" {
			t.Fatalf("node key leaked to redirect target")
		}
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	base, _ := url.Parse(source.URL)
	client := NewClient(base, testNodeAPIKey, source.Client(), 1<<20)
	if _, err := client.Health(context.Background(), "request"); err == nil {
		t.Fatal("redirect response was accepted")
	}
	if redirected {
		t.Fatal("redirect target was contacted")
	}
}

func TestDeleteArtifactsUsesOperationAsIdempotencyKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/artifacts/delete" || r.Header.Get("Idempotency-Key") != "delete-1" {
			t.Fatalf("request=%s idempotency=%q", r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		_ = json.NewEncoder(w).Encode(DeleteArtifactsResult{
			OperationID: "delete-1",
			Items:       []DeleteArtifactItem{{ArtifactID: "node-artifact", Status: "deleted", DeletedBytes: 42}},
		})
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, testNodeAPIKey, server.Client(), 1<<20)

	result, err := client.DeleteArtifacts(context.Background(), "request-1", DeleteArtifactsRequest{
		OperationID: "delete-1", ArtifactIDs: []string{"node-artifact"},
	})
	if err != nil || len(result.Items) != 1 || result.Items[0].DeletedBytes != 42 {
		t.Fatalf("DeleteArtifacts()=%+v, %v", result, err)
	}
}
