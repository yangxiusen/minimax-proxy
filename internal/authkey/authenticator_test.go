package authkey

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"minimax-h3-tc/internal/domain"
)

func TestGenerateAndDigest(t *testing.T) {
	key, err := Generate(strings.NewReader(strings.Repeat("x", 32)))
	if err != nil {
		t.Fatal(err)
	}
	if key != "mmx_"+strings.Repeat("78", 32) {
		t.Fatalf("key=%q", key)
	}
	if Digest(key) != Digest(key) || Digest(key) == Digest(key+"x") {
		t.Fatal("digest is not stable and distinct")
	}
	if _, err := Generate(errorReader{}); err == nil {
		t.Fatal("random source failure accepted")
	}
}

func TestAuthenticatorReloadAtomicallyReplacesEnabledCredentials(t *testing.T) {
	first, second := Digest("first"), Digest("second")
	store := &credentialStoreStub{items: []domain.ExternalAPIKeyCredential{{ID: "one", KeyDigest: first, Enabled: true}, {ID: "disabled", KeyDigest: second, Enabled: false}}}
	auth := NewAuthenticator(store)
	if _, ok := auth.Authenticate("first"); ok {
		t.Fatal("empty snapshot authenticated")
	}
	if err := auth.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if owner, ok := auth.Authenticate("first"); !ok || owner != "one" {
		t.Fatalf("owner=%q ok=%v", owner, ok)
	}
	if _, ok := auth.Authenticate("second"); ok {
		t.Fatal("disabled credential authenticated")
	}
	store.items = []domain.ExternalAPIKeyCredential{{ID: "two", KeyDigest: second, Enabled: true}}
	if err := auth.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := auth.Authenticate("first"); ok {
		t.Fatal("old snapshot remained")
	}
	if owner, ok := auth.Authenticate("second"); !ok || owner != "two" {
		t.Fatalf("owner=%q ok=%v", owner, ok)
	}
}

func TestAuthenticatorConcurrentAuthenticateAndReload(t *testing.T) {
	store := &credentialStoreStub{items: []domain.ExternalAPIKeyCredential{{ID: "owner", KeyDigest: Digest("token"), Enabled: true}}}
	auth := NewAuthenticator(store)
	if err := auth.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				auth.Authenticate("token")
			}
		}()
	}
	for i := 0; i < 100; i++ {
		if err := auth.Reload(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type credentialStoreStub struct {
	mu    sync.Mutex
	items []domain.ExternalAPIKeyCredential
	err   error
}

func (s *credentialStoreStub) ListExternalAPIKeyCredentials(context.Context) ([]domain.ExternalAPIKeyCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.ExternalAPIKeyCredential(nil), s.items...), s.err
}

var _ = errors.New
