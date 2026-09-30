package protocol

import (
	"minimax-h3-tc/internal/domain"
	"testing"
)

func TestNormalizeByProtocol(t *testing.T) {
	capability := domain.ModelCapability{SchemaVersion: 1, Modes: []domain.ModeCapability{{Scenario: "r2va", Durations: []int{4, 5, 6}}}, InputSources: []string{"http", "https", "data"}, ResolutionPolicy: "ignored", MaxMedia: 12}
	req := domain.GenerationRequest{Model: "example-reference-model", Content: []domain.GenerationContent{{Type: "text", Text: "product"}, {Type: "image_url", ImageURL: &domain.MediaURL{URL: "https://example.com/image.png"}}}}
	got, err := Normalize("tk2sd-v1", req, capability)
	if err != nil {
		t.Fatal(err)
	}
	if got.Request.Duration != 5 || got.Request.Resolution != "" || got.Scenario != "r2va" || got.Request.Content[1].Role != "reference_image" {
		t.Fatalf("normalized=%+v", got)
	}
	if req.Content[1].Role != "" {
		t.Fatal("normalization mutated input")
	}
	textOnly := req
	textOnly.Content = textOnly.Content[:1]
	if _, err := Normalize("tk2sd-v1", textOnly, capability); err == nil {
		t.Fatal("reference-only model accepted text")
	}
	req.Content[1].Role = "last_frame"
	if _, err := Normalize("tk2sd-v1", req, capability); err == nil {
		t.Fatal("tk2sd accepted last frame")
	}
	if _, err := Normalize("unknown", req, capability); err == nil {
		t.Fatal("unknown protocol accepted")
	}
}

func TestCapabilitiesDoNotCombineModes(t *testing.T) {
	cap := domain.ModelCapability{SchemaVersion: 1, Modes: []domain.ModeCapability{{Scenario: "t2va", Durations: []int{5}}, {Scenario: "r2va", Durations: []int{10}}}}
	if cap.Supports("t2va", 10) {
		t.Fatal("mixed mode duration capabilities")
	}
}

func TestNormalizeRequiresH3ResolutionAndRejectsExplicitZero(t *testing.T) {
	req := domain.GenerationRequest{Model: "MiniMax-H3", Duration: 5, Ratio: "16:9", Content: []domain.GenerationContent{{Type: "text", Text: "test"}}}
	if _, err := Normalize("h3-node-v1", req, BuiltinCapability("h3-node-v1")); err == nil {
		t.Fatal("H3 accepted missing resolution")
	}
	req.Model = "video-1.5-pro"
	req.Duration = 0
	req.DurationPresent = true
	if _, err := Normalize("tk2sd-v1", req, domain.ModelCapability{Modes: []domain.ModeCapability{{Scenario: "t2va", Durations: []int{5}}}}); err == nil {
		t.Fatal("explicit zero defaulted")
	}
}
