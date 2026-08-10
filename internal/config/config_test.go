package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestLoadExpandsEnvironmentAndAppliesDefaults(t *testing.T) {
	t.Setenv("TEST_MINIMAX_KEY", "secret-a")
	t.Setenv("TEST_UPSTREAM_URL", "http://127.0.0.1:7860")

	cfg, err := Load(writeConfig(t, validYAML(t)))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.Address != ":8080" {
		t.Fatalf("Server.Address = %q", cfg.Server.Address)
	}
	if cfg.Queue.ProtectedSlots != 3 || cfg.Queue.PerKeyUnfinishedLimit != 10 {
		t.Fatalf("Queue defaults = %+v", cfg.Queue)
	}
	if cfg.Task.Retention != 7*24*time.Hour || cfg.Task.IdempotencyTTL != 24*time.Hour {
		t.Fatalf("Task defaults = %+v", cfg.Task)
	}
	if cfg.Admin.Username != "admin" || cfg.Admin.Password != "123" {
		t.Fatalf("Admin credentials defaults = %+v", cfg.Admin)
	}
	if cfg.Admin.SessionTTL != 12*time.Hour || cfg.Admin.MonitorInterval != 5*time.Second {
		t.Fatalf("Admin duration defaults = %+v", cfg.Admin)
	}
	if got := cfg.APIKeys[0].Key; got != "secret-a" {
		t.Fatalf("expanded key = %q", got)
	}
	if got := cfg.Upstreams[0].BaseURL.String(); got != "http://127.0.0.1:7860" {
		t.Fatalf("expanded upstream = %q", got)
	}
	if cfg.Upstreams[0].SubmitAPIName != "submit_minimax_from_slots" {
		t.Fatalf("SubmitAPIName = %q", cfg.Upstreams[0].SubmitAPIName)
	}
}

func TestLoadExpandsAdminEnvironmentAndParsesDurations(t *testing.T) {
	t.Setenv("TEST_MINIMAX_KEY", "secret-a")
	t.Setenv("TEST_UPSTREAM_URL", "http://127.0.0.1:7860")
	t.Setenv("TEST_ADMIN_USERNAME", "operator")
	t.Setenv("TEST_ADMIN_PASSWORD", "strong-password")
	yaml := "admin:\n  username: ${TEST_ADMIN_USERNAME}\n  password: ${TEST_ADMIN_PASSWORD}\n  session_ttl: 30m\n  monitor_interval: 2s\n" + validYAML(t)

	cfg, err := Load(writeConfig(t, yaml))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Admin.Username != "operator" || cfg.Admin.Password != "strong-password" {
		t.Fatalf("Admin credentials = %+v", cfg.Admin)
	}
	if cfg.Admin.SessionTTL != 30*time.Minute || cfg.Admin.MonitorInterval != 2*time.Second {
		t.Fatalf("Admin durations = %+v", cfg.Admin)
	}
}

