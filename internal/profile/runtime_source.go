package profile

import (
	"context"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/monitor"
)

type RuntimeNodeSource interface {
	ListModelNodes(context.Context) ([]domain.ModelNode, error)
}

type RuntimeCapabilitySource struct {
	Nodes RuntimeNodeSource
	Cache *monitor.Cache
}

func (source RuntimeCapabilitySource) ListCapabilitySnapshots(ctx context.Context) ([]NodeCapabilitySnapshot, error) {
	if source.Nodes == nil {
		return nil, domain.ErrNoCompatibleNode
	}
	nodes, err := source.Nodes.ListModelNodes(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]NodeCapabilitySnapshot, 0, len(nodes))
	for _, node := range nodes {
		var snapshot monitor.NodeSnapshot
		found := false
		if source.Cache != nil {
			snapshot, found = source.Cache.Get(node.ID)
		}
		result = append(result, NodeCapabilitySnapshot{
			NodeID: node.ID, Enabled: node.Enabled, ProtocolVersion: node.ProtocolVersion,
			Healthy:      found && snapshot.Health == monitor.HealthHealthy && !snapshot.Applying,
			Capabilities: snapshot.Capabilities,
		})
	}
	return result, nil
}
