package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrArtifactNotFound = errors.New("产物不存在")

type TaskArtifact struct {
	ID        string
	TaskID    string
	StageID   string
	Kind      string
	SizeBytes int64
	SHA256    string
	MediaJSON string
	State     string
	CreatedAt int64
	ExpiresAt int64
	DeletedAt int64
}

func (s *Store) CreateArtifact(ctx context.Context, artifact TaskArtifact) error {
	if artifact.State == "" {
		artifact.State = "active"
	}
	if artifact.CreatedAt == 0 {
		artifact.CreatedAt = s.nowMillis()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO task_artifacts(id,task_id,stage_id,kind,size_bytes,sha256,media_json,state,created_at,expires_at) VALUES(?,?,NULLIF(?,''),?,?,NULLIF(?,''),NULLIF(?,''),?,?,?)`, artifact.ID, artifact.TaskID, artifact.StageID, artifact.Kind, artifact.SizeBytes, artifact.SHA256, artifact.MediaJSON, artifact.State, artifact.CreatedAt, artifact.ExpiresAt)
	return err
}

func (s *Store) GetArtifact(ctx context.Context, artifactID string) (artifact TaskArtifact, err error) {
	var deletedAt sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT id,task_id,COALESCE(stage_id,''),kind,size_bytes,COALESCE(sha256,''),COALESCE(media_json,''),state,created_at,expires_at,deleted_at FROM task_artifacts WHERE id=?`, artifactID).Scan(&artifact.ID, &artifact.TaskID, &artifact.StageID, &artifact.Kind, &artifact.SizeBytes, &artifact.SHA256, &artifact.MediaJSON, &artifact.State, &artifact.CreatedAt, &artifact.ExpiresAt, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskArtifact{}, ErrArtifactNotFound
	}
	if deletedAt.Valid {
		artifact.DeletedAt = deletedAt.Int64
	}
	return artifact, err
}

func (s *Store) RegisterStageOutput(
	ctx context.Context,
	taskID, stageID, nodeID, nodeArtifactID string,
	sizeBytes int64,
	digest, mediaJSON string,
) (artifactID string, err error) {
	if taskID == "" || stageID == "" || nodeID == "" || nodeArtifactID == "" || sizeBytes <= 0 || len(digest) != 64 || !json.Valid([]byte(mediaJSON)) {
		return "", errors.New("节点阶段产物元数据无效")
	}
	if decoded, decodeErr := hex.DecodeString(digest); decodeErr != nil || len(decoded) != sha256.Size {
		return "", errors.New("节点阶段产物 SHA-256 无效")
	}
	artifactID = stableArtifactID("art", stageID)
	locationID := stableArtifactID("loc", nodeID+"\x00"+nodeArtifactID)
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return "", err
	}
	defer completeTransaction(finish, &err)
	var stageTaskID string
	var stageOrder int
	if err := conn.QueryRowContext(ctx, `SELECT task_id,stage_order FROM task_stages WHERE id=?`, stageID).Scan(&stageTaskID, &stageOrder); errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("阶段不存在")
	} else if err != nil {
		return "", err
	}
	if stageTaskID != taskID {
		return "", errors.New("阶段不属于任务")
	}
	var expiresAt int64
	if err := conn.QueryRowContext(ctx, `SELECT expires_at*1000 FROM video_tasks WHERE task_id=? AND deleted_at IS NULL`, taskID).Scan(&expiresAt); err != nil {
		return "", err
	}
	var laterStages int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_stages WHERE task_id=? AND stage_order>?`, taskID, stageOrder).Scan(&laterStages); err != nil {
		return "", err
	}
	kind := "intermediate_video"
	if laterStages == 0 {
		kind = "final_video"
	}
	now := s.nowMillis()
	if _, err := conn.ExecContext(ctx, `INSERT INTO task_artifacts(id,task_id,stage_id,kind,size_bytes,sha256,media_json,state,created_at,expires_at) VALUES(?,?,?,?,?,?,?,'active',?,?) ON CONFLICT(id) DO NOTHING`, artifactID, taskID, stageID, kind, sizeBytes, digest, mediaJSON, now, expiresAt); err != nil {
		return "", err
	}
	var existingTaskID, existingStageID, existingKind, existingDigest, existingMedia string
	var existingSize int64
	if err := conn.QueryRowContext(ctx, `SELECT task_id,COALESCE(stage_id,''),kind,size_bytes,COALESCE(sha256,''),COALESCE(media_json,'') FROM task_artifacts WHERE id=?`, artifactID).Scan(&existingTaskID, &existingStageID, &existingKind, &existingSize, &existingDigest, &existingMedia); err != nil {
		return "", err
	}
	if existingTaskID != taskID || existingStageID != stageID || existingKind != kind || existingSize != sizeBytes || existingDigest != digest || existingMedia != mediaJSON {
		return "", errors.New("阶段产物幂等冲突")
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO artifact_locations(id,artifact_id,node_id,node_artifact_id,state,is_primary,size_bytes,sha256,created_at,verified_at) VALUES(?,?,?,?, 'active',1,?,?,?,?) ON CONFLICT(id) DO NOTHING`, locationID, artifactID, nodeID, nodeArtifactID, sizeBytes, digest, now, now); err != nil {
		return "", err
	}
	var existingArtifactID, existingNodeID, existingNodeArtifactID, existingLocationState, existingLocationDigest string
	var existingPrimary int
	var existingLocationSize int64
	if err := conn.QueryRowContext(ctx, `SELECT artifact_id,node_id,node_artifact_id,state,is_primary,size_bytes,sha256 FROM artifact_locations WHERE id=?`, locationID).Scan(&existingArtifactID, &existingNodeID, &existingNodeArtifactID, &existingLocationState, &existingPrimary, &existingLocationSize, &existingLocationDigest); err != nil {
		return "", err
	}
	if existingArtifactID != artifactID || existingNodeID != nodeID || existingNodeArtifactID != nodeArtifactID || existingLocationState != "active" || existingPrimary != 1 || existingLocationSize != sizeBytes || existingLocationDigest != digest {
		return "", fmt.Errorf("阶段产物位置幂等冲突")
	}
	return artifactID, nil
}

