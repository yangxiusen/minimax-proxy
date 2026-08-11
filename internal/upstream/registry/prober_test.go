package registry

import (
	"context"
	"errors"
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
