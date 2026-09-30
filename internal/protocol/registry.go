package protocol

import (
	"fmt"
	"minimax-h3-tc/internal/domain"
	"strings"
)

type Definition struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ModelSource   string `json:"model_source"`
	ParameterPlan string `json:"parameter_plan"`
	Creatable     bool   `json:"creatable"`
}

func Definitions() []Definition {
	return []Definition{{domain.ProtocolH3, "H3 内部节点", "builtin", "h3_profile", true}, {domain.ProtocolOfficial, "MiniMax 官方 V2", "manual", "direct", true}, {domain.ProtocolTK2SD, "tk2sd", "discovered", "direct", true}, {domain.ProtocolLegacy, "遗留 Gradio", "builtin", "h3_profile", false}}
}
func Lookup(id string) (Definition, bool) {
	for _, d := range Definitions() {
		if d.ID == id {
			return d, true
		}
	}
	return Definition{}, false
}
func BuiltinCapability(id string) domain.ModelCapability {
	d := []int{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	c := domain.ModelCapability{SchemaVersion: 1, Modes: []domain.ModeCapability{{Scenario: "t2va", Durations: d}, {Scenario: "i2va", Durations: d, Roles: []string{"first_frame", "last_frame"}}, {Scenario: "r2va", Durations: d, Roles: []string{"reference_image", "reference_video", "reference_audio"}}}, InputSources: []string{"http", "https", "data"}, ResolutionPolicy: "required", MaxMedia: 15}
	if id == domain.ProtocolOfficial {
		c.Resolutions = []string{"768P", "2K"}
		c.InputSources = append(c.InputSources, "mm_file")
	}
	return c
}
func Normalize(id string, r domain.GenerationRequest, c domain.ModelCapability) (domain.NormalizedRequest, error) {
	if _, ok := Lookup(id); !ok {
		return domain.NormalizedRequest{}, fmt.Errorf("未知节点协议")
	}
	if !domain.ValidModelID(r.Model) {
		return domain.NormalizedRequest{}, fmt.Errorf("model ID 无效")
	}
	if id == domain.ProtocolTK2SD {
		return normalizeTK2SD(r, c)
	}
	if r.Resolution == "" {
		return domain.NormalizedRequest{}, fmt.Errorf("resolution 必填")
	}
	model := r.Model
	if id != domain.ProtocolOfficial && model != "MiniMax-H3" {
		return domain.NormalizedRequest{}, domain.ErrUnsupportedModel
	}
	r.Model = "MiniMax-H3"
	v, err := ValidateCreate(r, nil)
	if err != nil {
		return domain.NormalizedRequest{}, err
	}
	v.Model = model
	got := normalized(v.CreateRequest, v.Scenario, v.Prompt, v.InputImageCount, id)
	if !c.Accepts(got.Requirements) {
		return domain.NormalizedRequest{}, domain.ErrModelInput
	}
	return got, nil
}
func normalized(r domain.GenerationRequest, scenario, prompt string, images int, version string) domain.NormalizedRequest {
	req := domain.TaskRequirements{Scenario: scenario, Duration: r.Duration, Resolution: r.Resolution}
	for _, item := range r.Content {
		if item.Type == "text" {
			continue
		}
		req.MediaCount++
		req.Roles = append(req.Roles, item.Role)
		if media := item.Media(); media != nil {
			source, _, _ := strings.Cut(media.URL, ":")
			req.InputSources = append(req.InputSources, source)
		}
	}
	return domain.NormalizedRequest{Request: r, Scenario: scenario, Prompt: prompt, InputImageCount: images, NormalizerVersion: version, Requirements: req}
}
