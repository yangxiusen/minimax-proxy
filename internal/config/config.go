package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

var requiredRatios = []string{"adaptive", "21:9", "16:9", "4:3", "1:1", "3:4", "9:16"}

type Config struct {
	Server             ServerConfig
	Admin              AdminConfig
	Database           DatabaseConfig
	Queue              QueueConfig
	Task               TaskConfig
	APIKeys            []APIKeyConfig
	Upstreams          []UpstreamConfig
	GenerationProfiles map[string]GenerationProfile
}

type AdminConfig struct {
	Username        string
	Password        string
	SessionTTL      time.Duration
	MonitorInterval time.Duration
	SecureCookie    bool
}

type ServerConfig struct {
	Address      string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

type DatabaseConfig struct {
	Path string
}

type QueueConfig struct {
	ProtectedSlots        int
	PerKeyUnfinishedLimit int
	GlobalUnfinishedLimit int
}

type TaskConfig struct {
	Retention      time.Duration
	IdempotencyTTL time.Duration
}

type APIKeyConfig struct {
	ID      string
	Key     string
	Enabled bool
}

type UpstreamConfig struct {
	ID             string
	BaseURL        *url.URL
	PublicBaseURL  *url.URL
	HealthPath     string
	SubmitAPIName  string
	CheckAPIName   string
	PollInterval   time.Duration
	RequestTimeout time.Duration
}

type GenerationProfile struct {
	ModelMode       string               `yaml:"model_mode"`
	CustomModel     string               `yaml:"custom_model"`
	CustomModelHigh string               `yaml:"custom_model_high"`
	EasyCache       bool                 `yaml:"easy_cache"`
	Steps           int                  `yaml:"steps"`
	Dimensions      map[string]Dimension `yaml:"dimensions"`
}

type Dimension struct {
	Width  int `yaml:"width"`
	Height int `yaml:"height"`
}

type rawConfig struct {
	Server struct {
		Address      string `yaml:"address"`
		ReadTimeout  string `yaml:"read_timeout"`
		WriteTimeout string `yaml:"write_timeout"`
	} `yaml:"server"`
	Admin struct {
		Username        *string `yaml:"username"`
		Password        *string `yaml:"password"`
		SessionTTL      *string `yaml:"session_ttl"`
		MonitorInterval *string `yaml:"monitor_interval"`
		SecureCookie    *bool   `yaml:"secure_cookie"`
	} `yaml:"admin"`
	Database DatabaseConfig `yaml:"database"`
	Queue    struct {
		ProtectedSlots        *int `yaml:"protected_slots"`
		PerKeyUnfinishedLimit *int `yaml:"per_key_unfinished_limit"`
		GlobalUnfinishedLimit *int `yaml:"global_unfinished_limit"`
	} `yaml:"queue"`
	Task struct {
		Retention      string `yaml:"retention"`
		IdempotencyTTL string `yaml:"idempotency_ttl"`
	} `yaml:"task"`
	APIKeys   []APIKeyConfig `yaml:"api_keys"`
	Upstreams []struct {
		ID             string `yaml:"id"`
		BaseURL        string `yaml:"base_url"`
		PublicBaseURL  string `yaml:"public_base_url"`
		HealthPath     string `yaml:"health_path"`
		SubmitAPIName  string `yaml:"submit_api_name"`
		CheckAPIName   string `yaml:"check_api_name"`
		PollInterval   string `yaml:"poll_interval"`
		RequestTimeout string `yaml:"request_timeout"`
	} `yaml:"upstreams"`
	GenerationProfiles map[string]GenerationProfile `yaml:"generation_profiles"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("读取配置文件: %w", err)
	}
	expanded, err := expandEnvironment(string(data))
	if err != nil {
		return Config{}, err
	}

	var raw rawConfig
	decoder := yaml.NewDecoder(bytes.NewBufferString(expanded))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("解析 YAML 配置: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return Config{}, errors.New("配置文件只能包含一个 YAML 文档")
	} else if !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("解析额外 YAML 内容: %w", err)
	}

	cfg, err := normalize(raw)
	if err != nil {
		return Config{}, err
	}
	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func expandEnvironment(input string) (string, error) {
	var missing string
	result := envPattern.ReplaceAllStringFunc(input, func(token string) string {
		name := envPattern.FindStringSubmatch(token)[1]
		value, ok := os.LookupEnv(name)
		if !ok || value == "" {
			missing = name
			return token
		}
		return value
	})
	if missing != "" {
		return "", fmt.Errorf("环境变量 %s 未配置或为空", missing)
	}
	return result, nil
}

func normalize(raw rawConfig) (Config, error) {
	cfg := Config{
		Server:             ServerConfig{Address: ":8080", ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second},
		Admin:              AdminConfig{Username: "admin", Password: "123", SessionTTL: 12 * time.Hour, MonitorInterval: 5 * time.Second},
		Database:           raw.Database,
		Queue:              QueueConfig{ProtectedSlots: 3, PerKeyUnfinishedLimit: 10, GlobalUnfinishedLimit: 100},
		Task:               TaskConfig{Retention: 7 * 24 * time.Hour, IdempotencyTTL: 24 * time.Hour},
		APIKeys:            raw.APIKeys,
		GenerationProfiles: raw.GenerationProfiles,
	}
	if raw.Server.Address != "" {
		cfg.Server.Address = raw.Server.Address
	}
	if raw.Admin.Username != nil {
		cfg.Admin.Username = *raw.Admin.Username
	}
	if raw.Admin.Password != nil {
		cfg.Admin.Password = *raw.Admin.Password
	}
	if raw.Admin.SecureCookie != nil {
		cfg.Admin.SecureCookie = *raw.Admin.SecureCookie
	}
	var err error
	if cfg.Server.ReadTimeout, err = parseDuration(raw.Server.ReadTimeout, cfg.Server.ReadTimeout, "server.read_timeout"); err != nil {
		return Config{}, err
	}
	if cfg.Server.WriteTimeout, err = parseDuration(raw.Server.WriteTimeout, cfg.Server.WriteTimeout, "server.write_timeout"); err != nil {
		return Config{}, err
	}
	if cfg.Admin.SessionTTL, err = parseOptionalDuration(raw.Admin.SessionTTL, cfg.Admin.SessionTTL, "admin.session_ttl"); err != nil {
		return Config{}, err
	}
	if cfg.Admin.MonitorInterval, err = parseOptionalDuration(raw.Admin.MonitorInterval, cfg.Admin.MonitorInterval, "admin.monitor_interval"); err != nil {
		return Config{}, err
	}
	if raw.Queue.ProtectedSlots != nil {
		cfg.Queue.ProtectedSlots = *raw.Queue.ProtectedSlots
	}
	if raw.Queue.PerKeyUnfinishedLimit != nil {
		cfg.Queue.PerKeyUnfinishedLimit = *raw.Queue.PerKeyUnfinishedLimit
	}
	if raw.Queue.GlobalUnfinishedLimit != nil {
		cfg.Queue.GlobalUnfinishedLimit = *raw.Queue.GlobalUnfinishedLimit
	}
	if cfg.Task.Retention, err = parseDuration(raw.Task.Retention, cfg.Task.Retention, "task.retention"); err != nil {
		return Config{}, err
	}
	if cfg.Task.IdempotencyTTL, err = parseDuration(raw.Task.IdempotencyTTL, cfg.Task.IdempotencyTTL, "task.idempotency_ttl"); err != nil {
		return Config{}, err
	}
	migrateLegacyGenerationDimensions(cfg.GenerationProfiles)
	for _, item := range raw.Upstreams {
		baseURL, err := parseURL(item.BaseURL)
		if err != nil {
			return Config{}, fmt.Errorf("upstream %q base_url: %w", item.ID, err)
		}
		publicURL, err := parseURL(item.PublicBaseURL)
		if err != nil {
			return Config{}, fmt.Errorf("upstream %q public_base_url: %w", item.ID, err)
		}
		u := UpstreamConfig{
			ID: item.ID, BaseURL: baseURL, PublicBaseURL: publicURL,
			HealthPath: "/", SubmitAPIName: "submit_minimax_from_slots", CheckAPIName: "check_and_get_video",
			PollInterval: 3 * time.Second, RequestTimeout: 30 * time.Second,
		}
		if item.HealthPath != "" {
			u.HealthPath = item.HealthPath
		}
		if item.SubmitAPIName != "" {
			u.SubmitAPIName = item.SubmitAPIName
		}
		if item.CheckAPIName != "" {
			u.CheckAPIName = item.CheckAPIName
		}
		if u.PollInterval, err = parseDuration(item.PollInterval, u.PollInterval, "upstream.poll_interval"); err != nil {
			return Config{}, err
		}
		if u.RequestTimeout, err = parseDuration(item.RequestTimeout, u.RequestTimeout, "upstream.request_timeout"); err != nil {
			return Config{}, err
		}
		cfg.Upstreams = append(cfg.Upstreams, u)
	}
	return cfg, nil
}

func migrateLegacyGenerationDimensions(profiles map[string]GenerationProfile) {
	// 兼容升级前示例配置中的旧尺寸，其他非 32 倍数仍由校验拒绝。
	if profile, ok := profiles["768P"]; ok {
		if dimension, exists := profile.Dimensions["21:9"]; exists && dimension == (Dimension{Width: 1104, Height: 480}) {
			profile.Dimensions["21:9"] = Dimension{Width: 1120, Height: 480}
		}
	}
	if profile, ok := profiles["2K"]; ok {
		for ratio, dimension := range profile.Dimensions {
			if dimension.Width == 1080 {
				dimension.Width = 1088
			}
			if dimension.Height == 1080 {
				dimension.Height = 1088
			}
			profile.Dimensions[ratio] = dimension
		}
	}
}

func parseDuration(value string, fallback time.Duration, field string) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s 必须是正数时长", field)
	}
	return duration, nil
}

func parseOptionalDuration(value *string, fallback time.Duration, field string) (time.Duration, error) {
	if value == nil {
		return fallback, nil
	}
	if strings.TrimSpace(*value) == "" {
		return 0, fmt.Errorf("%s 必须是正数时长", field)
	}
	return parseDuration(*value, fallback, field)
}

func parseURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("必须是完整的 HTTP/HTTPS URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("不得包含凭据、查询参数或片段")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u, nil
}

func validate(cfg Config) error {
	if strings.TrimSpace(cfg.Admin.Username) == "" {
		return errors.New("admin.username 不能为空")
	}
	if strings.TrimSpace(cfg.Admin.Password) == "" {
		return errors.New("admin.password 不能为空")
	}
	if cfg.Database.Path == "" {
		return errors.New("database.path 不能为空")
	}
	if err := validateWritableDirectory(filepath.Dir(cfg.Database.Path)); err != nil {
		return err
	}
	if cfg.Queue.ProtectedSlots < 0 || cfg.Queue.PerKeyUnfinishedLimit <= 0 || cfg.Queue.GlobalUnfinishedLimit <= 0 {
		return errors.New("队列限制配置无效")
	}
	if cfg.Queue.PerKeyUnfinishedLimit > cfg.Queue.GlobalUnfinishedLimit {
		return errors.New("每 Key 上限不得超过全局上限")
	}
	if len(cfg.APIKeys) == 0 {
		return errors.New("至少配置一个 API Key")
	}
	ids, keys := map[string]struct{}{}, map[string]struct{}{}
	enabledKeys := 0
	for _, key := range cfg.APIKeys {
		if key.ID == "" || key.Key == "" {
			return errors.New("API Key 的 id 和 key 不能为空")
		}
		if _, ok := ids[key.ID]; ok {
			return fmt.Errorf("API Key id %q 重复", key.ID)
		}
		if _, ok := keys[key.Key]; ok {
			return errors.New("API Key 值重复")
		}
		ids[key.ID], keys[key.Key] = struct{}{}, struct{}{}
		if key.Enabled {
			enabledKeys++
		}
	}
	if enabledKeys == 0 {
		return errors.New("至少配置一个启用的 API Key")
	}
	if len(cfg.Upstreams) == 0 {
		return errors.New("至少配置一个上游实例")
	}
	upstreamIDs := map[string]struct{}{}
	for _, upstream := range cfg.Upstreams {
		if upstream.ID == "" {
			return errors.New("upstream.id 不能为空")
		}
		if _, ok := upstreamIDs[upstream.ID]; ok {
			return fmt.Errorf("upstream id %q 重复", upstream.ID)
		}
		upstreamIDs[upstream.ID] = struct{}{}
		if !strings.HasPrefix(upstream.HealthPath, "/") {
			return fmt.Errorf("upstream %q health_path 必须以 / 开头", upstream.ID)
		}
	}
	for _, resolution := range []string{"768P", "2K"} {
		profile, ok := cfg.GenerationProfiles[resolution]
		if !ok {
			return fmt.Errorf("generation_profiles 缺少 %s", resolution)
		}
		if profile.ModelMode == "" || profile.Steps <= 0 {
			return fmt.Errorf("generation_profiles.%s 的 model_mode/steps 无效", resolution)
		}
		for _, ratio := range requiredRatios {
			dimension, ok := profile.Dimensions[ratio]
			if !ok {
				return fmt.Errorf("generation_profiles.%s 缺少 %s 尺寸", resolution, ratio)
			}
			if dimension.Width <= 0 || dimension.Height <= 0 {
				return fmt.Errorf("generation_profiles.%s.%s 尺寸无效", resolution, ratio)
			}
			if dimension.Width%32 != 0 || dimension.Height%32 != 0 {
				return fmt.Errorf("generation_profiles.%s.%s 的宽高必须是 32 的倍数", resolution, ratio)
			}
		}
	}
	return nil
}

func validateWritableDirectory(dir string) error {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("数据库目录不可用: %s", dir)
	}
	file, err := os.CreateTemp(dir, ".minimax-writecheck-*")
	if err != nil {
		return fmt.Errorf("数据库目录不可写: %w", err)
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭写入检查文件: %w", err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("清理写入检查文件: %w", err)
	}
	return nil
}
