package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"minimax-h3-tc/internal/domain"
)

var modelNodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
var modelNodeAPINamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func NormalizeModelNode(input domain.ModelNodeInput) (domain.ModelNodeInput, UpstreamConfig, error) {
	if !ValidModelNodeID(input.ID) {
		return domain.ModelNodeInput{}, UpstreamConfig{}, fmt.Errorf("节点 ID 仅支持 1 至 64 位字母、数字、点、下划线或短横线")
	}
	if input.ProtocolVersion != "legacy-gradio-v1" && (strings.TrimSpace(input.ServiceURL) != "" || strings.TrimSpace(input.ProtocolVersion) != "") {
		return normalizeNodeAPIModelNode(input)
	}
	return normalizeLegacyModelNode(input)
}

func normalizeNodeAPIModelNode(input domain.ModelNodeInput) (domain.ModelNodeInput, UpstreamConfig, error) {
	serviceURL, err := parseURL(strings.TrimSpace(input.ServiceURL))
	if err != nil {
		return domain.ModelNodeInput{}, UpstreamConfig{}, fmt.Errorf("service_url: %w", err)
	}
	if path := strings.TrimRight(serviceURL.EscapedPath(), "/"); strings.EqualFold(path, "/ui") {
		return domain.ModelNodeInput{}, UpstreamConfig{}, fmt.Errorf("service_url 不能使用 /ui 页面路径，请填写 Node API 根地址")
	}
	if input.ProtocolVersion == "" {
		input.ProtocolVersion = "h3-node-v1"
	}
	if input.ProtocolVersion != "h3-node-v1" {
		return domain.ModelNodeInput{}, UpstreamConfig{}, fmt.Errorf("protocol_version 仅支持 h3-node-v1")
	}
	if err := validateNodeDurations(input); err != nil {
		return domain.ModelNodeInput{}, UpstreamConfig{}, err
	}
	input.ServiceURL = serviceURL.String()
	return input, UpstreamConfig{
		ID: input.ID, ServiceURL: serviceURL, ProtocolVersion: input.ProtocolVersion,
		PollInterval: input.PollInterval, RequestTimeout: input.RequestTimeout,
	}, nil
}

func normalizeLegacyModelNode(input domain.ModelNodeInput) (domain.ModelNodeInput, UpstreamConfig, error) {
	baseURL, err := parseURL(strings.TrimSpace(input.BaseURL))
	if err != nil {
		return domain.ModelNodeInput{}, UpstreamConfig{}, fmt.Errorf("base_url: %w", err)
	}
	jobsBaseURL, err := parseURL(strings.TrimSpace(input.JobsBaseURL))
	if err != nil {
		return domain.ModelNodeInput{}, UpstreamConfig{}, fmt.Errorf("jobs_base_url: %w", err)
	}
	publicBaseURL, err := parseURL(strings.TrimSpace(input.PublicBaseURL))
	if err != nil {
		return domain.ModelNodeInput{}, UpstreamConfig{}, fmt.Errorf("public_base_url: %w", err)
	}
	if len(input.HealthPath) == 0 || len(input.HealthPath) > 256 || !strings.HasPrefix(input.HealthPath, "/") || strings.ContainsAny(input.HealthPath, "?#") {
		return domain.ModelNodeInput{}, UpstreamConfig{}, fmt.Errorf("health_path 必须是以 / 开头且不含查询参数或片段的路径")
	}
	if !validAPIName(input.SubmitAPIName) {
		return domain.ModelNodeInput{}, UpstreamConfig{}, fmt.Errorf("submit_api_name 格式无效")
	}
	if !validAPIName(input.CheckAPIName) {
		return domain.ModelNodeInput{}, UpstreamConfig{}, fmt.Errorf("check_api_name 格式无效")
	}
	if err := validateNodeDurations(input); err != nil {
		return domain.ModelNodeInput{}, UpstreamConfig{}, err
	}
	input.BaseURL = baseURL.String()
	input.JobsBaseURL = jobsBaseURL.String()
	input.PublicBaseURL = publicBaseURL.String()
	return input, UpstreamConfig{
		ID: input.ID, BaseURL: baseURL, JobsBaseURL: jobsBaseURL, PublicBaseURL: publicBaseURL,
		HealthPath: input.HealthPath, SubmitAPIName: input.SubmitAPIName, CheckAPIName: input.CheckAPIName,
		PollInterval: input.PollInterval, RequestTimeout: input.RequestTimeout,
	}, nil
}

func validateNodeDurations(input domain.ModelNodeInput) error {
	if input.PollInterval < time.Second || input.PollInterval > 5*time.Minute {
		return fmt.Errorf("poll_interval 必须在 1s 到 5m 之间")
	}
	if input.RequestTimeout < time.Second || input.RequestTimeout > 5*time.Minute {
		return fmt.Errorf("request_timeout 必须在 1s 到 5m 之间")
	}
	return nil
}

func ValidModelNodeID(id string) bool {
	return id != "." && id != ".." && modelNodeIDPattern.MatchString(id)
}

func validAPIName(value string) bool {
	return len(value) >= 1 && len(value) <= 128 && modelNodeAPINamePattern.MatchString(value)
}
