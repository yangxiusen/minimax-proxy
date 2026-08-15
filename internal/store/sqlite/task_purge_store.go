package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"minimax-h3-tc/internal/domain"
)

type purgeQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) EnsureTaskPurgeReady(ctx context.Context, taskID string) error {
	var status domain.InternalStatus
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM video_tasks WHERE task_id=? AND deleted_at IS NULL`, taskID).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		return domain.ErrTaskNotFound
	} else if err != nil {
		return err
	}
	if !status.AdminCanDelete() {
		return domain.ErrTaskNotOperable
	}
	var barrierCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_dispatch_barriers WHERE task_id=?`, taskID).Scan(&barrierCount); err != nil {
		return err
	}
	if barrierCount != 0 {
		return domain.ErrCancelReconcilePending
	}
	var profileRefs int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM profile_test_runs WHERE artifact_id IN (SELECT id FROM task_artifacts WHERE task_id=?)`, taskID).Scan(&profileRefs); err != nil {
		return err
	}
	if profileRefs != 0 {
		return domain.ErrStateConflict
	}
	return nil
}

func (s *Store) ListTaskArtifactLocations(ctx context.Context, taskID string) ([]domain.TaskArtifactLocation, error) {
	return listTaskArtifactLocationsWithConn(ctx, s.db, taskID)
}

func listTaskArtifactLocationsWithConn(ctx context.Context, db purgeQueryer, taskID string) ([]domain.TaskArtifactLocation, error) {
	rows, err := db.QueryContext(ctx, `SELECT l.id,a.task_id,l.node_id,l.node_artifact_id,l.state
FROM artifact_locations l JOIN task_artifacts a ON a.id=l.artifact_id
WHERE a.task_id=? AND l.state NOT IN ('deleted','missing')`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	locations := make([]domain.TaskArtifactLocation, 0)
	for rows.Next() {
		var location domain.TaskArtifactLocation
		if err := rows.Scan(&location.ID, &location.TaskID, &location.NodeID, &location.NodeArtifactID, &location.State); err != nil {
			return nil, err
		}
		locations = append(locations, location)
	}
	return locations, rows.Err()
}

func sameTaskArtifactLocations(a, b []domain.TaskArtifactLocation) bool {
	if len(a) != len(b) {
		return false
	}
	byID := make(map[string]domain.TaskArtifactLocation, len(a))
	for _, location := range a {
		byID[location.ID] = location
	}
	for _, location := range b {
		got, ok := byID[location.ID]
		if !ok {
			return false
		}
		if got.TaskID != location.TaskID || got.NodeID != location.NodeID || got.NodeArtifactID != location.NodeArtifactID || got.State != location.State {
			return false
		}
	}
	return true
}
