package registry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/upstream/gradio"
)

func TestNodeProberChecksGradioAndJobsIndependently(t *testing.T) {
	client := &probeClientFake{jobsErr: errors.New("jobs failed")}
	prober := NodeProber{ClientFactory: func(config.UpstreamConfig) NodeProbeClient { return client }}
	result := prober.Probe(context.Background(), domain.ModelNodeInput{
		ID: "node-1", BaseURL: "http://private.example:7860", JobsBaseURL: "http://jobs.example:8188",
		PublicBaseURL: "https://video.example", HealthPath: "/health", SubmitAPIName: "submit", CheckAPIName: "check",
		PollInterval: time.Second, RequestTimeout: time.Second, Enabled: true,
	})
	if !result.GradioOK || result.JobsOK || result.GradioErrorCode != "" || result.JobsErrorCode != "upstream_jobs_unhealthy" {
		t.Fatalf("result=%+v", result)
	}
	if client.healthPath != "/health" || !client.jobsCalled {
		t.Fatalf("health_path=%q jobs_called=%v", client.healthPath, client.jobsCalled)
	}
}

func TestNodeProberUsesSingleAuthenticatedNodeAPIEndpoint(t *testing.T) {
	const apiKey = "Abcdefghijklmnopqrstuvwx12345678"
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			http.Error(w, `{"error":{"code":"unauthorized","message":"bad key"}}`, http.StatusUnauthorized)
			return
		}
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/internal/v1/health" {
			_, _ = w.Write([]byte(`{"status":"healthy","protocol_version":"h3-node-v1","node_time":1,"components":{},"comfyui_exposure":"loopback_only"}`))
			return
		}
		_, _ = w.Write([]byte(`{"protocol_version":"h3-node-v1","capabilities_revision":"rev-1","stages":["generation"],"scenarios":["t2v"],"ratios":["16:9"]}`))
	}))
	defer server.Close()
	result := (NodeProber{}).ProbeNodeAPI(context.Background(), domain.ModelNodeInput{
		ID: "node-1", ServiceURL: server.URL, ProtocolVersion: "h3-node-v1",
		PollInterval: time.Second, RequestTimeout: time.Second, Enabled: true,
	}, apiKey)
	if !result.Reachable || !result.Authenticated || result.ProtocolVersion != "h3-node-v1" || len(paths) != 2 {
		t.Fatalf("result=%+v paths=%v", result, paths)
	}
}

type probeClientFake struct {
	healthPath string
	jobsCalled bool
	healthErr  error
	jobsErr    error
}

func (c *probeClientFake) Healthy(_ context.Context, healthPath string) error {
	c.healthPath = healthPath
	return c.healthErr
}

func (c *probeClientFake) ListJobs(context.Context) ([]gradio.Job, error) {
	c.jobsCalled = true
	return nil, c.jobsErr
}