func TestLoadAdminSecureCookie(t *testing.T) {
	t.Setenv("TEST_MINIMAX_KEY", "secret-a")
	t.Setenv("TEST_UPSTREAM_URL", "http://127.0.0.1:7860")
	yaml := "admin:\n  secure_cookie: true\n" + validYAML(t)
	cfg, err := Load(writeConfig(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Admin.SecureCookie {
		t.Fatal("admin.secure_cookie was not loaded")
	}
}

func TestLoadRejectsInvalidAdminConfig(t *testing.T) {
	t.Setenv("TEST_MINIMAX_KEY", "secret-a")
	t.Setenv("TEST_UPSTREAM_URL", "http://127.0.0.1:7860")
	tests := []struct {
		name  string
		admin string
		want  string
	}{
		{name: "empty username", admin: "username: \"\"\n  password: password", want: "admin.username"},
		{name: "empty password", admin: "username: operator\n  password: \"\"", want: "admin.password"},
		{name: "empty session ttl", admin: "session_ttl: \"\"", want: "admin.session_ttl"},
		{name: "empty monitor interval", admin: "monitor_interval: \"\"", want: "admin.monitor_interval"},
		{name: "zero session ttl", admin: "session_ttl: 0s", want: "admin.session_ttl"},
		{name: "negative monitor interval", admin: "monitor_interval: -1s", want: "admin.monitor_interval"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			yaml := "admin:\n  " + test.admin + "\n" + validYAML(t)
			_, err := Load(writeConfig(t, yaml))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestLoadRejectsMissingEnvironmentVariable(t *testing.T) {
	t.Setenv("TEST_UPSTREAM_URL", "http://127.0.0.1:7860")
	yaml := strings.ReplaceAll(validYAML(t), "${TEST_MINIMAX_KEY}", "${MISSING_TEST_KEY}")
	_, err := Load(writeConfig(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "MISSING_TEST_KEY") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsDuplicateAPIKey(t *testing.T) {
	t.Setenv("TEST_MINIMAX_KEY", "same-secret")
	t.Setenv("TEST_UPSTREAM_URL", "http://127.0.0.1:7860")
	yaml := strings.Replace(validYAML(t), "upstreams:\n", "  - id: customer-b\n    key: same-secret\n    enabled: true\nupstreams:\n", 1)
	_, err := Load(writeConfig(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "API Key") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsIncompleteGenerationProfile(t *testing.T) {
	t.Setenv("TEST_MINIMAX_KEY", "secret-a")
	t.Setenv("TEST_UPSTREAM_URL", "http://127.0.0.1:7860")
	yaml := strings.Replace(validYAML(t), "      adaptive: {width: 832, height: 480}\n", "", 1)
	_, err := Load(writeConfig(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "adaptive") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsGenerationDimensionNotMultipleOf32(t *testing.T) {
	t.Setenv("TEST_MINIMAX_KEY", "secret-a")
	t.Setenv("TEST_UPSTREAM_URL", "http://127.0.0.1:7860")
	yaml := strings.Replace(validYAML(t), "adaptive: {width: 832, height: 480}", "adaptive: {width: 830, height: 480}", 1)
	_, err := Load(writeConfig(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "32 的倍数") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadMigratesLegacyGenerationDimensions(t *testing.T) {
	t.Setenv("TEST_MINIMAX_KEY", "secret-a")
	t.Setenv("TEST_UPSTREAM_URL", "http://127.0.0.1:7860")
	yaml := strings.Replace(validYAML(t), `"21:9": {width: 1120, height: 480}`, `"21:9": {width: 1104, height: 480}`, 1)
	yaml = strings.ReplaceAll(yaml, "height: 1088", "height: 1080")
	yaml = strings.ReplaceAll(yaml, "width: 1088", "width: 1080")

	cfg, err := Load(writeConfig(t, yaml))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.GenerationProfiles["768P"].Dimensions["21:9"]; got != (Dimension{Width: 1120, Height: 480}) {
		t.Fatalf("768P 21:9 dimension = %+v", got)
	}
	if got := cfg.GenerationProfiles["2K"].Dimensions["adaptive"]; got != (Dimension{Width: 1920, Height: 1088}) {
		t.Fatalf("2K adaptive dimension = %+v", got)
	}
}

func TestLoadRejectsConfigWithoutEnabledAPIKey(t *testing.T) {
	t.Setenv("TEST_MINIMAX_KEY", "secret-a")
	t.Setenv("TEST_UPSTREAM_URL", "http://127.0.0.1:7860")
	yaml := strings.Replace(validYAML(t), "enabled: true", "enabled: false", 1)
	_, err := Load(writeConfig(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "启用") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsInvalidUpstreamURL(t *testing.T) {
	t.Setenv("TEST_MINIMAX_KEY", "secret-a")
	t.Setenv("TEST_UPSTREAM_URL", "ftp://127.0.0.1:7860")
	_, err := Load(writeConfig(t, validYAML(t)))
	if err == nil || !strings.Contains(err.Error(), "HTTP/HTTPS") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadAllowsHTTPPublicBaseURLForPrivateDeployment(t *testing.T) {
	t.Setenv("TEST_MINIMAX_KEY", "secret-a")
	t.Setenv("TEST_UPSTREAM_URL", "http://192.168.1.202:7861")
	yaml := strings.Replace(validYAML(t), "https://video.example.com", "http://192.168.1.202:7861", 1)
	cfg, err := Load(writeConfig(t, yaml))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.Upstreams[0].PublicBaseURL.String(); got != "http://192.168.1.202:7861" {
		t.Fatalf("PublicBaseURL = %q", got)
	}
}

func TestLoadRejectsUnavailableDatabaseDirectory(t *testing.T) {
	t.Setenv("TEST_MINIMAX_KEY", "secret-a")
	t.Setenv("TEST_UPSTREAM_URL", "http://127.0.0.1:7860")
	yaml := regexp.MustCompile(`path: "[^"]+"`).ReplaceAllString(validYAML(t), `path: "Z:/path/that/does/not/exist/minimax.db"`)
	_, err := Load(writeConfig(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "数据库目录") {
		t.Fatalf("Load() error = %v", err)
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validYAML(t *testing.T) string {
	t.Helper()
	dbPath := filepath.ToSlash(filepath.Join(t.TempDir(), "minimax.db"))
	return `server:
  address: ":8080"
database:
  path: "` + dbPath + `"
api_keys:
  - id: customer-a
    key: ${TEST_MINIMAX_KEY}
    enabled: true
upstreams:
  - id: gpu-1
    base_url: ${TEST_UPSTREAM_URL}
    public_base_url: https://video.example.com
generation_profiles:
  768P:
    model_mode: high_quality
    steps: 20
    dimensions:
      adaptive: {width: 832, height: 480}
      "21:9": {width: 1120, height: 480}
      "16:9": {width: 832, height: 480}
      "4:3": {width: 640, height: 480}
      "1:1": {width: 480, height: 480}
      "3:4": {width: 480, height: 640}
      "9:16": {width: 480, height: 832}
  2K:
    model_mode: custom
    steps: 20
    dimensions:
      adaptive: {width: 1920, height: 1088}
      "21:9": {width: 2560, height: 1088}
      "16:9": {width: 1920, height: 1088}
      "4:3": {width: 1440, height: 1088}
      "1:1": {width: 1088, height: 1088}
      "3:4": {width: 1088, height: 1440}
      "9:16": {width: 1088, height: 1920}
`
}
