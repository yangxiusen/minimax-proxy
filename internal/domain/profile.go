package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrProfileNotFound        = errors.New("请求配置不存在")
	ErrProfileVersionConflict = errors.New("请求配置已被其他操作修改")
	ErrProfileKeyConflict     = errors.New("请求配置键冲突")
	ErrNoCompatibleNode       = errors.New("没有兼容的模型服务节点")
	ErrInvalidProfileConfig   = errors.New("请求配置无效")
)

var ProfileRatios = []string{"adaptive", "21:9", "16:9", "4:3", "1:1", "3:4", "9:16"}

type GenerationProfile struct {
	ModelMode     string `json:"model_mode,omitempty"`
	Steps         int    `json:"steps"`
	SageAttention string `json:"sage_attention"`
	CacheMode     string `json:"cache_mode"`
}

type RatioMapping struct {
	BaseWidth    int `json:"base_width"`
	BaseHeight   int `json:"base_height"`
	TargetWidth  int `json:"target_width"`
	TargetHeight int `json:"target_height"`
}

type LoRAProfile struct {
	Name     string  `json:"name"`
	Strength float64 `json:"strength"`
}

type InterpolationProfile struct {
	Enabled bool   `json:"enabled"`
	Engine  string `json:"engine"`
	Scale   int    `json:"scale"`
}

type RestorationProfile struct {
	Enabled bool   `json:"enabled"`
	Engine  string `json:"engine"`
	Scale   int    `json:"scale"`
}

type ProfileConfig struct {
	Resolution    string                  `json:"resolution"`
	Generation    GenerationProfile       `json:"generation"`
	Ratios        map[string]RatioMapping `json:"ratios"`
	LoRAs         []LoRAProfile           `json:"loras"`
	Interpolation InterpolationProfile    `json:"interpolation"`
	Restoration   RestorationProfile      `json:"restoration"`
}

type ModelRequestProfile struct {
	ID            string        `json:"id"`
	Resolution    string        `json:"resolution"`
	ResolutionKey string        `json:"-"`
	ConfigJSON    string        `json:"-"`
	ConfigHash    string        `json:"config_hash"`
	Config        ProfileConfig `json:"config"`
	CreatedBy     string        `json:"created_by"`
	UpdatedBy     string        `json:"updated_by"`
	CreatedAt     int64         `json:"created_at"`
	UpdatedAt     int64         `json:"updated_at"`
	RowVersion    int64         `json:"row_version"`
}

type CompatibleNode struct {
	NodeID     string   `json:"node_id"`
	Compatible bool     `json:"compatible"`
	Reasons    []string `json:"reasons"`
}

func NormalizeResolutionName(value string) (string, string, error) {
	display := strings.TrimSpace(value)
	count := utf8.RuneCountInString(display)
	if count < 1 || count > 32 {
		return "", "", fmt.Errorf("%w: 逻辑分辨率名称格式无效", ErrInvalidProfileConfig)
	}
	var key strings.Builder
	key.Grow(len(display))
	for _, character := range display {
		switch {
		case character >= 'A' && character <= 'Z':
			key.WriteRune(character + ('a' - 'A'))
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9', character == ' ', character == '-', character == '_', unicode.Is(unicode.Han, character):
			key.WriteRune(character)
		default:
			return "", "", fmt.Errorf("%w: 逻辑分辨率名称格式无效", ErrInvalidProfileConfig)
		}
	}
	return display, key.String(), nil
}
