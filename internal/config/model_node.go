package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"minimax-h3-tc/internal/domain"
)

var modelNodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var modelNodeAPINamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func NormalizeModelNode(input domain.ModelNodeInput) (domain.ModelNodeInput, UpstreamConfig, error) {
	if !ValidModelNodeID(input.ID) {
		return domain.ModelNodeInput{}, UpstreamConfig{}, fmt.Errorf("节点 ID 格式无效")
	}
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
	if input.PollInterval < time.Second || input.PollInterval > 5*time.Minute {
		return domain.ModelNodeInput{}, UpstreamConfig{}, fmt.Errorf("poll_interval 必须在 1s 到 5m 之间")
	}
	if input.RequestTimeout < time.Second || input.RequestTimeout > 5*time.Minute {
		return domain.ModelNodeInput{}, UpstreamConfig{}, fmt.Errorf("request_timeout 必须在 1s 到 5m 之间")
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

func ValidModelNodeID(id string) bool {
	return modelNodeIDPattern.MatchString(id)
}

func validAPIName(value string) bool {
	return len(value) >= 1 && len(value) <= 128 && modelNodeAPINamePattern.MatchString(value)
}
