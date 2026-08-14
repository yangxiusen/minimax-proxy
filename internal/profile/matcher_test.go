package profile

import (
	"context"
	"testing"

	"minimax-h3-tc/internal/domain"
)

func TestCapabilityMatcherRequiresRealHealthyCompatibleNode(t *testing.T) {
	config := validConfig()
	source := capabilitySourceStub{snapshots: []NodeCapabilitySnapshot{
		{NodeID: "legacy", Enabled: true, Healthy: true, ProtocolVersion: "legacy-gradio-v1", Capabilities: fullCapabilities(config)},
		{NodeID: "offline", Enabled: true, Healthy: false, ProtocolVersion: "h3-node-v1", Capabilities: fullCapabilities(config)},
		{NodeID: "missing-rife", Enabled: true, Healthy: true, ProtocolVersion: "h3-node-v1", Capabilities: fullCapabilities(config)},
		{NodeID: "compatible", Enabled: true, Healthy: true, ProtocolVersion: "h3-node-v1", Capabilities: fullCapabilities(config)},
	}}
	interpolation := source.snapshots[2].Capabilities["interpolation"].(map[string]any)["rife"].(map[string]any)
	interpolation["available"] = false
	matcher := CapabilityMatcher{Source: source}
	items, err := matcher.CompatibleNodes(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Compatible || items[1].Compatible || items[2].Compatible || !items[3].Compatible {
		t.Fatalf("items=%+v", items)
	}
}

type capabilitySourceStub struct{ snapshots []NodeCapabilitySnapshot }

func (source capabilitySourceStub) ListCapabilitySnapshots(context.Context) ([]NodeCapabilitySnapshot, error) {
	return source.snapshots, nil
}

func fullCapabilities(config domain.ProfileConfig) map[string]any {
	loraModels := make([]any, 0, len(config.LoRAs))
	for _, lora := range config.LoRAs {
		loraModels = append(loraModels, lora.Name)
	}
	ratios := make([]any, 0, len(domain.ProfileRatios))
	for _, ratio := range domain.ProfileRatios {
		ratios = append(ratios, ratio)
	}
	return map[string]any{
		"scenarios": []any{"t2va", "i2va", "r2va"}, "ratios": ratios,
		"acceleration": map[string]any{
			"sage_attention": map[string]any{"available": true}, "easycache": map[string]any{"available": true}, "te_speed": map[string]any{"available": true},
		},
		"lora":          map[string]any{"available": true, "max_count": float64(4), "models": loraModels},
		"interpolation": map[string]any{"rife": map[string]any{"available": true, "scales": []any{float64(2)}}},
		"restoration":   map[string]any{"seedvr2": map[string]any{"available": true, "scales": []any{float64(2), float64(3), float64(4)}}},
		"watermark":     map[string]any{"ffmpeg": map[string]any{"available": true}},
	}
}
