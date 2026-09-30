package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"minimax-h3-tc/internal/domain"
)

func (s *Store) GetModelRoute(ctx context.Context, model string) (domain.ModelRoute, int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.ModelRoute{}, 0, err
	}
	defer tx.Rollback()
	r, rev, err := routeWith(ctx, tx, model)
	if err != nil {
		return r, rev, err
	}
	return r, rev, tx.Commit()
}
func routeWith(ctx context.Context, q rowQuerier, model string) (domain.ModelRoute, int64, error) {
	r := domain.ModelRoute{Model: model, Candidates: []domain.RouteCandidate{}}
	var revision int64
	if err := q.QueryRowContext(ctx, `SELECT revision FROM routing_state WHERE id=1`).Scan(&revision); err != nil {
		return r, 0, err
	}
	err := q.QueryRowContext(ctx, `SELECT COALESCE(protocol_version,''),selection_mode,version FROM model_routes WHERE model_id=?`, model).Scan(&r.ProtocolVersion, &r.SelectionMode, &r.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return r, revision, domain.ErrUnsupportedModel
	}
	if err != nil {
		return r, revision, err
	}
	rows, err := q.QueryContext(ctx, `SELECT n.protocol_version,n.id FROM node_models m JOIN model_service_nodes n ON n.id=m.node_id WHERE m.model_id=? AND m.present=1 AND m.enabled=1 AND n.enabled=1 AND n.deleted_at IS NULL ORDER BY n.protocol_version,n.id`, model)
	if err != nil {
		return r, revision, err
	}
	for rows.Next() {
		var p, id string
		if err := rows.Scan(&p, &id); err != nil {
			rows.Close()
			return r, revision, err
		}
		if len(r.Candidates) == 0 || r.Candidates[len(r.Candidates)-1].ProtocolVersion != p {
			r.Candidates = append(r.Candidates, domain.RouteCandidate{ProtocolVersion: p, NodeIDs: []string{}})
		}
		last := len(r.Candidates) - 1
		r.Candidates[last].NodeIDs = append(r.Candidates[last].NodeIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return r, revision, err
	}
	r.State = "unavailable"
	if r.ProtocolVersion == "" {
		r.State = "ambiguous"
	}
	for _, c := range r.Candidates {
		if c.ProtocolVersion == r.ProtocolVersion && r.State != "ambiguous" {
			r.State = "ready"
		}
		if r.SelectionMode != "manual" && c.ProtocolVersion != r.ProtocolVersion {
			r.State = "ambiguous"
		}
	}
	if r.State == "ambiguous" {
		r.WaitReason = "multiple_protocols"
	} else if r.State == "unavailable" {
		r.WaitReason = "no_protocol_nodes"
	}
	err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_tasks WHERE model=? AND route_state='awaiting_route' AND deleted_at IS NULL AND status IN ('queued_open','queued_locked')`, model).Scan(&r.LegacyAwaitingTasks)
	return r, revision, err
}
func (s *Store) ListModelRoutes(ctx context.Context) ([]domain.ModelRoute, int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT model_id FROM model_routes ORDER BY model_id`)
	if err != nil {
		return nil, 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	result := []domain.ModelRoute{}
	var rev int64
	for _, id := range ids {
		route, r, err := routeWith(ctx, tx, id)
		if err != nil {
			return nil, 0, err
		}
		rev = r
		result = append(result, route)
	}
	if len(ids) == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM routing_state WHERE id=1`).Scan(&rev); err != nil {
			return nil, 0, err
		}
	}
	return result, rev, tx.Commit()
}
func (s *Store) BindModelRoute(ctx context.Context, model, protocol, admin string, version, revision int64) (result domain.ModelRoute, rev int64, err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return result, 0, err
	}
	defer completeTransaction(finish, &err)
	r, current, err := routeWith(ctx, conn, model)
	if err != nil {
		return result, 0, err
	}
	if r.Version != version || current != revision {
		return result, 0, domain.ErrRouteChanged
	}
	found := false
	for _, c := range r.Candidates {
		if c.ProtocolVersion == protocol {
			found = true
		}
	}
	if !found {
		return result, 0, domain.ErrRouteUnavailable
	}
	now := s.nowUnix()
	_, err = conn.ExecContext(ctx, `UPDATE model_routes SET protocol_version=?,selection_mode='manual',version=version+1,updated_by=?,updated_at=? WHERE model_id=?`, protocol, admin, now, model)
	if err != nil {
		return result, 0, err
	}
	if err = bumpRouting(ctx, conn, now); err != nil {
		return result, 0, err
	}
	return routeWith(ctx, conn, model)
}
