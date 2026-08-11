package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"minimax-h3-tc/internal/domain"
)

const modelNodeSelect = `SELECT id,base_url,jobs_base_url,public_base_url,health_path,submit_api_name,check_api_name,poll_interval_ms,request_timeout_ms,enabled,version,created_at,updated_at FROM model_service_nodes`

func (s *Store) ListModelNodes(ctx context.Context) ([]domain.ModelNode, error) {
	rows, err := s.db.QueryContext(ctx, modelNodeSelect+` WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []domain.ModelNode
	for rows.Next() {
		node, err := scanModelNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

func (s *Store) GetModelNode(ctx context.Context, id string) (domain.ModelNode, error) {
	node, err := scanModelNode(s.db.QueryRowContext(ctx, modelNodeSelect+` WHERE id=? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ModelNode{}, domain.ErrNodeNotFound
	}
	return node, err
}

func (s *Store) CreateModelNode(ctx context.Context, input domain.ModelNodeInput) (nodeResult domain.ModelNode, err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return domain.ModelNode{}, err
	}
	defer completeTransaction(finish, &err)
	var exists int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_service_nodes WHERE id=?`, input.ID).Scan(&exists); err != nil {
		return domain.ModelNode{}, err
	}
	if exists > 0 {
		return domain.ModelNode{}, domain.ErrNodeIDConflict
	}
	now := s.nowUnix()
	if _, err := conn.ExecContext(ctx, `INSERT INTO model_service_nodes(id,base_url,jobs_base_url,public_base_url,health_path,submit_api_name,check_api_name,poll_interval_ms,request_timeout_ms,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		input.ID, input.BaseURL, input.JobsBaseURL, input.PublicBaseURL, input.HealthPath, input.SubmitAPIName, input.CheckAPIName,
		input.PollInterval.Milliseconds(), input.RequestTimeout.Milliseconds(), boolInt(input.Enabled), now, now); err != nil {
		return domain.ModelNode{}, err
	}
	return scanModelNode(conn.QueryRowContext(ctx, modelNodeSelect+` WHERE id=? AND deleted_at IS NULL`, input.ID))
}

func (s *Store) UpdateModelNode(ctx context.Context, id string, expectedVersion int64, input domain.ModelNodeInput) (nodeResult domain.ModelNode, err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return domain.ModelNode{}, err
	}
	defer completeTransaction(finish, &err)
	current, err := scanModelNode(conn.QueryRowContext(ctx, modelNodeSelect+` WHERE id=? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ModelNode{}, domain.ErrNodeNotFound
	}
	if err != nil {
		return domain.ModelNode{}, err
	}
	if current.Version != expectedVersion {
		return domain.ModelNode{}, domain.ErrNodeVersionConflict
	}
	active, err := activeTaskCount(ctx, conn, id)
	if err != nil {
		return domain.ModelNode{}, err
	}
	if active > 0 && !(current.Enabled && !input.Enabled && sameNodeConnection(current.ModelNodeInput, input)) {
		return domain.ModelNode{}, domain.ErrNodeHasActiveTask
	}
	now := s.nowUnix()
	result, err := conn.ExecContext(ctx, `UPDATE model_service_nodes SET base_url=?,jobs_base_url=?,public_base_url=?,health_path=?,submit_api_name=?,check_api_name=?,poll_interval_ms=?,request_timeout_ms=?,enabled=?,version=version+1,updated_at=? WHERE id=? AND version=? AND deleted_at IS NULL`,
		input.BaseURL, input.JobsBaseURL, input.PublicBaseURL, input.HealthPath, input.SubmitAPIName, input.CheckAPIName,
		input.PollInterval.Milliseconds(), input.RequestTimeout.Milliseconds(), boolInt(input.Enabled), now, id, expectedVersion)
	if err := oneRow(result, err); err != nil {
		if errors.Is(err, domain.ErrStateConflict) {
			return domain.ModelNode{}, domain.ErrNodeVersionConflict
		}
		return domain.ModelNode{}, err
	}
	return scanModelNode(conn.QueryRowContext(ctx, modelNodeSelect+` WHERE id=? AND deleted_at IS NULL`, id))
}

func (s *Store) DeleteModelNode(ctx context.Context, id string, expectedVersion int64) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	current, err := scanModelNode(conn.QueryRowContext(ctx, modelNodeSelect+` WHERE id=? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNodeNotFound
	}
	if err != nil {
		return err
	}
	if current.Version != expectedVersion {
		return domain.ErrNodeVersionConflict
	}
	if current.Enabled {
		return domain.ErrNodeMustBeDisabled
	}
	active, err := activeTaskCount(ctx, conn, id)
	if err != nil {
		return err
	}
	if active > 0 {
		return domain.ErrNodeHasActiveTask
	}
	now := s.nowUnix()
	result, err := conn.ExecContext(ctx, `UPDATE model_service_nodes SET deleted_at=?,updated_at=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`, now, now, id, expectedVersion)
	if err := oneRow(result, err); errors.Is(err, domain.ErrStateConflict) {
		return domain.ErrNodeVersionConflict
	} else {
		return err
	}
}

