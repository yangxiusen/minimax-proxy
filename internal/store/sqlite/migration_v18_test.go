package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

func TestObjectStorageBase64InputUploadFlagDefaultsOffAndPersists(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "migration-v18.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	input := domain.ObjectStorageConfig{Provider: "ucloud-us3", BucketName: "video-results", FileHost: "https://region.ufileos.com", PublicBaseURL: "https://video.example.com", PublicKeyCiphertext: []byte("public"), PublicKeyNonce: []byte("nonce"), PrivateKeyCiphertext: []byte("private"), PrivateKeyNonce: []byte("nonce"), RequestTimeout: 30 * time.Second}
	created, err := store.PutObjectStorageConfig(context.Background(), 0, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.UploadBase64Inputs {
		t.Fatal("new flag must default off")
	}
	created.UploadBase64Inputs = true
	updated, err := store.PutObjectStorageConfig(context.Background(), created.Version, created)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.UploadBase64Inputs {
		t.Fatal("new flag was not persisted")
	}
}
