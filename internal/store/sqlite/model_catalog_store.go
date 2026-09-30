package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/protocol"
)

func CatalogFingerprint(n domain.ModelNodeInput) string {
	sum := sha256.Sum256([]byte(n.ProtocolVersion + "\x00" + n.ServiceURL + "\x00" + n.APIKeyFingerprint))
	return hex.EncodeToString(sum[:])
}

func (s *Store) GetModelCatalog(ctx context.Context, nodeID string) (domain.ModelCatalog, error) {
	return catalogWith(ctx, s.db, nodeID)
}
func catalogWith(ctx context.Context, q rowQuerier, nodeID string) (domain.ModelCatalog, error) {
	c := domain.ModelCatalog{NodeID: nodeID, Items: []domain.NodeModel{}}
	err := q.QueryRowContext(ctx, `SELECT source,revision,connection_fingerprint,last_status,COALESCE(last_attempt_at,0),COALESCE(last_success_at,0),COALESCE(valid_until,0),last_error_code FROM node_model_catalogs WHERE node_id=?`, nodeID).Scan(&c.Source, &c.Revision, &c.Fingerprint, &c.Status, &c.LastAttemptAt, &c.LastSuccessAt, &c.ValidUntil, &c.LastErrorCode)
	if errors.Is(err, sql.ErrNoRows) {
		return c, domain.ErrNodeNotFound
	}
	if err != nil {
		return c, err
	}
	rows, err := q.QueryContext(ctx, `SELECT model_id,enabled,present,verified,capabilities_json FROM node_models WHERE node_id=? ORDER BY model_id`, nodeID)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var m domain.NodeModel
		var enabled, present, verified int
		var data string
		if err := rows.Scan(&m.ModelID, &enabled, &present, &verified, &data); err != nil {
			return c, err
		}
		if err := json.Unmarshal([]byte(data), &m.Capabilities); err != nil {
			return c, err
		}
		m.Enabled = enabled == 1
		m.Present = present == 1
		m.Verified = verified == 1
		c.Items = append(c.Items, m)
	}
	return c, rows.Err()
}

