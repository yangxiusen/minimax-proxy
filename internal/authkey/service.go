package authkey

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"minimax-h3-tc/internal/domain"
)

var ErrCacheRefresh = errors.New("API Key 鉴权缓存刷新失败")

type Store interface {
	ListExternalAPIKeys(context.Context) ([]domain.ExternalAPIKey, error)
	CreateExternalAPIKey(context.Context, domain.ExternalAPIKeyInput) (domain.ExternalAPIKey, error)
	UpdateExternalAPIKey(context.Context, string, int64, domain.ExternalAPIKeyUpdate) (domain.ExternalAPIKey, error)
	DeleteExternalAPIKey(context.Context, string, int64) error
}

type Reloader interface{ Reload(context.Context) error }

type ServiceOptions struct{ Random io.Reader }

type Service struct {
	store    Store
	reloader Reloader
	random   io.Reader
}

type CreatedExternalAPIKey struct {
	domain.ExternalAPIKey
	Key string
}

func NewService(store Store, reloader Reloader, options ServiceOptions) *Service {
	if options.Random == nil {
		options.Random = rand.Reader
	}
	return &Service{store: store, reloader: reloader, random: options.Random}
}

func (s *Service) List(ctx context.Context) ([]domain.ExternalAPIKey, error) {
	return s.store.ListExternalAPIKeys(ctx)
}

func (s *Service) Create(ctx context.Context, name string) (CreatedExternalAPIKey, error) {
	key, err := Generate(s.random)
	if err != nil {
		return CreatedExternalAPIKey{}, err
	}
	idBytes := make([]byte, 16)
	if _, err := io.ReadFull(s.random, idBytes); err != nil {
		return CreatedExternalAPIKey{}, fmt.Errorf("生成 API Key ID: %w", err)
	}
	input := domain.ExternalAPIKeyInput{ID: "key_" + hex.EncodeToString(idBytes), Name: strings.TrimSpace(name), Key: key, KeyDigest: Digest(key), KeyPrefix: key[:8], KeySuffix: key[len(key)-4:], Enabled: true}
	created, err := s.store.CreateExternalAPIKey(ctx, input)
	result := CreatedExternalAPIKey{ExternalAPIKey: created, Key: key}
	if err != nil {
		return CreatedExternalAPIKey{}, err
	}
	if err := s.reloader.Reload(ctx); err != nil {
		return result, errors.Join(ErrCacheRefresh, err)
	}
	return result, nil
}

func (s *Service) Update(ctx context.Context, id string, version int64, input domain.ExternalAPIKeyUpdate) (domain.ExternalAPIKey, error) {
	input.Name = strings.TrimSpace(input.Name)
	updated, err := s.store.UpdateExternalAPIKey(ctx, id, version, input)
	if err != nil {
		return domain.ExternalAPIKey{}, err
	}
	if err := s.reloader.Reload(ctx); err != nil {
		return updated, errors.Join(ErrCacheRefresh, err)
	}
	return updated, nil
}

func (s *Service) Delete(ctx context.Context, id string, version int64) error {
	if err := s.store.DeleteExternalAPIKey(ctx, id, version); err != nil {
		return err
	}
	if err := s.reloader.Reload(ctx); err != nil {
		return errors.Join(ErrCacheRefresh, err)
	}
	return nil
}

func (s *Service) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.reloader.Reload(ctx)
		}
	}
}
