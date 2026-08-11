package registry

import (
	"context"
	"net/http"
	"sync"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/upstream/gradio"
)

type NodeProbeClient interface {
	Healthy(context.Context, string) error
	ListJobs(context.Context) ([]gradio.Job, error)
}

type NodeProbeResult struct {
	GradioOK        bool
	GradioErrorCode string
	JobsOK          bool
	JobsErrorCode   string
}

type NodeProber struct {
	ClientFactory func(config.UpstreamConfig) NodeProbeClient
}

func (p NodeProber) Probe(ctx context.Context, input domain.ModelNodeInput) NodeProbeResult {
	_, upstream, err := config.NormalizeModelNode(input)
	if err != nil {
		return NodeProbeResult{GradioErrorCode: "node_config_invalid", JobsErrorCode: "node_config_invalid"}
	}
	clientFactory := p.ClientFactory
	if clientFactory == nil {
		clientFactory = func(upstream config.UpstreamConfig) NodeProbeClient {
			return gradio.NewClientWithJobs(upstream.BaseURL, upstream.JobsBaseURL, &http.Client{Timeout: upstream.RequestTimeout}, 1<<20)
		}
	}
	client := clientFactory(upstream)
	if client == nil {
		return NodeProbeResult{GradioErrorCode: "upstream_unhealthy", JobsErrorCode: "upstream_jobs_unhealthy"}
	}
	var result NodeProbeResult
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		if err := client.Healthy(ctx, upstream.HealthPath); err != nil {
			result.GradioErrorCode = "upstream_unhealthy"
			return
		}
		result.GradioOK = true
	}()
	go func() {
		defer group.Done()
		if _, err := client.ListJobs(ctx); err != nil {
			result.JobsErrorCode = "upstream_jobs_unhealthy"
			return
		}
		result.JobsOK = true
	}()
	group.Wait()
	return result
}
