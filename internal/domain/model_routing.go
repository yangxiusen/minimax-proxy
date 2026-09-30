package domain

import (
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ProtocolH3       = "h3-node-v1"
	ProtocolOfficial = "minimax-v2"
	ProtocolTK2SD    = "tk2sd-v1"
	ProtocolLegacy   = "legacy-gradio-v1"
)

var (
	ErrUnsupportedModel = errors.New("模型未配置或未开放")
	ErrRouteAmbiguous   = errors.New("模型存在多个协议，请由管理员指定线路")
	ErrRouteUnavailable = errors.New("模型所选协议暂不可用")
	ErrRouteChanged     = errors.New("模型线路已变化，请重试")
	ErrCatalogConflict  = errors.New("模型目录已变化，请刷新后重试")
	ErrModelInput       = errors.New("模型不支持该输入")
)

func ValidModelID(id string) bool {
	return utf8.ValidString(id) && utf8.RuneCountInString(id) >= 1 && utf8.RuneCountInString(id) <= 128 && !strings.Contains(id, "*") && strings.IndexFunc(id, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}

type ModeCapability struct {
	Scenario  string   `json:"scenario"`
	Durations []int    `json:"durations"`
	Roles     []string `json:"roles,omitempty"`
}
type ModelCapability struct {
	SchemaVersion    int              `json:"schema_version"`
	Modes            []ModeCapability `json:"modes"`
	InputSources     []string         `json:"input_sources,omitempty"`
	ResolutionPolicy string           `json:"resolution_policy,omitempty"`
	Resolutions      []string         `json:"resolutions,omitempty"`
	MaxMedia         int              `json:"max_media,omitempty"`
}

func (c ModelCapability) Supports(scenario string, duration int) bool {
	for _, m := range c.Modes {
		if m.Scenario == scenario && slices.Contains(m.Durations, duration) {
			return true
		}
	}
	return false
}
func (c ModelCapability) HasMode(scenario string) bool {
	for _, m := range c.Modes {
		if m.Scenario == scenario {
			return true
		}
	}
	return false
}

type TaskRequirements struct {
	Scenario     string   `json:"scenario"`
	Duration     int      `json:"duration"`
	Resolution   string   `json:"resolution,omitempty"`
	Roles        []string `json:"roles,omitempty"`
	InputSources []string `json:"input_sources,omitempty"`
	MediaCount   int      `json:"media_count"`
}

func (c ModelCapability) Accepts(r TaskRequirements) bool {
	if !c.Supports(r.Scenario, r.Duration) || (c.MaxMedia > 0 && r.MediaCount > c.MaxMedia) {
		return false
	}
	if len(c.Resolutions) > 0 && !slices.Contains(c.Resolutions, r.Resolution) {
		return false
	}
	for _, s := range r.InputSources {
		if len(c.InputSources) > 0 && !slices.Contains(c.InputSources, s) {
			return false
		}
	}
	for _, m := range c.Modes {
		if m.Scenario != r.Scenario || !slices.Contains(m.Durations, r.Duration) {
			continue
		}
		for _, role := range r.Roles {
			if len(m.Roles) > 0 && !slices.Contains(m.Roles, role) {
				return false
			}
		}
		return true
	}
	return false
}

type NodeModel struct {
	ModelID      string          `json:"model_id"`
	Enabled      bool            `json:"enabled"`
	Present      bool            `json:"present"`
	Verified     bool            `json:"verified"`
	Capabilities ModelCapability `json:"capabilities"`
}
type ModelCatalog struct {
	NodeID        string      `json:"node_id"`
	Source        string      `json:"source"`
	Revision      int64       `json:"revision"`
	Status        string      `json:"status"`
	Fingerprint   string      `json:"-"`
	LastAttemptAt int64       `json:"last_attempt_at"`
	LastSuccessAt int64       `json:"last_success_at"`
	ValidUntil    int64       `json:"valid_until"`
	LastErrorCode string      `json:"last_error_code"`
	Items         []NodeModel `json:"items"`
}
type RouteCandidate struct {
	ProtocolVersion string   `json:"protocol_version"`
	NodeIDs         []string `json:"node_ids"`
	HealthyNodes    int      `json:"healthy_nodes"`
}
type ModelRoute struct {
	Model               string           `json:"model"`
	ProtocolVersion     string           `json:"protocol_version"`
	SelectionMode       string           `json:"selection_mode"`
	Version             int64            `json:"version"`
	State               string           `json:"state"`
	WaitReason          string           `json:"wait_reason"`
	Candidates          []RouteCandidate `json:"candidates"`
	LegacyAwaitingTasks int              `json:"legacy_awaiting_tasks"`
}
type RouteSnapshot struct {
	SchemaVersion     int              `json:"schema_version"`
	Model             string           `json:"model"`
	ProtocolVersion   string           `json:"protocol_version"`
	RouteVersion      int64            `json:"route_version"`
	RoutingRevision   int64            `json:"routing_revision"`
	SelectionMode     string           `json:"selection_mode"`
	ParameterPlan     string           `json:"parameter_plan"`
	NormalizerVersion string           `json:"normalizer_version"`
	Requirements      TaskRequirements `json:"requirements"`
}
