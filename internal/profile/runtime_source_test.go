package profile

import (
	"context"
	"testing"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/monitor"
)

type runtimeNodesFake struct{ nodes []domain.ModelNode }

func (fake runtimeNodesFake) ListModelNodes(context.Context) ([]domain.ModelNode, error) {
	return fake.nodes, nil
}

func TestRuntimeCapabilitySourceRequiresHealthyRuntimeSnapshot(t *testing.T) {
	cache := monitor.NewCache([]monitor.NodeSnapshot{{
		ID: "healthy", Health: monitor.HealthHealthy,
		Capabilities: map[string]any{"scenarios": []string{"t2va"}},
	}})
	source := RuntimeCapabilitySource{
		Nodes: runtimeNodesFake{nodes: []domain.ModelNode{
			{ModelNodeInput: domain.ModelNodeInput{ID: "healthy", Enabled: true, ProtocolVersion: "h3-node-v1"}},
			{ModelNodeInput: domain.ModelNodeInput{ID: "missing", Enabled: true, ProtocolVersion: "h3-node-v1"}},
		}},
		Cache: cache,
	}

	items, err := source.ListCapabilitySnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || !items[0].Healthy || items[1].Healthy {
		t.Fatalf("snapshots=%+v", items)
	}
	items[0].Capabilities["changed"] = true
	original, _ := cache.Get("healthy")
	if _, leaked := original.Capabilities["changed"]; leaked {
		t.Fatal("capability snapshot mutation leaked into monitor cache")
	}
}
