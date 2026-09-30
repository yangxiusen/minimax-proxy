package remote_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"minimax-h3-tc/internal/domain"
	"net/http"
	"strings"
	"testing"
)

func TestIntegrationRequestedDurationAndProxyTaskID(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested int
		actual    float64
	}{{"seven", 7, 7.059}, {"ten", 10, 10.055}, {"omitted", 0, 5.062}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIntegrationFixture(t, "doubao-seedance-2-0-mini-260615")
			var logs bytes.Buffer
			f.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			f.wire()
			var input map[string]any
			if err := json.Unmarshal(integrationBody(t, true), &input); err != nil {
				t.Fatal(err)
			}
			input["model"] = f.upstream.model
			input["content"].([]any)[1].(map[string]any)["role"] = "reference_image"
			if tc.requested == 0 {
				delete(input, "duration")
			} else {
				input["duration"] = tc.requested
			}
			body, _ := json.Marshal(input)
			response := f.request(http.MethodPost, "/v2/video_generation", "key-a", body)
			var created map[string]string
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &created) != nil || created["task_id"] == "" || created["id"] != "" {
				t.Fatalf("incompatible create response: %d %s", response.Code, response.Body.String())
			}
			id := created["task_id"]
			want := tc.requested
			if want == 0 {
				want = 5
			}
			if task := f.task(id); task.Duration != want {
				t.Fatalf("persisted duration=%d want=%d", task.Duration, want)
			}
			if err := f.processor.ProcessOne(context.Background()); !errors.Is(err, domain.ErrRemotePending) {
				t.Fatal(err)
			}
			run, err := f.store.GetRemoteRun(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if run.UpstreamTaskID == "" || run.UpstreamTaskID == id {
				t.Fatal("upstream id was not associated with a distinct proxy id")
			}
			f.upstream.mu.Lock()
			submitted := f.upstream.submitBodies[0]
			f.upstream.actualDuration = tc.actual
			f.upstream.mu.Unlock()
			var forwarded domain.GenerationRequest
			if err := json.Unmarshal([]byte(submitted), &forwarded); err != nil {
				t.Fatal(err)
			}
			if forwarded.Duration != want || forwarded.Model != f.upstream.model || forwarded.Content[1].Role != "reference_image" {
				t.Fatalf("forwarded=%+v", forwarded)
			}
			f.upstream.setStatus(id, "succeeded")
			if err := f.processor.ProcessTask(context.Background(), f.task(id)); err != nil {
				t.Fatal(err)
			}
			got := f.publicTask(id)
			if got.ID != id || got.Duration == nil || *got.Duration != tc.actual {
				t.Fatalf("actual response=%+v", got)
			}
			checkDurationLogs(t, logs.String(), id, run.UpstreamTaskID, want, tc.requested != 0)
		})
	}
}

func checkDurationLogs(t *testing.T, logs, id, upstreamID string, want int, provided bool) {
	t.Helper()
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) != nil {
			t.Fatal("invalid diagnostic JSON")
		}
		switch {
		case row["stage"] == "proxy_create" && row["event"] == "request":
			seen["inbound"] = row["duration_provided"] == provided
		case row["stage"] == "proxy_create" && row["event"] == "normalized":
			source := "request"
			if !provided {
				source = "default"
			}
			seen["duration"] = row["effective_duration"] == float64(want) && row["duration_source"] == source
			if !provided && row["level"] != "WARN" {
				t.Fatal("default duration is not visible as warning")
			}
		case row["stage"] == "proxy_create" && row["event"] == "response":
			body, _ := row["response"].(map[string]any)
			seen["compatible_response"] = body["task_id"] == id
		case row["stage"] == "tk2sd_api" && row["operation"] == "create" && row["event"] == "request":
			body, _ := row["request"].(map[string]any)
			seen["outbound"] = row["task_id"] == id && body["duration"] == float64(want)
		case row["event"] == "task_id_mapping":
			seen["mapping"] = row["task_id"] == id && row["upstream_task_id"] == upstreamID
		}
	}
	for _, key := range []string{"inbound", "duration", "compatible_response", "outbound", "mapping"} {
		if !seen[key] {
			t.Fatalf("missing %s diagnostic", key)
		}
	}
	for _, secret := range []string{integrationNodeKey, "Local integration fixture", "base64,", "signature=local-signed"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("sensitive payload in diagnostics: %s", secret)
		}
	}
}

func TestIntegrationExplicitInvalidDurationIsNotDefaulted(t *testing.T) {
	for _, value := range []any{nil, 0, 3, 16, 10.5, "10"} {
		f := newIntegrationFixture(t, "doubao-seedance-2-0-mini-260615")
		var input map[string]any
		_ = json.Unmarshal(integrationBody(t, true), &input)
		input["model"] = f.upstream.model
		input["duration"] = value
		body, _ := json.Marshal(input)
		response := f.request(http.MethodPost, "/v2/video_generation", "key-a", body)
		if response.Code != 400 {
			t.Fatalf("invalid duration=%v status=%d", value, response.Code)
		}
		_, count, err := f.store.List(context.Background(), "owner-a", domain.TaskFilter{})
		if err != nil || count != 0 {
			t.Fatalf("invalid duration created a task: %v %d", err, count)
		}
	}
}
