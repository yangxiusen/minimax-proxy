package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"minimax-h3-tc/internal/domain"
	modernsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type ModelRequestProfile = domain.ModelRequestProfile

const profileSelect = `SELECT id,resolution,resolution_key,config_json,config_hash,created_by,updated_by,created_at,updated_at,row_version FROM request_profiles`

func (s *Store) CreateProfile(ctx context.Context, input domain.ModelRequestProfile) (result domain.ModelRequestProfile, err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return result, err
	}
	defer completeTransaction(finish, &err)
	if input.UpdatedBy == "" {
		input.UpdatedBy = input.CreatedBy
	}
	if input.ResolutionKey == "" {
		_, input.ResolutionKey, err = domain.NormalizeResolutionName(input.Resolution)
		if err != nil {
			return result, err
		}
	}
	now := s.nowMillis()
	_, err = conn.ExecContext(ctx, `INSERT INTO request_profiles(id,resolution,resolution_key,config_json,config_hash,created_by,updated_by,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, input.ID, input.Resolution, input.ResolutionKey, input.ConfigJSON, input.ConfigHash, input.CreatedBy, input.UpdatedBy, now, now)
	if err != nil {
		var sqliteErr *modernsqlite.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE && strings.Contains(sqliteErr.Error(), "request_profiles.resolution_key") {
			return result, fmt.Errorf("%w: %v", domain.ErrProfileKeyConflict, err)
		}
		return result, err
	}
	return scanProfile(conn.QueryRowContext(ctx, profileSelect+` WHERE id=?`, input.ID))
}

func (s *Store) GetProfile(ctx context.Context, id string) (domain.ModelRequestProfile, error) {
	profile, err := scanProfile(s.db.QueryRowContext(ctx, profileSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return profile, domain.ErrProfileNotFound
	}
	return profile, err
}

func (s *Store) GetProfileByResolution(ctx context.Context, resolution string) (domain.ModelRequestProfile, error) {
	_, key, normalizeErr := domain.NormalizeResolutionName(resolution)
	if normalizeErr != nil {
		return domain.ModelRequestProfile{}, domain.ErrProfileNotFound
	}
	profile, err := scanProfile(s.db.QueryRowContext(ctx, profileSelect+` WHERE resolution_key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return profile, domain.ErrProfileNotFound
	}
	return profile, err
}

func (s *Store) ListProfiles(ctx context.Context) ([]domain.ModelRequestProfile, error) {
	rows, err := s.db.QueryContext(ctx, profileSelect+` ORDER BY CASE resolution_key WHEN '480p' THEN 1 WHEN '768p' THEN 2 WHEN '2k' THEN 3 ELSE 4 END,resolution_key,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var profiles []domain.ModelRequestProfile
	for rows.Next() {
		profile, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (s *Store) UpdateProfile(ctx context.Context, id string, expectedRowVersion int64, configJSON, configHash, administrator string) (result domain.ModelRequestProfile, err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return result, err
	}
	defer completeTransaction(finish, &err)
	var version int64
	if err := conn.QueryRowContext(ctx, `SELECT row_version FROM request_profiles WHERE id=?`, id).Scan(&version); errors.Is(err, sql.ErrNoRows) {
		return result, domain.ErrProfileNotFound
	} else if err != nil {
		return result, err
	}
	if version != expectedRowVersion {
		return result, domain.ErrProfileVersionConflict
	}
	updated, err := conn.ExecContext(ctx, `UPDATE request_profiles SET config_json=?,config_hash=?,updated_by=?,updated_at=?,row_version=row_version+1 WHERE id=? AND row_version=?`, configJSON, configHash, administrator, s.nowMillis(), id, expectedRowVersion)
	if err := oneRow(updated, err); err != nil {
		return result, domain.ErrProfileVersionConflict
	}
	return scanProfile(conn.QueryRowContext(ctx, profileSelect+` WHERE id=?`, id))
}

func (s *Store) DeleteProfile(ctx context.Context, id string, expectedRowVersion int64) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	var version int64
	if err := conn.QueryRowContext(ctx, `SELECT row_version FROM request_profiles WHERE id=?`, id).Scan(&version); errors.Is(err, sql.ErrNoRows) {
		return domain.ErrProfileNotFound
	} else if err != nil {
		return err
	}
	if version != expectedRowVersion {
		return domain.ErrProfileVersionConflict
	}
	deleted, err := conn.ExecContext(ctx, `DELETE FROM request_profiles WHERE id=? AND row_version=?`, id, expectedRowVersion)
	if err := oneRow(deleted, err); err != nil {
		return domain.ErrProfileVersionConflict
	}
	return nil
}

func scanProfile(scanner rowScanner) (profile domain.ModelRequestProfile, err error) {
	err = scanner.Scan(&profile.ID, &profile.Resolution, &profile.ResolutionKey, &profile.ConfigJSON, &profile.ConfigHash, &profile.CreatedBy, &profile.UpdatedBy, &profile.CreatedAt, &profile.UpdatedAt, &profile.RowVersion)
	return profile, err
}
