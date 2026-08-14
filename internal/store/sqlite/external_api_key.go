package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"minimax-h3-tc/internal/domain"
)

const externalAPIKeySelect = `SELECT id,name,COALESCE(key_plaintext,''),key_digest,key_prefix,key_suffix,enabled,version,created_at,updated_at FROM external_api_keys`

func (s *Store) ListExternalAPIKeys(ctx context.Context) ([]domain.ExternalAPIKey, error) {
	rows, err := s.db.QueryContext(ctx, externalAPIKeySelect+` ORDER BY created_at DESC,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ExternalAPIKey
	for rows.Next() {
		item, err := scanExternalAPIKey(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) GetExternalAPIKey(ctx context.Context, id string) (domain.ExternalAPIKey, error) {
	item, err := scanExternalAPIKey(s.db.QueryRowContext(ctx, externalAPIKeySelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExternalAPIKey{}, domain.ErrAPIKeyNotFound
	}
	return item, err
}

func (s *Store) CreateExternalAPIKey(ctx context.Context, input domain.ExternalAPIKeyInput) (result domain.ExternalAPIKey, err error) {
	input.Name = strings.TrimSpace(input.Name)
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return result, err
	}
	defer completeTransaction(finish, &err)
	if err = ensureExternalAPIKeyUnique(ctx, conn, input.Name, input.KeyDigest[:], ""); err != nil {
		return result, err
	}
	now := s.nowMillis()
	_, err = conn.ExecContext(ctx, `INSERT INTO external_api_keys(id,name,key_plaintext,key_digest,key_prefix,key_suffix,enabled,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,1,?,?)`, input.ID, input.Name, input.Key, input.KeyDigest[:], input.KeyPrefix, input.KeySuffix, boolInt(input.Enabled), now, now)
	if err != nil {
		return result, classifyExternalAPIKeyConstraint(err)
	}
	return scanExternalAPIKey(conn.QueryRowContext(ctx, externalAPIKeySelect+` WHERE id=?`, input.ID))
}

func (s *Store) UpdateExternalAPIKey(ctx context.Context, id string, expectedVersion int64, input domain.ExternalAPIKeyUpdate) (result domain.ExternalAPIKey, err error) {
	input.Name = strings.TrimSpace(input.Name)
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return result, err
	}
	defer completeTransaction(finish, &err)
	var currentVersion int64
	if err = conn.QueryRowContext(ctx, `SELECT version FROM external_api_keys WHERE id=?`, id).Scan(&currentVersion); errors.Is(err, sql.ErrNoRows) {
		return result, domain.ErrAPIKeyNotFound
	}
	if err != nil {
		return result, err
	}
	if currentVersion != expectedVersion {
		return result, domain.ErrAPIKeyVersionConflict
	}
	if err = ensureExternalAPIKeyUnique(ctx, conn, input.Name, nil, id); err != nil {
		return result, err
	}
	resultSQL, err := conn.ExecContext(ctx, `UPDATE external_api_keys SET name=?,enabled=?,version=version+1,updated_at=? WHERE id=? AND version=?`, input.Name, boolInt(input.Enabled), s.nowMillis(), id, expectedVersion)
	if err != nil {
		return result, classifyExternalAPIKeyConstraint(err)
	}
	if rows, _ := resultSQL.RowsAffected(); rows != 1 {
		return result, domain.ErrAPIKeyVersionConflict
	}
	return scanExternalAPIKey(conn.QueryRowContext(ctx, externalAPIKeySelect+` WHERE id=?`, id))
}

func (s *Store) DeleteExternalAPIKey(ctx context.Context, id string, expectedVersion int64) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	var version int64
	if err = conn.QueryRowContext(ctx, `SELECT version FROM external_api_keys WHERE id=?`, id).Scan(&version); errors.Is(err, sql.ErrNoRows) {
		return domain.ErrAPIKeyNotFound
	}
	if err != nil {
		return err
	}
	if version != expectedVersion {
		return domain.ErrAPIKeyVersionConflict
	}
	var references int
	if err = conn.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM video_tasks WHERE api_key_id=?)+(SELECT COUNT(*) FROM idempotency_keys WHERE api_key_id=?)`, id, id).Scan(&references); err != nil {
		return err
	}
	if references > 0 {
		return domain.ErrAPIKeyInUse
	}
	result, err := conn.ExecContext(ctx, `DELETE FROM external_api_keys WHERE id=? AND version=?`, id, expectedVersion)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return domain.ErrAPIKeyVersionConflict
	}
	return nil
}

