package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"minimax-h3-tc/internal/domain"
)

func (s *Store) FindIdempotentTask(ctx context.Context, apiKeyID, keyHash, requestHash string) (domain.Task, error) {
	if keyHash == "" {
		return domain.Task{}, domain.ErrTaskNotFound
	}
	now := s.nowUnix()
	var taskID, storedHash string
	err := s.db.QueryRowContext(ctx, `SELECT i.task_id,i.request_hash FROM idempotency_keys i JOIN video_tasks t ON t.task_id=i.task_id WHERE i.api_key_id=? AND i.key_hash=? AND i.expires_at>? AND t.deleted_at IS NULL`, apiKeyID, keyHash, now).Scan(&taskID, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, domain.ErrTaskNotFound
	}
	if err != nil {
		return domain.Task{}, err
	}
	if storedHash != requestHash {
		return domain.Task{}, domain.ErrIdempotencyConflict
	}
	return getWith(ctx, s.db, apiKeyID, taskID, now)
}

func (s *Store) ListInputSpoolFiles(ctx context.Context, taskID string) ([]domain.InputSpoolFile, error) {
	rows, err := s.db.QueryContext(ctx, inputSpoolFileSelect+` WHERE task_id=? ORDER BY content_index,id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := make([]domain.InputSpoolFile, 0)
	for rows.Next() {
		file, err := scanInputSpoolFile(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (s *Store) GetInputSpoolFile(ctx context.Context, taskID, inputID string) (domain.InputSpoolFile, error) {
	file, err := scanInputSpoolFile(s.db.QueryRowContext(ctx, inputSpoolFileSelect+` WHERE task_id=? AND id=?`, taskID, inputID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.InputSpoolFile{}, domain.ErrTaskNotFound
	}
	return file, err
}

func (s *Store) ListInputSpoolTaskIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT task_id FROM video_tasks WHERE deleted_at IS NULL UNION SELECT task_id FROM task_input_spool_files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]bool)
	for rows.Next() {
		var taskID string
		if err := rows.Scan(&taskID); err != nil {
			return nil, err
		}
		result[taskID] = true
	}
	return result, rows.Err()
}

func validateInputSpoolFile(file domain.InputSpoolFile) error {
	if file.ID == "" || file.TaskID == "" || file.ContentIndex < 0 || file.Role == "" ||
		file.MediaType == "" || file.Extension == "" || file.RelativePath == "" ||
		file.SizeBytes <= 0 || len(file.SHA256) != 64 {
		return errors.New("输入临时文件元数据无效")
	}
	if _, err := hex.DecodeString(file.SHA256); err != nil {
		return errors.New("输入临时文件 SHA-256 无效")
	}
	if file.ContentType != "image_url" && file.ContentType != "video_url" && file.ContentType != "audio_url" {
		return errors.New("输入临时文件内容类型无效")
	}
	if file.SourceKind == "" {
		file.SourceKind = "data_uri"
	}
	if file.SourceKind != "data_uri" {
		return errors.New("输入临时文件来源类型无效")
	}
	return nil
}

const inputSpoolFileSelect = `SELECT id,task_id,content_index,content_type,role,source_kind,COALESCE(declared_mime,''),COALESCE(detected_mime,''),media_type,extension,relative_path,size_bytes,sha256,created_at,updated_at FROM task_input_spool_files`

func scanInputSpoolFile(scanner rowScanner) (domain.InputSpoolFile, error) {
	var file domain.InputSpoolFile
	var createdAt, updatedAt int64
	err := scanner.Scan(
		&file.ID, &file.TaskID, &file.ContentIndex, &file.ContentType, &file.Role, &file.SourceKind,
		&file.DeclaredMIME, &file.DetectedMIME, &file.MediaType, &file.Extension, &file.RelativePath,
		&file.SizeBytes, &file.SHA256, &createdAt, &updatedAt,
	)
	if err != nil {
		return domain.InputSpoolFile{}, err
	}
	file.CreatedAt = time.UnixMilli(createdAt).UTC()
	file.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	return file, nil
}

func nullEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