func (s *Store) writeNodeCatalog(ctx context.Context, conn *sql.Conn, n domain.ModelNodeInput, now int64) error {
	old, err := catalogWith(ctx, conn, n.ID)
	exists := err == nil
	if err != nil && !errors.Is(err, domain.ErrNodeNotFound) {
		return err
	}
	fingerprint := CatalogFingerprint(n)
	if n.ModelCatalog == nil && exists && old.Fingerprint == fingerprint {
		if n.Enabled && !catalogHasEnabled(old, now) {
			return domain.ErrRouteUnavailable
		}
		if err := syncModelRoutes(ctx, conn, now); err != nil {
			return err
		}
		return bumpRouting(ctx, conn, now)
	}
	var c domain.ModelCatalog
	if n.ModelCatalog != nil {
		c = *n.ModelCatalog
		if exists && c.Revision != old.Revision {
			return domain.ErrCatalogConflict
		}
		if exists && c.Source == "discovered" && old.Fingerprint == fingerprint {
			if old.LastSuccessAt > c.LastSuccessAt {
				c.LastSuccessAt = old.LastSuccessAt
				c.ValidUntil = old.ValidUntil
			}
			if old.LastAttemptAt > c.LastAttemptAt {
				c.LastAttemptAt = old.LastAttemptAt
				c.Status = old.Status
				c.LastErrorCode = old.LastErrorCode
			}
		}
	} else {
		definition, ok := protocol.Lookup(n.ProtocolVersion)
		if !ok {
			return fmt.Errorf("未知节点协议")
		}
		c = domain.ModelCatalog{Source: definition.ModelSource, Status: "ready", LastSuccessAt: now, Items: []domain.NodeModel{}}
		if definition.ModelSource == "discovered" {
			c.Status = "uninitialized"
			c.LastSuccessAt = 0
		} else {
			c.Items = []domain.NodeModel{{ModelID: "MiniMax-H3", Enabled: true, Present: true, Verified: definition.ModelSource == "builtin", Capabilities: protocol.BuiltinCapability(n.ProtocolVersion)}}
		}
	}
	if len(c.Items) > 256 {
		return fmt.Errorf("节点模型数量超过限制")
	}
	if err := validateCatalogItems(c.Items); err != nil {
		return err
	}
	if n.Enabled && !catalogHasEnabled(c, now) {
		return domain.ErrRouteUnavailable
	}
	revision := int64(1)
	if exists {
		revision = old.Revision + 1
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO node_model_catalogs(node_id,source,revision,connection_fingerprint,last_status,last_attempt_at,last_success_at,valid_until,last_error_code,created_at,updated_at) VALUES(?,?,?,?,?,NULLIF(?,0),NULLIF(?,0),NULLIF(?,0),?,?,?) ON CONFLICT(node_id) DO UPDATE SET source=excluded.source,revision=excluded.revision,connection_fingerprint=excluded.connection_fingerprint,last_status=excluded.last_status,last_attempt_at=excluded.last_attempt_at,last_success_at=excluded.last_success_at,valid_until=excluded.valid_until,last_error_code=excluded.last_error_code,updated_at=excluded.updated_at`, n.ID, c.Source, revision, fingerprint, c.Status, c.LastAttemptAt, c.LastSuccessAt, c.ValidUntil, c.LastErrorCode, now, now)
	if err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `UPDATE node_models SET present=0,updated_at=? WHERE node_id=?`, now, n.ID); err != nil {
		return err
	}
	for _, m := range c.Items {
		if err := writeNodeModel(ctx, conn, n.ID, m, now); err != nil {
			return err
		}
	}
	if err := syncModelRoutes(ctx, conn, now); err != nil {
		return err
	}
	return bumpRouting(ctx, conn, now)
}
func catalogHasEnabled(c domain.ModelCatalog, now int64) bool {
	if c.Source == "discovered" && (c.LastSuccessAt == 0 || c.ValidUntil <= now) {
		return false
	}
	for _, m := range c.Items {
		if m.Enabled && m.Present {
			return true
		}
	}
	return false
}
func validateCatalogItems(items []domain.NodeModel) error {
	seen := map[string]bool{}
	for _, m := range items {
		if !domain.ValidModelID(m.ModelID) || seen[m.ModelID] {
			return fmt.Errorf("模型 ID 无效或重复")
		}
		seen[m.ModelID] = true
		if len(m.Capabilities.Modes) == 0 {
			return fmt.Errorf("模型能力缺失")
		}
	}
	return nil
}
func writeNodeModel(ctx context.Context, conn *sql.Conn, nodeID string, m domain.NodeModel, now int64) error {
	data, err := json.Marshal(m.Capabilities)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO node_models(node_id,model_id,enabled,present,capabilities_json,verified,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(node_id,model_id) DO UPDATE SET enabled=excluded.enabled,present=excluded.present,capabilities_json=excluded.capabilities_json,verified=excluded.verified,updated_at=excluded.updated_at`, nodeID, m.ModelID, boolInt(m.Enabled), boolInt(m.Present), string(data), boolInt(m.Verified), now, now)
	return err
}
func bumpRouting(ctx context.Context, conn *sql.Conn, now int64) error {
	_, err := conn.ExecContext(ctx, `UPDATE routing_state SET revision=revision+1,updated_at=? WHERE id=1`, now)
	return err
}
func syncModelRoutes(ctx context.Context, conn *sql.Conn, now int64) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO model_routes(model_id,protocol_version,selection_mode,version,updated_by,created_at,updated_at)
 SELECT m.model_id,CASE WHEN COUNT(DISTINCT n.protocol_version)=1 THEN MIN(n.protocol_version) ELSE NULL END,CASE WHEN COUNT(DISTINCT n.protocol_version)=1 THEN 'auto' ELSE 'unresolved' END,1,'system',?,?
 FROM node_models m JOIN model_service_nodes n ON n.id=m.node_id WHERE m.enabled=1 AND m.present=1 AND n.enabled=1 AND n.deleted_at IS NULL GROUP BY m.model_id ON CONFLICT(model_id) DO NOTHING`, now, now)
	return err
}

func (s *Store) RefreshModelCatalog(ctx context.Context, nodeID string, nodeVersion, catalogRevision int64, items []domain.NodeModel, errorCode string) (result domain.ModelCatalog, err error) {
	if len(items) > 256 {
		return result, fmt.Errorf("模型清单过大")
	}
	if errorCode == "" {
		if err = validateCatalogItems(items); err != nil {
			return result, err
		}
	}
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return result, err
	}
	defer completeTransaction(finish, &err)
	node, err := scanModelNode(conn.QueryRowContext(ctx, modelNodeSelect+` WHERE id=? AND deleted_at IS NULL`, nodeID))
	if err != nil {
		return result, err
	}
	if node.Version != nodeVersion {
		return result, domain.ErrNodeVersionConflict
	}
	old, err := catalogWith(ctx, conn, nodeID)
	if err != nil {
		return result, err
	}
	if old.Revision != catalogRevision || old.Fingerprint != CatalogFingerprint(node.ModelNodeInput) {
		return result, domain.ErrCatalogConflict
	}
	if old.Source != "discovered" {
		return result, fmt.Errorf("协议不支持模型发现")
	}
	now := s.nowUnix()
	if errorCode != "" {
		_, err = conn.ExecContext(ctx, `UPDATE node_model_catalogs SET last_status='error',last_attempt_at=?,last_error_code=?,updated_at=? WHERE node_id=?`, now, errorCode, now, nodeID)
		if err != nil {
			return result, err
		}
		return catalogWith(ctx, conn, nodeID)
	}
	previous := map[string]domain.NodeModel{}
	for _, m := range old.Items {
		previous[m.ModelID] = m
	}
	changed := false
	current := map[string]bool{}
	for i := range items {
		m := &items[i]
		m.Present = true
		m.Verified = true
		m.Enabled = previous[m.ModelID].Enabled
		current[m.ModelID] = true
		before, _ := json.Marshal(previous[m.ModelID])
		after, _ := json.Marshal(*m)
		if string(before) != string(after) {
			changed = true
		}
	}
	for _, m := range old.Items {
		if m.Present && !current[m.ModelID] {
			changed = true
		}
	}
	status := "ready"
	if len(items) == 0 {
		status = "empty"
	}
	revision := old.Revision
	if changed {
		revision++
	}
	_, err = conn.ExecContext(ctx, `UPDATE node_model_catalogs SET revision=?,last_status=?,last_attempt_at=?,last_success_at=?,valid_until=?,last_error_code='',updated_at=? WHERE node_id=?`, revision, status, now, now, now+1800, now, nodeID)
	if err != nil {
		return result, err
	}
	if _, err = conn.ExecContext(ctx, `UPDATE node_models SET present=0,updated_at=? WHERE node_id=?`, now, nodeID); err != nil {
		return result, err
	}
	for _, m := range items {
		if err = writeNodeModel(ctx, conn, nodeID, m, now); err != nil {
			return result, err
		}
	}
	if changed {
		if err = syncModelRoutes(ctx, conn, now); err != nil {
			return result, err
		}
		if err = bumpRouting(ctx, conn, now); err != nil {
			return result, err
		}
	}
	return catalogWith(ctx, conn, nodeID)
}
