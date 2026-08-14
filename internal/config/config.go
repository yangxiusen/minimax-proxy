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
	"strconv"
	"strings"
	"time"

	"minimax-h3-tc/internal/domain"

	"gopkg.in/yaml.v3"
)

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

var requiredRatios = []string{"adaptive", "21:9", "16:9", "4:3", "1:1", "3:4", "9:16"}
var requiredResolutions = []string{"480P", "768P", "2K"}

type Config struct {
	Server             ServerConfig
	Admin              AdminConfig
	Database           DatabaseConfig
	Queue              QueueConfig
	Task               TaskConfig
	APIKeys            []APIKeyConfig
	LegacyUpstreams    []LegacyUpstreamConfig
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
	Address       string
	PublicBaseURL *url.URL
	ReadTimeout   time.Duration
	WriteTimeout  time.Duration
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
	Retention        time.Duration
	IdempotencyTTL   time.Duration
	ExecutionTimeout time.Duration
}

type APIKeyConfig struct {
	ID         string `yaml:"id"`
	Key        string `yaml:"key"`
	Enabled    bool   `yaml:"-"`
	EnabledRaw string `yaml:"-"`
}

func (key *APIKeyConfig) UnmarshalYAML(node *yaml.Node) error {
	var raw struct {
		ID      string    `yaml:"id"`
		Key     string    `yaml:"key"`
		Enabled yaml.Node `yaml:"enabled"`
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	key.ID, key.Key, key.EnabledRaw = raw.ID, raw.Key, raw.Enabled.Value
	if raw.Enabled.Tag == "!!bool" {
		value, err := strconv.ParseBool(raw.Enabled.Value)
		if err != nil {
			return err
		}
		key.Enabled = value
	}
	return nil
}

type UpstreamConfig struct {
	ID              string
	ServiceURL      *url.URL
	ProtocolVersion string
	BaseURL         *url.URL
	JobsBaseURL     *url.URL
	PublicBaseURL   *url.URL
	HealthPath      string
	SubmitAPIName   string
	CheckAPIName    string
	PollInterval    time.Duration
	RequestTimeout  time.Duration
}

type LegacyUpstreamConfig struct {
	ID             string `yaml:"id"`
	BaseURL        string `yaml:"base_url"`
	JobsBaseURL    string `yaml:"jobs_base_url"`
	PublicBaseURL  string `yaml:"public_base_url"`
	HealthPath     string `yaml:"health_path"`
	SubmitAPIName  string `yaml:"submit_api_name"`
	CheckAPIName   string `yaml:"check_api_name"`
	PollInterval   string `yaml:"poll_interval"`
	RequestTimeout string `yaml:"request_timeout"`
}

type GenerationProfile struct {
	ModelMode       string               `yaml:"model_mode"`
	CustomModel     string               `yaml:"custom_model"`
	CustomModelHigh string               `yaml:"custom_model_high"`
	FPS             int                  `yaml:"fps"`
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
		Address       string `yaml:"address"`
		PublicBaseURL string `yaml:"public_base_url"`
		ReadTimeout   string `yaml:"read_timeout"`
		WriteTimeout  string `yaml:"write_timeout"`
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
		Retention        string `yaml:"retention"`
		IdempotencyTTL   string `yaml:"idempotency_ttl"`
		ExecutionTimeout string `yaml:"execution_timeout"`
	} `yaml:"task"`
	APIKeys            []APIKeyConfig               `yaml:"api_keys"`
	Upstreams          []LegacyUpstreamConfig       `yaml:"upstreams"`
	GenerationProfiles map[string]GenerationProfile `yaml:"generation_profiles"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("读取配置文件: %w", err)
	}
	mainYAML, legacyUpstreams, err := splitLegacyUpstreams(data)
	if err != nil {
		return Config{}, err
	}
	mainYAML, legacyAPIKeys, err := splitLegacyAPIKeys([]byte(mainYAML))
	if err != nil {
		return Config{}, err
	}
	expanded, err := expandEnvironment(mainYAML)
	if err != nil {
		return Config{}, err
	}
	raw, err := decodeRawConfig(expanded)
	if err != nil {
		return Config{}, err
	}
	raw.Upstreams = legacyUpstreams
	raw.APIKeys = legacyAPIKeys

	cfg, err := normalize(raw)
	if err != nil {
		return Config{}, err
	}
	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func splitLegacyAPIKeys(data []byte) (string, []APIKeyConfig, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return "", nil, fmt.Errorf("解析 YAML 配置: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return "", nil, errors.New("配置文件必须是 YAML 对象")
	}
	root := document.Content[0]
	var legacy []APIKeyConfig
	for index := 0; index < len(root.Content); index += 2 {
		if root.Content[index].Value != "api_keys" {
			continue
		}
		keyData, err := yaml.Marshal(root.Content[index+1])
		if err != nil {
			return "", nil, fmt.Errorf("解析旧 API Key 配置: %w", err)
		}
		keyDecoder := yaml.NewDecoder(bytes.NewReader(keyData))
		keyDecoder.KnownFields(true)
		if err := keyDecoder.Decode(&legacy); err != nil {
			return "", nil, fmt.Errorf("解析旧 API Key 配置: %w", err)
		}
		root.Content = append(root.Content[:index], root.Content[index+2:]...)
		break
	}
	mainData, err := yaml.Marshal(&document)
	if err != nil {
		return "", nil, fmt.Errorf("规范化 YAML 配置: %w", err)
	}
	return string(mainData), legacy, nil
}

func ParseLegacyAPIKeys(items []APIKeyConfig) ([]APIKeyConfig, error) {
	result := make([]APIKeyConfig, 0, len(items))
	ids, keys := make(map[string]struct{}, len(items)), make(map[string]struct{}, len(items))
	for _, item := range items {
		id, err := expandEnvironment(item.ID)
		if err != nil {
			return nil, err
		}
		key, err := expandEnvironment(item.Key)
		if err != nil {
			return nil, err
		}
		item.ID, item.Key = strings.TrimSpace(id), key
		if item.EnabledRaw != "" {
			enabled, err := expandEnvironment(item.EnabledRaw)
			if err != nil {
				return nil, err
			}
			item.Enabled, err = strconv.ParseBool(enabled)
			if err != nil {
				return nil, errors.New("API Key enabled 必须是布尔值")
			}
		}
		if item.ID == "" || item.Key == "" {
			return nil, errors.New("API Key 的 id 和 key 不能为空")
		}
		if _, exists := ids[strings.ToLower(item.ID)]; exists {
			return nil, fmt.Errorf("API Key id %q 重复", item.ID)
		}
		if _, exists := keys[item.Key]; exists {
			return nil, errors.New("API Key 值重复")
		}
		ids[strings.ToLower(item.ID)], keys[item.Key] = struct{}{}, struct{}{}
		result = append(result, item)
	}
	return result, nil
}

func splitLegacyUpstreams(data []byte) (string, []LegacyUpstreamConfig, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return "", nil, fmt.Errorf("解析 YAML 配置: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return "", nil, errors.New("配置文件只能包含一个 YAML 文档")
	} else if !errors.Is(err, io.EOF) {
		return "", nil, fmt.Errorf("解析 YAML 配置: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return "", nil, errors.New("配置文件必须是 YAML 对象")
	}
	root := document.Content[0]
	var legacy []LegacyUpstreamConfig
	for index := 0; index < len(root.Content); index += 2 {
		if root.Content[index].Value != "upstreams" {
			continue
		}
		upstreamData, err := yaml.Marshal(root.Content[index+1])
		if err != nil {
			return "", nil, fmt.Errorf("解析 YAML 配置: %w", err)
		}
		upstreamDecoder := yaml.NewDecoder(bytes.NewReader(upstreamData))
		upstreamDecoder.KnownFields(true)
		if err := upstreamDecoder.Decode(&legacy); err != nil {
			return "", nil, fmt.Errorf("解析 YAML 配置: %w", err)
		}
		root.Content = append(root.Content[:index], root.Content[index+2:]...)
		break
	}
	mainData, err := yaml.Marshal(&document)
	if err != nil {
		return "", nil, fmt.Errorf("规范化 YAML 配置: %w", err)
	}
	return string(mainData), legacy, nil
}

func decodeRawConfig(input string) (rawConfig, error) {
	var raw rawConfig
	decoder := yaml.NewDecoder(bytes.NewBufferString(input))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return rawConfig{}, fmt.Errorf("解析 YAML 配置: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return rawConfig{}, errors.New("配置文件只能包含一个 YAML 文档")
	} else if !errors.Is(err, io.EOF) {
		return rawConfig{}, fmt.Errorf("解析额外 YAML 内容: %w", err)
	}
	return raw, nil
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
		Task:               TaskConfig{Retention: 7 * 24 * time.Hour, IdempotencyTTL: 24 * time.Hour, ExecutionTimeout: 10 * time.Minute},
		APIKeys:            raw.APIKeys,
		LegacyUpstreams:    append([]LegacyUpstreamConfig(nil), raw.Upstreams...),
		GenerationProfiles: raw.GenerationProfiles,
	}
	if raw.Server.Address != "" {
		cfg.Server.Address = raw.Server.Address
	}
	var err error
	if cfg.Server.PublicBaseURL, err = parsePublicBaseURL(raw.Server.PublicBaseURL); err != nil {
		return Config{}, err
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
	if cfg.Task.ExecutionTimeout, err = parseDuration(raw.Task.ExecutionTimeout, cfg.Task.ExecutionTimeout, "task.execution_timeout"); err != nil {
		return Config{}, err
	}
	migrateLegacyGenerationProfiles(cfg.GenerationProfiles)
	return cfg, nil
}

func ParseLegacyUpstreams(items []LegacyUpstreamConfig) ([]UpstreamConfig, error) {
	upstreams := make([]UpstreamConfig, 0, len(items))
	ids := make(map[string]struct{}, len(items))
	for _, rawItem := range items {
		item, err := expandLegacyUpstream(rawItem)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(item.ID) == "" {
			return nil, errors.New("upstream.id 不能为空")
		}
		input := domain.ModelNodeInput{
			ID: item.ID, BaseURL: item.BaseURL, JobsBaseURL: item.JobsBaseURL, PublicBaseURL: item.PublicBaseURL,
			HealthPath: "/", SubmitAPIName: "submit_minimax_from_slots", CheckAPIName: "check_and_get_video",
			PollInterval: 3 * time.Second, RequestTimeout: 30 * time.Second, Enabled: true,
		}
		if item.HealthPath != "" {
			input.HealthPath = item.HealthPath
		}
		if item.SubmitAPIName != "" {
			input.SubmitAPIName = item.SubmitAPIName
		}
		if item.CheckAPIName != "" {
			input.CheckAPIName = item.CheckAPIName
		}
		if input.PollInterval, err = parseDuration(item.PollInterval, input.PollInterval, "upstream.poll_interval"); err != nil {
			return nil, err
		}
		if input.RequestTimeout, err = parseDuration(item.RequestTimeout, input.RequestTimeout, "upstream.request_timeout"); err != nil {
			return nil, err
		}
		normalized, upstream, err := NormalizeModelNode(input)
		if err != nil {
			return nil, fmt.Errorf("upstream %q: %w", item.ID, err)
		}
		if _, exists := ids[normalized.ID]; exists {
			return nil, fmt.Errorf("upstream id %q 重复", normalized.ID)
		}
		ids[normalized.ID] = struct{}{}
		upstreams = append(upstreams, upstream)
	}
	return upstreams, nil
}

func expandLegacyUpstream(item LegacyUpstreamConfig) (LegacyUpstreamConfig, error) {
	fields := []*string{&item.ID, &item.BaseURL, &item.JobsBaseURL, &item.PublicBaseURL, &item.HealthPath, &item.SubmitAPIName, &item.CheckAPIName, &item.PollInterval, &item.RequestTimeout}
	for _, field := range fields {
		expanded, err := expandEnvironment(*field)
		if err != nil {
			return LegacyUpstreamConfig{}, err
		}
		*field = expanded
	}
	return item, nil
}

func migrateLegacyGenerationProfiles(profiles map[string]GenerationProfile) {
	for resolution, profile := range profiles {
		if profile.FPS == 0 {
			profile.FPS = 24
			profiles[resolution] = profile
		}
	}
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
	if _, has480P := profiles["480P"]; !has480P {
		if legacyProfile, ok := profiles["768P"]; ok && dimensionsEqual(legacyProfile.Dimensions, dimensions480P()) {
			profiles["480P"] = legacyProfile
			legacyProfile.Dimensions = dimensions768P()
			profiles["768P"] = legacyProfile
		}
	}
}

func dimensionsEqual(got, want map[string]Dimension) bool {
	if len(got) != len(want) {
		return false
	}
	for ratio, dimension := range want {
		if got[ratio] != dimension {
			return false
		}
	}
	return true
}

func dimensions480P() map[string]Dimension {
	return map[string]Dimension{
		"adaptive": {Width: 832, Height: 480},
		"21:9":     {Width: 1120, Height: 480},
		"16:9":     {Width: 832, Height: 480},
		"4:3":      {Width: 640, Height: 480},
		"1:1":      {Width: 480, Height: 480},
		"3:4":      {Width: 480, Height: 640},
		"9:16":     {Width: 480, Height: 832},
	}
}

func dimensions768P() map[string]Dimension {
	return map[string]Dimension{
		"adaptive": {Width: 1344, Height: 768},
		"21:9":     {Width: 1792, Height: 768},
		"16:9":     {Width: 1344, Height: 768},
		"4:3":      {Width: 1024, Height: 768},
		"1:1":      {Width: 768, Height: 768},
		"3:4":      {Width: 768, Height: 1024},
		"9:16":     {Width: 768, Height: 1344},
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

func parsePublicBaseURL(value string) (*url.URL, error) {
	if strings.TrimSpace(value) == "" {
		return nil, errors.New("server.public_base_url 不能为空")
	}
	u, err := parseURL(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("server.public_base_url %w", err)
	}
	if u.Path != "" {
		return nil, errors.New("server.public_base_url 必须是根地址，不得包含子路径")
	}
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
	ids, keys := map[string]struct{}{}, map[string]struct{}{}
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
	}
	for _, resolution := range requiredResolutions {
		profile, ok := cfg.GenerationProfiles[resolution]
		if !ok {
			return fmt.Errorf("generation_profiles 缺少 %s", resolution)
		}
		if profile.ModelMode == "" || profile.Steps <= 0 {
			return fmt.Errorf("generation_profiles.%s 的 model_mode/steps 无效", resolution)
		}
		if profile.FPS < 10 || profile.FPS > 60 {
			return fmt.Errorf("generation_profiles.%s 的 fps 必须为 10 到 60", resolution)
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
