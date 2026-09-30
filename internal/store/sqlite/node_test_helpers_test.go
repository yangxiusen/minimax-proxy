package sqlite

import (
	"context"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

func insertNodeAPINode(t *testing.T, store *Store, id string) {
	t.Helper()
	_, err := store.CreateModelNode(context.Background(), domain.ModelNodeInput{
		ID: id, ServiceURL: "https://" + id + ".example", ProtocolVersion: domain.ProtocolH3,
		APIKeyCiphertext: []byte{1}, APIKeyNonce: []byte{2}, APIKeyFingerprint: "fingerprint",
		PollInterval: 3 * time.Second, RequestTimeout: 30 * time.Second, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
}