func (s *Store) ListExternalAPIKeyCredentials(ctx context.Context) ([]domain.ExternalAPIKeyCredential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,key_digest,enabled FROM external_api_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ExternalAPIKeyCredential
	for rows.Next() {
		var item domain.ExternalAPIKeyCredential
		var digest []byte
		var enabled int
		if err := rows.Scan(&item.ID, &digest, &enabled); err != nil {
			return nil, err
		}
		if len(digest) != len(item.KeyDigest) {
			return nil, fmt.Errorf("API Key 摘要长度无效: %d", len(digest))
		}
		copy(item.KeyDigest[:], digest)
		item.Enabled = enabled == 1
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) APIKeyBootstrapPending(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_key_config_bootstrap WHERE source='yaml_api_keys'`).Scan(&count)
	return count == 0, err
}

func (s *Store) BackfillExternalAPIKeyPlaintexts(ctx context.Context, inputs []domain.ExternalAPIKeyInput) error {
	for _, input := range inputs {
		if input.Key == "" {
			continue
		}
		result, err := s.db.ExecContext(ctx, `UPDATE external_api_keys SET key_plaintext=? WHERE id=? AND key_digest=? AND (key_plaintext IS NULL OR key_plaintext='')`, input.Key, input.ID, input.KeyDigest[:])
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows > 1 {
			return errors.New("API Key 明文回填影响了多条记录")
		}
	}
	return nil
}

func (s *Store) ImportLegacyAPIKeys(ctx context.Context, inputs []domain.ExternalAPIKeyInput) (countResult int, importedResult bool, err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return 0, false, err
	}
	defer completeTransaction(finish, &err)
	var importedCount int
	err = conn.QueryRowContext(ctx, `SELECT imported_count FROM api_key_config_bootstrap WHERE source='yaml_api_keys'`).Scan(&importedCount)
	if err == nil {
		return importedCount, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	var existing int
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM external_api_keys`).Scan(&existing); err != nil {
		return 0, false, err
	}
	if existing == 0 {
		if err := validateLegacyAPIKeys(inputs); err != nil {
			return 0, false, err
		}
		sorted := append([]domain.ExternalAPIKeyInput(nil), inputs...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
		now := s.nowMillis()
		for _, input := range sorted {
			input.Name = strings.TrimSpace(input.Name)
			if _, err = conn.ExecContext(ctx, `INSERT INTO external_api_keys(id,name,key_plaintext,key_digest,key_prefix,key_suffix,enabled,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,1,?,?)`, input.ID, input.Name, input.Key, input.KeyDigest[:], input.KeyPrefix, input.KeySuffix, boolInt(input.Enabled), now, now); err != nil {
				return 0, false, classifyExternalAPIKeyConstraint(err)
			}
		}
		importedCount = len(sorted)
	}
	if _, err = conn.ExecContext(ctx, `INSERT INTO api_key_config_bootstrap(source,imported_count,completed_at) VALUES('yaml_api_keys',?,?)`, importedCount, s.nowMillis()); err != nil {
		return 0, false, err
	}
	return importedCount, existing == 0, nil
}

func validateLegacyAPIKeys(inputs []domain.ExternalAPIKeyInput) error {
	names := make(map[string]struct{}, len(inputs))
	digests := make(map[[32]byte]struct{}, len(inputs))
	for _, input := range inputs {
		name := strings.TrimSpace(input.Name)
		if input.ID == "" || utf8.RuneCountInString(input.ID) > 128 || name == "" || utf8.RuneCountInString(name) > 128 {
			return fmt.Errorf("旧 API Key ID 或名称无效")
		}
		if len(input.KeyPrefix) < 4 || len(input.KeyPrefix) > 16 || len(input.KeySuffix) < 4 || len(input.KeySuffix) > 16 {
			return fmt.Errorf("旧 API Key 掩码无效")
		}
		for existing := range names {
			if strings.EqualFold(existing, name) {
				return domain.ErrAPIKeyNameConflict
			}
		}
		names[name] = struct{}{}
		if _, exists := digests[input.KeyDigest]; exists {
			return domain.ErrAPIKeyDigestConflict
		}
		digests[input.KeyDigest] = struct{}{}
	}
	return nil
}

func ensureExternalAPIKeyUnique(ctx context.Context, query rowQuerier, name string, digest []byte, excludedID string) error {
	rows, err := query.QueryContext(ctx, `SELECT name FROM external_api_keys WHERE id<>?`, excludedID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var existing string
		if err := rows.Scan(&existing); err != nil {
			return err
		}
		if strings.EqualFold(existing, name) {
			return domain.ErrAPIKeyNameConflict
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if digest != nil {
		var count int
		if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM external_api_keys WHERE key_digest=?`, digest).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return domain.ErrAPIKeyDigestConflict
		}
	}
	return nil
}

func classifyExternalAPIKeyConstraint(err error) error {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "uq_external_api_keys_name_ci") || strings.Contains(message, "external_api_keys.name") {
		return domain.ErrAPIKeyNameConflict
	}
	if strings.Contains(message, "uq_external_api_keys_digest") || strings.Contains(message, "external_api_keys.key_digest") {
		return domain.ErrAPIKeyDigestConflict
	}
	return err
}

func scanExternalAPIKey(scanner rowScanner) (domain.ExternalAPIKey, error) {
	var item domain.ExternalAPIKey
	var digest []byte
	var enabled int
	var created, updated int64
	err := scanner.Scan(&item.ID, &item.Name, &item.Key, &digest, &item.KeyPrefix, &item.KeySuffix, &enabled, &item.Version, &created, &updated)
	if err != nil {
		return item, err
	}
	if len(digest) != len(item.KeyDigest) {
		return item, fmt.Errorf("API Key 摘要长度无效: %d", len(digest))
	}
	copy(item.KeyDigest[:], digest)
	item.Enabled = enabled == 1
	item.CreatedAt, item.UpdatedAt = unixMillis(created), unixMillis(updated)
	return item, nil
}

func unixMillis(value int64) time.Time { return time.UnixMilli(value).UTC() }
