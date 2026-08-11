package config

import (
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

func TestNormalizeModelNodeValidatesAndCanonicalizesConfiguration(t *testing.T) {
	input := domain.ModelNodeInput{
		ID: "gpu-1", BaseURL: "http://127.0.0.1:7860/", JobsBaseURL: "http://127.0.0.1:8188/",
		PublicBaseURL: "https://video.example.com/", HealthPath: "/",
		SubmitAPIName: "submit_minimax_from_slots", CheckAPIName: "check_and_get_video",
		PollInterval: time.Second, RequestTimeout: 5 * time.Minute, Enabled: true,
	}
	normalized, upstream, err := NormalizeModelNode(input)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.BaseURL != "http://127.0.0.1:7860" || normalized.JobsBaseURL != "http://127.0.0.1:8188" || normalized.PublicBaseURL != "https://video.example.com" {
		t.Fatalf("normalized = %+v", normalized)
	}
	if upstream.ID != input.ID || upstream.RequestTimeout != 5*time.Minute || upstream.BaseURL.String() != normalized.BaseURL {
		t.Fatalf("upstream = %+v", upstream)
	}
}

func TestNormalizeModelNodeRejectsInvalidFields(t *testing.T) {
	valid := domain.ModelNodeInput{
		ID: "gpu-1", BaseURL: "http://127.0.0.1:7860", JobsBaseURL: "http://127.0.0.1:8188",
		PublicBaseURL: "https://video.example.com", HealthPath: "/",
		SubmitAPIName: "submit_minimax_from_slots", CheckAPIName: "check_and_get_video",
		PollInterval: 3 * time.Second, RequestTimeout: 30 * time.Second, Enabled: true,
	}
	tests := []struct {
		name string
		edit func(*domain.ModelNodeInput)
		want string
	}{
		{name: "id", edit: func(v *domain.ModelNodeInput) { v.ID = "bad id" }, want: "节点 ID"},
		{name: "credentials", edit: func(v *domain.ModelNodeInput) { v.BaseURL = "http://user:pass@127.0.0.1" }, want: "凭据"},
		{name: "query", edit: func(v *domain.ModelNodeInput) { v.JobsBaseURL += "?secret=1" }, want: "查询参数"},
		{name: "health path", edit: func(v *domain.ModelNodeInput) { v.HealthPath = "health" }, want: "health_path"},
		{name: "api name", edit: func(v *domain.ModelNodeInput) { v.SubmitAPIName = "bad/name" }, want: "submit_api_name"},
		{name: "poll too short", edit: func(v *domain.ModelNodeInput) { v.PollInterval = time.Millisecond }, want: "poll_interval"},
		{name: "timeout too long", edit: func(v *domain.ModelNodeInput) { v.RequestTimeout = 6 * time.Minute }, want: "request_timeout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.edit(&input)
			if _, _, err := NormalizeModelNode(input); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NormalizeModelNode() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