func (s *Store) LegacyNodeImportPending(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_config_bootstrap WHERE source='yaml_upstreams'`).Scan(&count); err != nil {
		return false, err
	}
	return count == 0, nil
}

func (s *Store) ImportLegacyNodes(ctx context.Context, inputs []domain.ModelNodeInput) (countResult int, importedResult bool, err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return 0, false, err
	}
	defer completeTransaction(finish, &err)
	var importedCount int
	err = conn.QueryRowContext(ctx, `SELECT imported_count FROM node_config_bootstrap WHERE source='yaml_upstreams'`).Scan(&importedCount)
	if err == nil {
		return importedCount, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	var existing int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_service_nodes`).Scan(&existing); err != nil {
		return 0, false, err
	}
	if existing == 0 {
		sorted := append([]domain.ModelNodeInput(nil), inputs...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
		now := s.nowUnix()
		for _, input := range sorted {
			if _, err := conn.ExecContext(ctx, `INSERT INTO model_service_nodes(id,base_url,jobs_base_url,public_base_url,health_path,submit_api_name,check_api_name,poll_interval_ms,request_timeout_ms,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
				input.ID, input.BaseURL, input.JobsBaseURL, input.PublicBaseURL, input.HealthPath, input.SubmitAPIName, input.CheckAPIName,
				input.PollInterval.Milliseconds(), input.RequestTimeout.Milliseconds(), boolInt(input.Enabled), now, now); err != nil {
				return 0, false, err
			}
		}
		importedCount = len(sorted)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO node_config_bootstrap(source,imported_count,completed_at) VALUES('yaml_upstreams',?,?)`, importedCount, s.nowUnix()); err != nil {
		return 0, false, err
	}
	return importedCount, existing == 0, nil
}

func activeTaskCount(ctx context.Context, query rowQuerier, nodeID string) (int, error) {
	var count int
	err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_tasks WHERE upstream_id=? AND status IN ('dispatching','running','reconciling','cancelling') AND deleted_at IS NULL`, nodeID).Scan(&count)
	return count, err
}

func sameNodeConnection(left, right domain.ModelNodeInput) bool {
	return left.BaseURL == right.BaseURL && left.JobsBaseURL == right.JobsBaseURL && left.PublicBaseURL == right.PublicBaseURL &&
		left.HealthPath == right.HealthPath && left.SubmitAPIName == right.SubmitAPIName && left.CheckAPIName == right.CheckAPIName &&
		left.PollInterval == right.PollInterval && left.RequestTimeout == right.RequestTimeout
}

func scanModelNode(scanner rowScanner) (domain.ModelNode, error) {
	var node domain.ModelNode
	var pollMS, timeoutMS, enabled, created, updated int64
	err := scanner.Scan(&node.ID, &node.BaseURL, &node.JobsBaseURL, &node.PublicBaseURL, &node.HealthPath, &node.SubmitAPIName, &node.CheckAPIName,
		&pollMS, &timeoutMS, &enabled, &node.Version, &created, &updated)
	if err != nil {
		return domain.ModelNode{}, err
	}
	node.PollInterval = durationMilliseconds(pollMS)
	node.RequestTimeout = durationMilliseconds(timeoutMS)
	node.Enabled = enabled == 1
	node.CreatedAt = unix(created)
	node.UpdatedAt = unix(updated)
	return node, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func durationMilliseconds(value int64) time.Duration {
	return time.Duration(value) * time.Millisecond
}
