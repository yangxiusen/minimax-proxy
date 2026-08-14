package registry

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/upstream/gradio"
	"minimax-h3-tc/internal/upstream/nodeapi"
)

type NodeProbeClient interface {
	Healthy(context.Context, string) error
	ListJobs(context.Context) ([]gradio.Job, error)
}

type NodeAPIProbeResult struct {
	Reachable           bool
	Authenticated       bool
	ProtocolVersion     string
	Capabilities        map[string]any
	HealthErrorCode     string
	CapabilityErrorCode string
}

func (p NodeProber) ProbeNodeAPI(ctx context.Context, input domain.ModelNodeInput, apiKey string) NodeAPIProbeResult {
	normalized, upstream, err := config.NormalizeModelNode(input)
	if err != nil || !normalized.UsesNodeAPI() || upstream.ServiceURL == nil {
		return NodeAPIProbeResult{HealthErrorCode: "node_config_invalid", CapabilityErrorCode: "node_config_invalid"}
	}
	client := nodeapi.NewClient(upstream.ServiceURL, apiKey, &http.Client{Timeout: upstream.RequestTimeout}, 1<<20)
	health, err := client.Health(ctx, "node-probe-health")
	if err != nil {
		code := "node_unreachable"
		if strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), "HTTP 403") {
			code = "node_authentication_failed"
		}
		return NodeAPIProbeResult{Reachable: code != "node_unreachable", HealthErrorCode: code, CapabilityErrorCode: "capabilities_not_checked"}
	}
	result := NodeAPIProbeResult{Reachable: true, Authenticated: true, ProtocolVersion: health.ProtocolVersion}
	capabilities, err := client.Capabilities(ctx, "node-probe-capabilities")
	if err != nil {
		result.CapabilityErrorCode = "node_capabilities_failed"
		return result
	}
	result.Capabilities = capabilities.Raw
	return result
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
