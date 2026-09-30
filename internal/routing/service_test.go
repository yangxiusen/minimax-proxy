package routing

import (
	"context"
	"errors"
	"minimax-h3-tc/internal/domain"
	"testing"
)

type routeFixture struct {
	route    domain.ModelRoute
	nodes    []domain.ModelNode
	catalogs map[string]domain.ModelCatalog
}

func (f routeFixture) GetModelRoute(context.Context, string) (domain.ModelRoute, int64, error) {
	return f.route, 1, nil
}
func (f routeFixture) ListModelNodes(context.Context) ([]domain.ModelNode, error) {
	return f.nodes, nil
}
func (f routeFixture) GetModelCatalog(_ context.Context, id string) (domain.ModelCatalog, error) {
	return f.catalogs[id], nil
}
func TestResolvePinsAutomaticProtocol(t *testing.T) {
	f := routeFixture{route: domain.ModelRoute{Model: "model", ProtocolVersion: domain.ProtocolTK2SD, SelectionMode: "auto", Candidates: []domain.RouteCandidate{{ProtocolVersion: domain.ProtocolOfficial, NodeIDs: []string{"other"}}}}}
	service := Service{Store: f}
	_, err := service.Resolve(context.Background(), domain.GenerationRequest{Model: "model"})
	if !errors.Is(err, domain.ErrRouteAmbiguous) && !errors.Is(err, domain.ErrRouteUnavailable) {
		t.Fatalf("err=%v", err)
	}
	f.route.SelectionMode = "manual"
	service.Store = f
	_, err = service.Resolve(context.Background(), domain.GenerationRequest{Model: "model"})
	if !errors.Is(err, domain.ErrRouteUnavailable) {
		t.Fatalf("manual err=%v", err)
	}
}
