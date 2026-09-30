package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/protocol"
	"strings"
)

func (s *Store) prepareTaskRouting(ctx context.Context, conn *sql.Conn, in *domain.NewTask) error {
	if in.ProtocolVersion == "" {
		rows, err := conn.QueryContext(ctx, `SELECT DISTINCT n.protocol_version FROM model_service_nodes n JOIN node_models m ON m.node_id=n.id WHERE n.enabled=1 AND n.deleted_at IS NULL AND m.enabled=1 AND m.present=1 AND m.model_id=?`, in.Model)
		if err != nil {
			return err
		}
		var protocols []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return err
			}
			protocols = append(protocols, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(protocols) > 1 {
			return domain.ErrRouteAmbiguous
		}
		if len(protocols) == 1 {
			in.ProtocolVersion = protocols[0]
		} else if len(in.Stages) > 0 {
			in.ProtocolVersion = domain.ProtocolH3
		} else {
			in.ProtocolVersion = domain.ProtocolLegacy
		}
	}
	if _, ok := protocol.Lookup(in.ProtocolVersion); !ok {
		return domain.ErrRouteUnavailable
	}
	if in.RequestNormalizer == "" {
		in.RequestNormalizer = "legacy-h3-v1"
	}
	if in.RoutingSnapshotJSON == "" {
		snapshot := domain.RouteSnapshot{SchemaVersion: 1, Model: in.Model, ProtocolVersion: in.ProtocolVersion, NormalizerVersion: in.RequestNormalizer, Requirements: domain.TaskRequirements{Scenario: in.Scenario, Duration: in.Duration, Resolution: in.Resolution}}
		data, _ := json.Marshal(snapshot)
		in.RoutingSnapshotJSON = string(data)
	}
	var snapshot domain.RouteSnapshot
	if err := json.Unmarshal([]byte(in.RoutingSnapshotJSON), &snapshot); err != nil {
		return err
	}
	if snapshot.ProtocolVersion != in.ProtocolVersion || snapshot.Model != in.Model {
		return domain.ErrRouteChanged
	}
	if in.RoutingRevision > 0 {
		var current int64
		if err := conn.QueryRowContext(ctx, `SELECT revision FROM routing_state WHERE id=1`).Scan(&current); err != nil {
			return err
		}
		if current != in.RoutingRevision {
			return domain.ErrRouteChanged
		}
		rows, err := conn.QueryContext(ctx, `SELECT n.id FROM model_service_nodes n JOIN node_models m ON m.node_id=n.id JOIN node_model_catalogs c ON c.node_id=n.id WHERE n.enabled=1 AND n.deleted_at IS NULL AND n.protocol_version=? AND m.model_id=? AND m.enabled=1 AND m.present=1 AND (c.source<>'discovered' OR c.valid_until>?)`, in.ProtocolVersion, in.Model, s.nowUnix())
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return domain.ErrRouteUnavailable
		}
		eligible := false
		for _, id := range ids {
			cat, err := catalogWith(ctx, conn, id)
			if err != nil {
				return err
			}
			for _, m := range cat.Items {
				if m.ModelID == in.Model && m.Enabled && m.Present && m.Capabilities.Accepts(snapshot.Requirements) {
					eligible = true
				}
			}
		}
		if !eligible {
			return domain.ErrModelInput
		}
	}
	in.RouteState = "ready"
	return nil
}

