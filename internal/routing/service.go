package routing

import (
	"context"
	"errors"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/protocol"
	"time"
)

type Store interface {
	GetModelRoute(context.Context, string) (domain.ModelRoute, int64, error)
	ListModelNodes(context.Context) ([]domain.ModelNode, error)
	GetModelCatalog(context.Context, string) (domain.ModelCatalog, error)
}
type Service struct {
	Store   Store
	Healthy func(string) bool
	Now     func() time.Time
}
type Decision struct {
	Snapshot   domain.RouteSnapshot
	Normalized domain.NormalizedRequest
}

func (s Service) Resolve(ctx context.Context, r domain.GenerationRequest) (Decision, error) {
	route, revision, err := s.Store.GetModelRoute(ctx, r.Model)
	if err != nil {
		return Decision{}, err
	}
	if route.ProtocolVersion == "" || route.SelectionMode == "unresolved" {
		return Decision{}, domain.ErrRouteAmbiguous
	}
	if route.SelectionMode != "manual" {
		for _, c := range route.Candidates {
			if c.ProtocolVersion != route.ProtocolVersion {
				return Decision{}, domain.ErrRouteAmbiguous
			}
		}
	}
	d, ok := protocol.Lookup(route.ProtocolVersion)
	if !ok {
		return Decision{}, domain.ErrRouteUnavailable
	}
	nodes, err := s.Store.ListModelNodes(ctx)
	if err != nil {
		return Decision{}, err
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	hasCapability := false
	var validationErr error
	for _, n := range nodes {
		if !n.Enabled || n.ProtocolVersion != route.ProtocolVersion {
			continue
		}
		catalog, err := s.Store.GetModelCatalog(ctx, n.ID)
		if err != nil {
			return Decision{}, err
		}
		if catalog.Source == "discovered" && (catalog.ValidUntil <= now.Unix() || catalog.LastSuccessAt == 0) {
			continue
		}
		for _, model := range catalog.Items {
			if model.ModelID != r.Model || !model.Enabled || !model.Present {
				continue
			}
			normalized, err := protocol.Normalize(route.ProtocolVersion, r, model.Capabilities)
			if err != nil {
				validationErr = err
				continue
			}
			hasCapability = true
			if s.Healthy != nil && !s.Healthy(n.ID) {
				continue
			}
			return Decision{Normalized: normalized, Snapshot: domain.RouteSnapshot{SchemaVersion: 1, Model: r.Model, ProtocolVersion: route.ProtocolVersion, RouteVersion: route.Version, RoutingRevision: revision, SelectionMode: route.SelectionMode, ParameterPlan: d.ParameterPlan, NormalizerVersion: normalized.NormalizerVersion, Requirements: normalized.Requirements}}, nil
		}
	}
	if !hasCapability && validationErr != nil {
		return Decision{}, errors.Join(domain.ErrModelInput, validationErr)
	}
	return Decision{}, domain.ErrRouteUnavailable
}
