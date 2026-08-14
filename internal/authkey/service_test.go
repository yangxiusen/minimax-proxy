package authkey

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

func TestServiceCRUDReloadsAfterCommittedWrites(t *testing.T) {
	store := &serviceStoreStub{}
	reloader := &reloadStub{}
	service := NewService(store, reloader, ServiceOptions{Random: strings.NewReader(strings.Repeat("a", 96))})
	created, err := service.Create(context.Background(), " Production ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Key, "mmx_") || created.ExternalAPIKey.Name != "Production" || store.created.KeyDigest != Digest(created.Key) || reloader.calls != 1 {
		t.Fatalf("created=%+v store=%+v reloads=%d", created, store.created, reloader.calls)
	}
	if _, err := service.Update(context.Background(), created.ID, 1, domain.ExternalAPIKeyUpdate{Name: " renamed ", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if store.updated.Name != "renamed" || reloader.calls != 2 {
		t.Fatalf("updated=%+v reloads=%d", store.updated, reloader.calls)
	}
	if err := service.Delete(context.Background(), created.ID, 2); err != nil {
		t.Fatal(err)
	}
	if reloader.calls != 3 {
		t.Fatalf("reloads=%d", reloader.calls)
	}
}

func TestServiceReportsCommittedWriteWhenReloadFails(t *testing.T) {
	store := &serviceStoreStub{}
	reloader := &reloadStub{err: errors.New("temporary")}
	service := NewService(store, reloader, ServiceOptions{Random: strings.NewReader(strings.Repeat("b", 96))})
	created, err := service.Create(context.Background(), "name")
	if !errors.Is(err, ErrCacheRefresh) || created.Key == "" || store.created.ID == "" {
		t.Fatalf("created=%+v err=%v", created, err)
	}
}

func TestServiceRunRefreshesUntilContextCancellation(t *testing.T) {
	reloader := &reloadStub{notify: make(chan struct{}, 4)}
	service := NewService(&serviceStoreStub{}, reloader, ServiceOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { service.Run(ctx, time.Millisecond); close(done) }()
	for i := 0; i < 2; i++ {
		select {
		case <-reloader.notify:
		case <-time.After(time.Second):
			t.Fatal("refresh did not run")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("run did not stop")
	}
}

type serviceStoreStub struct {
	created domain.ExternalAPIKeyInput
	updated domain.ExternalAPIKeyUpdate
}

func (s *serviceStoreStub) ListExternalAPIKeys(context.Context) ([]domain.ExternalAPIKey, error) {
	return nil, nil
}
func (s *serviceStoreStub) CreateExternalAPIKey(_ context.Context, input domain.ExternalAPIKeyInput) (domain.ExternalAPIKey, error) {
	s.created = input
	return domain.ExternalAPIKey{ID: input.ID, Name: input.Name, KeyDigest: input.KeyDigest, KeyPrefix: input.KeyPrefix, KeySuffix: input.KeySuffix, Enabled: input.Enabled, Version: 1}, nil
}
func (s *serviceStoreStub) UpdateExternalAPIKey(_ context.Context, id string, version int64, input domain.ExternalAPIKeyUpdate) (domain.ExternalAPIKey, error) {
	s.updated = input
	return domain.ExternalAPIKey{ID: id, Name: input.Name, Enabled: input.Enabled, Version: version + 1}, nil
}
func (s *serviceStoreStub) DeleteExternalAPIKey(context.Context, string, int64) error { return nil }

type reloadStub struct {
	calls  int
	err    error
	notify chan struct{}
}

func (r *reloadStub) Reload(context.Context) error {
	r.calls++
	if r.notify != nil {
		select {
		case r.notify <- struct{}{}:
		default:
		}
	}
	return r.err
}
