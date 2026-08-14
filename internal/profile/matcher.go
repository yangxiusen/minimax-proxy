package profile

import (
	"context"
	"encoding/json"
	"slices"

	"minimax-h3-tc/internal/domain"
)

type NodeCapabilitySnapshot struct {
	NodeID          string
	Enabled         bool
	Healthy         bool
	ProtocolVersion string
	Capabilities    map[string]any
}

type CapabilitySource interface {
	ListCapabilitySnapshots(context.Context) ([]NodeCapabilitySnapshot, error)
}

type NodeMatcher interface {
	CompatibleNodes(context.Context, domain.ProfileConfig) ([]domain.CompatibleNode, error)
}

type CapabilityMatcher struct {
	Source CapabilitySource
}

func (matcher CapabilityMatcher) CompatibleNodes(ctx context.Context, config domain.ProfileConfig) ([]domain.CompatibleNode, error) {
	if matcher.Source == nil {
		return nil, domain.ErrNoCompatibleNode
	}
	snapshots, err := matcher.Source.ListCapabilitySnapshots(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]domain.CompatibleNode, 0, len(snapshots))
	for _, snapshot := range snapshots {
		reasons := compatibilityReasons(config, snapshot)
		items = append(items, domain.CompatibleNode{NodeID: snapshot.NodeID, Compatible: len(reasons) == 0, Reasons: reasons})
	}
	return items, nil
}

func compatibilityReasons(config domain.ProfileConfig, node NodeCapabilitySnapshot) []string {
	var reasons []string
	if !node.Enabled {
		reasons = append(reasons, "node_disabled")
	}
	if !node.Healthy {
		reasons = append(reasons, "node_unhealthy")
	}
	if node.ProtocolVersion != "h3-node-v1" {
		reasons = append(reasons, "protocol_incompatible")
	}
	capabilities := node.Capabilities
	for _, ratio := range domain.ProfileRatios {
		if !containsString(capabilities["ratios"], ratio) {
			reasons = append(reasons, "ratio_unsupported:"+ratio)
		}
	}
	if config.Generation.SageAttention != "off" && !nestedAvailable(capabilities, "acceleration", "sage_attention") {
		reasons = append(reasons, "sage_attention_unavailable")
	}
	if config.Generation.CacheMode != "off" && !nestedAvailable(capabilities, "acceleration", config.Generation.CacheMode) {
		reasons = append(reasons, config.Generation.CacheMode+"_unavailable")
	}
	if len(config.LoRAs) > 0 {
		lora, _ := capabilities["lora"].(map[string]any)
		if !boolValue(lora["available"]) {
			reasons = append(reasons, "lora_unavailable")
		} else if maxCount, ok := numberValue(lora["max_count"]); !ok || maxCount < float64(len(config.LoRAs)) {
			reasons = append(reasons, "lora_limit_incompatible")
		} else {
			for _, configured := range config.LoRAs {
				if !containsString(lora["models"], configured.Name) {
					reasons = append(reasons, "lora_model_missing:"+configured.Name)
				}
			}
		}
	}
	if config.Interpolation.Enabled && (!nestedAvailable(capabilities, "interpolation", config.Interpolation.Engine) || !nestedContainsNumber(capabilities, "interpolation", config.Interpolation.Engine, "scales", config.Interpolation.Scale)) {
		reasons = append(reasons, "interpolation_unavailable")
	}
	if config.Restoration.Enabled && (!nestedAvailable(capabilities, "restoration", config.Restoration.Engine) || !nestedContainsNumber(capabilities, "restoration", config.Restoration.Engine, "scales", config.Restoration.Scale)) {
		reasons = append(reasons, "restoration_unavailable")
	}
	return reasons
}

func nestedAvailable(root map[string]any, first, second string) bool {
	firstMap, ok := root[first].(map[string]any)
	if !ok {
		return false
	}
	secondMap, ok := firstMap[second].(map[string]any)
	return ok && boolValue(secondMap["available"])
}

func nestedContainsNumber(root map[string]any, first, second, key string, expected int) bool {
	firstMap, ok := root[first].(map[string]any)
	if !ok {
		return false
	}
	secondMap, ok := firstMap[second].(map[string]any)
	if !ok {
		return false
	}
	values, ok := secondMap[key].([]any)
	if !ok {
		if ints, ok := secondMap[key].([]int); ok {
			return slices.Contains(ints, expected)
		}
		return false
	}
	for _, value := range values {
		if number, ok := numberValue(value); ok && number == float64(expected) {
			return true
		}
	}
	return false
}

func containsString(value any, expected string) bool {
	switch values := value.(type) {
	case []string:
		return slices.Contains(values, expected)
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok && text == expected {
				return true
			}
		}
	}
	return false
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func numberValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}
