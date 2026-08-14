package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"minimax-h3-tc/internal/domain"
)

const artifactLocationSelect = `SELECT id,artifact_id,node_id,node_artifact_id,COALESCE(storage_key_fingerprint,''),state,is_primary,size_bytes,sha256,created_at,COALESCE(verified_at,0),COALESCE(deleted_at,0) FROM artifact_locations`

func (s *Store) GetArtifactLocation(ctx context.Context, locationID string) (location ArtifactLocation, err error) {
	err = scanArtifactLocation(s.db.QueryRowContext(ctx, artifactLocationSelect+` WHERE id=?`, locationID), &location)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactLocation{}, ErrArtifactNotFound
	}
	return location, err
}

func (s *Store) GetActiveArtifactLocation(ctx context.Context, artifactID, nodeID string) (location ArtifactLocation, err error) {
	rows, err := s.db.QueryContext(ctx, artifactLocationSelect+` WHERE artifact_id=? AND node_id=? AND state='active' ORDER BY is_primary DESC,created_at DESC LIMIT 2`, artifactID, nodeID)
	if err != nil {
		return ArtifactLocation{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return ArtifactLocation{}, ErrArtifactNotFound
	}
	if err := scanArtifactLocation(rows, &location); err != nil {
		return ArtifactLocation{}, err
	}
	if rows.Next() {
		return ArtifactLocation{}, fmt.Errorf("同一产物在节点上存在多个活动位置")
	}
	return location, rows.Err()
}

func (s *Store) GetPrimaryArtifactLocation(ctx context.Context, artifactID string) (location ArtifactLocation, err error) {
	err = scanArtifactLocation(s.db.QueryRowContext(ctx, artifactLocationSelect+` WHERE artifact_id=? AND state='active' AND is_primary=1`, artifactID), &location)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactLocation{}, ErrArtifactNotFound
	}
	return location, err
}

func scanArtifactLocation(scanner rowScanner, location *ArtifactLocation) error {
	var primary int
	err := scanner.Scan(
		&location.ID, &location.ArtifactID, &location.NodeID, &location.NodeArtifactID, &location.StorageKeyFingerprint,
		&location.State, &primary, &location.SizeBytes, &location.SHA256, &location.CreatedAt, &location.VerifiedAt, &location.DeletedAt,
	)
	location.IsPrimary = primary == 1
	return err
}

type ArtifactLocation struct {
	ID                    string
	ArtifactID            string
	NodeID                string
	NodeArtifactID        string
	StorageKeyFingerprint string
	State                 string
	IsPrimary             bool
	SizeBytes             int64
	SHA256                string
	CreatedAt             int64
	VerifiedAt            int64
	DeletedAt             int64
}

func (s *Store) CreateArtifactLocation(ctx context.Context, location ArtifactLocation) error {
	if location.State == "" {
		location.State = "active"
	}
	if location.CreatedAt == 0 {
		location.CreatedAt = s.nowMillis()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO artifact_locations(id,artifact_id,node_id,node_artifact_id,storage_key_fingerprint,state,is_primary,size_bytes,sha256,created_at,verified_at) VALUES(?,?,?,?,NULLIF(?,''),?,?,?,?,?,NULLIF(?,0))`, location.ID, location.ArtifactID, location.NodeID, location.NodeArtifactID, location.StorageKeyFingerprint, location.State, boolInt(location.IsPrimary), location.SizeBytes, location.SHA256, location.CreatedAt, location.VerifiedAt)
	return err
}

func (s *Store) UpdateImportingArtifactLocation(ctx context.Context, locationID, nodeArtifactID string, sizeBytes int64, digest string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE artifact_locations SET node_artifact_id=?,size_bytes=?,sha256=? WHERE id=? AND (state='importing' OR (state='active' AND node_artifact_id=? AND size_bytes=? AND sha256=?))`, nodeArtifactID, sizeBytes, digest, locationID, nodeArtifactID, sizeBytes, digest)
	return oneRow(result, err)
}