func taskEligibleForNode(ctx context.Context, q rowQuerier, task domain.Task, nodeID string, now int64) (bool, error) {
	if task.RouteState != "ready" || task.ProtocolVersion == "" {
		return false, nil
	}
	var nodeProtocol string
	var enabled int
	err := q.QueryRowContext(ctx, `SELECT protocol_version,enabled FROM model_service_nodes WHERE id=? AND deleted_at IS NULL`, nodeID).Scan(&nodeProtocol, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return task.ProtocolVersion == domain.ProtocolLegacy, nil
	}
	if err != nil {
		return false, err
	}
	if enabled != 1 || nodeProtocol != task.ProtocolVersion {
		return false, nil
	}
	cat, err := catalogWith(ctx, q, nodeID)
	if err != nil {
		return false, err
	}
	if cat.Source == "discovered" && cat.ValidUntil <= now {
		return false, nil
	}
	var snapshot domain.RouteSnapshot
	if err := json.Unmarshal([]byte(task.RoutingSnapshotJSON), &snapshot); err != nil {
		return false, err
	}
	for _, model := range cat.Items {
		if model.ModelID == task.Model && model.Enabled && model.Present && model.Capabilities.Accepts(snapshot.Requirements) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) BindLegacyTaskRoute(ctx context.Context, taskID, nodeProtocol string, version, revision int64) (result domain.Task, err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return result, err
	}
	defer completeTransaction(finish, &err)
	task, err := scanTask(conn.QueryRowContext(ctx, taskSelect+` WHERE task_id=? AND deleted_at IS NULL`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return result, domain.ErrTaskNotFound
	}
	if err != nil {
		return result, err
	}
	if task.Version != version || task.RouteState != "awaiting_route" || (task.Status != domain.StatusQueuedOpen && task.Status != domain.StatusQueuedLocked) || task.UpstreamJobID != "" || task.GradioEventID != "" || task.OfficialSubmissionBaselineSaved || !task.AttemptStartedAt.IsZero() {
		return result, domain.ErrStateConflict
	}
	var evidence int
	if err := conn.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM stage_attempts a JOIN task_stages st ON st.id=a.stage_id WHERE st.task_id=?)+(SELECT COUNT(*) FROM node_dispatch_barriers WHERE task_id=?)+(SELECT COUNT(*) FROM task_remote_runs WHERE task_id=?)`, taskID, taskID, taskID).Scan(&evidence); err != nil {
		return result, err
	}
	if evidence > 0 {
		return result, domain.ErrStateConflict
	}
	r, rev, err := routeWith(ctx, conn, task.Model)
	if err != nil {
		return result, err
	}
	if revision != rev {
		return result, domain.ErrRouteChanged
	}
	found := false
	for _, c := range r.Candidates {
		if c.ProtocolVersion == nodeProtocol {
			found = true
		}
	}
	if !found {
		return result, domain.ErrRouteUnavailable
	}
	var request domain.GenerationRequest
	if err := json.Unmarshal([]byte(task.RequestJSON), &request); err != nil {
		return result, err
	}
	// 内部引用只在元数据证明归属后用于迁移资格校验，真正读取仍由执行器完成。
	for i := range request.Content {
		item := &request.Content[i]
		media := item.Media()
		if media == nil {
			continue
		}
		if len(media.URL) > 14 && media.URL[:14] == "proxy-input://" {
			var count int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_input_spool_files WHERE task_id=? AND content_index=? AND content_type=? AND role=? AND 'proxy-input://' || task_id || '/' || id=?`, taskID, i, item.Type, item.Role, media.URL).Scan(&count); err != nil {
				return result, err
			}
			if count != 1 {
				return result, domain.ErrModelInput
			}
			media.URL = "https://migration.invalid/input"
		}
	}
	var chosen domain.NormalizedRequest
	ok := false
	for _, c := range r.Candidates {
		if c.ProtocolVersion != nodeProtocol {
			continue
		}
		for _, id := range c.NodeIDs {
			cat, e := catalogWith(ctx, conn, id)
			if e != nil {
				return result, e
			}
			for _, m := range cat.Items {
				if m.ModelID != task.Model || !m.Enabled || !m.Present {
					continue
				}
				n, e := protocol.Normalize(nodeProtocol, request, m.Capabilities)
				if e == nil {
					chosen = n
					ok = true
					break
				}
			}
		}
	}
	if !ok {
		return result, domain.ErrModelInput
	}
	def, _ := protocol.Lookup(nodeProtocol)
	if def.ParameterPlan == "h3_profile" && task.ConfigSnapshotJSON == "" {
		return result, domain.ErrModelInput
	}
	snapshot := domain.RouteSnapshot{SchemaVersion: 1, Model: task.Model, ProtocolVersion: nodeProtocol, RouteVersion: r.Version, RoutingRevision: rev, SelectionMode: "manual", ParameterPlan: def.ParameterPlan, NormalizerVersion: task.RequestNormalizer, Requirements: chosen.Requirements}
	data, _ := json.Marshal(snapshot)
	_, err = conn.ExecContext(ctx, `UPDATE video_tasks SET protocol_version=?,route_state='ready',routing_snapshot_json=?,routing_wait_reason='',updated_at=?,version=version+1 WHERE task_id=? AND version=?`, nodeProtocol, string(data), s.nowUnix(), taskID, version)
	if err != nil {
		return result, err
	}
	if err = s.rebalance(ctx, conn, s.nowUnix()); err != nil {
		return result, err
	}
	return scanTask(conn.QueryRowContext(ctx, taskSelect+` WHERE task_id=?`, taskID))
}

func (s *Store) FindIdempotentRecord(ctx context.Context, owner, keyHash string) (domain.Task, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT i.task_id FROM idempotency_keys i JOIN video_tasks t ON t.task_id=i.task_id WHERE i.api_key_id=? AND i.key_hash=? AND i.expires_at>? AND t.deleted_at IS NULL`, owner, keyHash, s.nowUnix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, domain.ErrTaskNotFound
	}
	if err != nil {
		return domain.Task{}, err
	}
	return s.Get(ctx, owner, id)
}

func nextEligibleTask(ctx context.Context, conn *sql.Conn, nodeID string, now int64) (domain.Task, error) {
	var after int64
	for {
		rows, err := conn.QueryContext(ctx, taskSelect+` WHERE deleted_at IS NULL AND route_state='ready' AND status IN ('queued_open','queued_locked') AND queue_seq>? ORDER BY queue_seq LIMIT 100`, after)
		if err != nil {
			return domain.Task{}, err
		}
		var tasks []domain.Task
		for rows.Next() {
			task, err := scanTask(rows)
			if err != nil {
				rows.Close()
				return domain.Task{}, err
			}
			tasks = append(tasks, task)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return domain.Task{}, err
		}
		for _, task := range tasks {
			after = task.QueueSeq
			eligible, err := taskEligibleForNode(ctx, conn, task, nodeID, now)
			if err != nil {
				return domain.Task{}, err
			}
			if !eligible {
				continue
			}
			var request domain.GenerationRequest
			if json.Unmarshal([]byte(task.RequestJSON), &request) != nil {
				continue
			}
			valid := true
			for index, item := range request.Content {
				media := item.Media()
				if media == nil {
					continue
				}
				source, tail, _ := strings.Cut(media.URL, ":")
				source = strings.ToLower(source)
				switch source {
				case "http", "https", "data":
				case "mm_file":
					valid = task.ProtocolVersion == domain.ProtocolOfficial && strings.HasPrefix(tail, "//") && len(tail) > 2
				case "proxy-input":
					var count int
					err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_input_spool_files WHERE task_id=? AND content_index=? AND content_type=? AND role=? AND 'proxy-input://' || task_id || '/' || id=?`, task.TaskID, index, item.Type, item.Role, media.URL).Scan(&count)
					if err != nil {
						return domain.Task{}, err
					}
					valid = count == 1
				default:
					valid = false
				}
				if !valid {
					break
				}
			}
			if valid {
				return task, nil
			}
		}
		if len(tasks) < 100 {
			return domain.Task{}, domain.ErrQueueEmpty
		}
	}
}
