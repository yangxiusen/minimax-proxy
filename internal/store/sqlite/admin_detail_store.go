package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"minimax-h3-tc/internal/domain"
)

func (s *Store) GetAdminTaskDetail(ctx context.Context, taskID string) (domain.AdminTaskDetail, error) {
	task, err := scanTask(s.db.QueryRowContext(ctx, taskSelect+` WHERE task_id=? AND deleted_at IS NULL`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AdminTaskDetail{}, domain.ErrTaskNotFound
	}
	if err != nil {
		return domain.AdminTaskDetail{}, err
	}
	files, err := s.ListInputSpoolFiles(ctx, taskID)
	if err != nil {
		return domain.AdminTaskDetail{}, err
	}
	return domain.AdminTaskDetail{Task: task, InputSpoolFiles: files, LegacyBase64Present: strings.Contains(task.RequestJSON, ";base64,")}, nil
}