func (s *Store) SetPrimaryArtifactLocation(ctx context.Context, artifactID, locationID string) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	if _, err := conn.ExecContext(ctx, `UPDATE artifact_locations SET is_primary=0 WHERE artifact_id=?`, artifactID); err != nil {
		return err
	}
	result, err := conn.ExecContext(ctx, `UPDATE artifact_locations SET is_primary=1 WHERE id=? AND artifact_id=? AND state='active'`, locationID, artifactID)
	if err := oneRow(result, err); err != nil {
		if errors.Is(err, ErrArtifactNotFound) {
			return ErrArtifactNotFound
		}
		return err
	}
	return nil
}

func (s *Store) ActivatePrimaryArtifactLocation(ctx context.Context, artifactID, locationID string, sizeBytes int64, digest string) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	var artifactSize int64
	var artifactDigest string
	if err := conn.QueryRowContext(ctx, `SELECT size_bytes,COALESCE(sha256,'') FROM task_artifacts WHERE id=? AND state='active'`, artifactID).Scan(&artifactSize, &artifactDigest); errors.Is(err, sql.ErrNoRows) {
		return ErrArtifactNotFound
	} else if err != nil {
		return err
	}
	if artifactSize != sizeBytes || artifactDigest != digest {
		return fmt.Errorf("产物迁移完整性不匹配")
	}
	if _, err := conn.ExecContext(ctx, `UPDATE artifact_locations SET is_primary=0 WHERE artifact_id=?`, artifactID); err != nil {
		return err
	}
	result, err := conn.ExecContext(ctx, `UPDATE artifact_locations SET state='active',is_primary=1,size_bytes=?,sha256=?,verified_at=? WHERE id=? AND artifact_id=? AND state IN ('importing','active')`, sizeBytes, digest, s.nowMillis(), locationID, artifactID)
	if err := oneRow(result, err); err != nil {
		return err
	}
	return nil
}

type ArtifactAccess struct {
	Artifact      TaskArtifact
	Location      ArtifactLocation
	APIKeyID      string
	TaskStatus    domain.InternalStatus
	TaskDeletedAt int64
}

func (s *Store) GetArtifactAccess(ctx context.Context, artifactID string) (access ArtifactAccess, err error) {
	var primary int
	var taskDeleted sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT
		a.id,a.task_id,COALESCE(a.stage_id,''),a.kind,a.size_bytes,COALESCE(a.sha256,''),COALESCE(a.media_json,''),a.state,a.created_at,a.expires_at,COALESCE(a.deleted_at,0),
		l.id,l.artifact_id,l.node_id,l.node_artifact_id,COALESCE(l.storage_key_fingerprint,''),l.state,l.is_primary,l.size_bytes,l.sha256,l.created_at,COALESCE(l.verified_at,0),COALESCE(l.deleted_at,0),
		t.api_key_id,t.status,t.deleted_at
		FROM task_artifacts a JOIN video_tasks t ON t.task_id=a.task_id
		JOIN artifact_locations l ON l.artifact_id=a.id AND l.state='active' AND l.is_primary=1
		WHERE a.id=? AND t.result_artifact_id=a.id`, artifactID).Scan(
		&access.Artifact.ID, &access.Artifact.TaskID, &access.Artifact.StageID, &access.Artifact.Kind, &access.Artifact.SizeBytes,
		&access.Artifact.SHA256, &access.Artifact.MediaJSON, &access.Artifact.State, &access.Artifact.CreatedAt, &access.Artifact.ExpiresAt, &access.Artifact.DeletedAt,
		&access.Location.ID, &access.Location.ArtifactID, &access.Location.NodeID, &access.Location.NodeArtifactID, &access.Location.StorageKeyFingerprint,
		&access.Location.State, &primary, &access.Location.SizeBytes, &access.Location.SHA256, &access.Location.CreatedAt, &access.Location.VerifiedAt, &access.Location.DeletedAt,
		&access.APIKeyID, &access.TaskStatus, &taskDeleted,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactAccess{}, ErrArtifactNotFound
	}
	access.Location.IsPrimary = primary == 1
	if taskDeleted.Valid {
		access.TaskDeletedAt = taskDeleted.Int64
	}
	return access, err
}
