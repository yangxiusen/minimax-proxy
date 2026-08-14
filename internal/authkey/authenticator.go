package authkey

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync/atomic"

	"minimax-h3-tc/internal/domain"
)

type CredentialStore interface {
	ListExternalAPIKeyCredentials(context.Context) ([]domain.ExternalAPIKeyCredential, error)
}

type snapshot struct{ owners map[[32]byte]string }

type Authenticator struct {
	store   CredentialStore
	current atomic.Pointer[snapshot]
}

func NewAuthenticator(store CredentialStore) *Authenticator {
	auth := &Authenticator{store: store}
	auth.current.Store(&snapshot{owners: map[[32]byte]string{}})
	return auth
}

func Generate(random io.Reader) (string, error) {
	bytes := make([]byte, 32)
	if _, err := io.ReadFull(random, bytes); err != nil {
		return "", fmt.Errorf("生成 API Key: %w", err)
	}
	return "mmx_" + hex.EncodeToString(bytes), nil
}

func Digest(token string) [32]byte { return sha256.Sum256([]byte(token)) }

func (a *Authenticator) Authenticate(token string) (string, bool) {
	if a == nil || token == "" {
		return "", false
	}
	current := a.current.Load()
	if current == nil {
		return "", false
	}
	owner, ok := current.owners[Digest(token)]
	return owner, ok
}

func (a *Authenticator) Reload(ctx context.Context) error {
	items, err := a.store.ListExternalAPIKeyCredentials(ctx)
	if err != nil {
		return fmt.Errorf("加载 API Key 鉴权快照: %w", err)
	}
	next := &snapshot{owners: make(map[[32]byte]string, len(items))}
	for _, item := range items {
		if item.Enabled {
			next.owners[item.KeyDigest] = item.ID
		}
	}
	a.current.Store(next)
	return nil
}