func stableArtifactID(prefix, value string) string {
	digest := sha256.Sum256([]byte(value))
	return prefix + "_" + hex.EncodeToString(digest[:16])
}

func (s *Store) RegisterInputArtifact(ctx context.Context, artifactID, taskID, kind, nodeID, nodeArtifactID string, sizeBytes int64, digest, mediaJSON string) (err error) {
	if artifactID == "" || taskID == "" || nodeID == "" || nodeArtifactID == "" || sizeBytes <= 0 || len(digest) != 64 {
		return errors.New("输入素材元数据无效")
	}
	if kind != "audio_source" && kind != "intermediate_video" && kind != "test_output" {
		return errors.New("输入素材类型无效")
	}
	if mediaJSON != "" && !json.Valid([]byte(mediaJSON)) {
		return errors.New("输入素材媒体清单无效")
	}
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	var expires int64
	if err := conn.QueryRowContext(ctx, `SELECT expires_at*1000 FROM video_tasks WHERE task_id=? AND deleted_at IS NULL`, taskID).Scan(&expires); err != nil {
		return err
	}
	now := s.nowMillis()
	if _, err := conn.ExecContext(ctx, `INSERT INTO task_artifacts(id,task_id,kind,size_bytes,sha256,media_json,state,created_at,expires_at) VALUES(?,?,?,?,?,NULLIF(?,''),'active',?,?) ON CONFLICT(id) DO NOTHING`, artifactID, taskID, kind, sizeBytes, digest, mediaJSON, now, expires); err != nil {
		return err
	}
	locationID := stableArtifactID("loc", artifactID+"\x00"+nodeID)
	if _, err := conn.ExecContext(ctx, `UPDATE artifact_locations SET is_primary=0 WHERE artifact_id=? AND is_primary=1`, artifactID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO artifact_locations(id,artifact_id,node_id,node_artifact_id,state,is_primary,size_bytes,sha256,created_at,verified_at) VALUES(?,?,?,?,'active',1,?,?,?,?) ON CONFLICT(id) DO UPDATE SET node_artifact_id=excluded.node_artifact_id,state='active',is_primary=1,size_bytes=excluded.size_bytes,sha256=excluded.sha256,verified_at=excluded.verified_at`, locationID, artifactID, nodeID, nodeArtifactID, sizeBytes, digest, now, now); err != nil {
		return err
	}
	return nil
}
