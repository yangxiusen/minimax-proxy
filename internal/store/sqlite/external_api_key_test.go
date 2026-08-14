package sqlite

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

func TestExternalAPIKeyCRUDAndCredentialSnapshot(t *testing.T) {
	store := openAPIKeyTestStore(t)
	ctx := context.Background()
	digest := sha256.Sum256([]byte("mmx_test-key"))
	created, err := store.CreateExternalAPIKey(ctx, domain.ExternalAPIKeyInput{
		ID: "key_01", Name: " Production ", Key: "mmx_test-key", KeyDigest: digest, KeyPrefix: "mmx_test", KeySuffix: "-key", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "Production" || created.Key != "mmx_test-key" || created.Version != 1 || created.MaskedKey() != "mmx_test...-key" {
		t.Fatalf("created=%+v", created)
	}
	if _, err := store.CreateExternalAPIKey(ctx, domain.ExternalAPIKeyInput{ID: "key_02", Name: "production", KeyDigest: sha256.Sum256([]byte("other")), KeyPrefix: "mmx_othe", KeySuffix: "ther", Enabled: true}); !errors.Is(err, domain.ErrAPIKeyNameConflict) {
		t.Fatalf("name conflict err=%v", err)
	}
	if _, err := store.CreateExternalAPIKey(ctx, domain.ExternalAPIKeyInput{ID: "key_03", Name: "Ä", KeyDigest: sha256.Sum256([]byte("unicode-one")), KeyPrefix: "mmx_unic", KeySuffix: "-one", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateExternalAPIKey(ctx, domain.ExternalAPIKeyInput{ID: "key_04", Name: "ä", KeyDigest: sha256.Sum256([]byte("unicode-two")), KeyPrefix: "mmx_unic", KeySuffix: "-two", Enabled: true}); !errors.Is(err, domain.ErrAPIKeyNameConflict) {
		t.Fatalf("unicode name conflict err=%v", err)
	}
	updated, err := store.UpdateExternalAPIKey(ctx, created.ID, created.Version, domain.ExternalAPIKeyUpdate{Name: " Renamed ", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Renamed" || updated.Enabled || updated.Version != 2 {
		t.Fatalf("updated=%+v", updated)
	}
	if _, err := store.UpdateExternalAPIKey(ctx, created.ID, 1, domain.ExternalAPIKeyUpdate{Name: "stale", Enabled: true}); !errors.Is(err, domain.ErrAPIKeyVersionConflict) {
		t.Fatalf("version conflict err=%v", err)
	}
	credentials, err := store.ListExternalAPIKeyCredentials(ctx)
	if err != nil || len(credentials) != 2 || credentials[0].Enabled || credentials[0].KeyDigest != digest {
		t.Fatalf("credentials=%+v err=%v", credentials, err)
	}
	if err := store.DeleteExternalAPIKey(ctx, created.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetExternalAPIKey(ctx, created.ID); !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Fatalf("get deleted err=%v", err)
	}
}

func TestExternalAPIKeyDeleteProtectsTaskAndIdempotencyReferences(t *testing.T) {
	for _, reference := range []string{"video_tasks", "idempotency_keys"} {
		t.Run(reference, func(t *testing.T) {
			store := openAPIKeyTestStore(t)
			ctx := context.Background()
			digest := sha256.Sum256([]byte(reference))
			key, err := store.CreateExternalAPIKey(ctx, domain.ExternalAPIKeyInput{ID: "legacy-owner", Name: "owner", KeyDigest: digest, KeyPrefix: "mmx_abcd", KeySuffix: "1234", Enabled: false})
			if err != nil {
				t.Fatal(err)
			}
			if reference == "video_tasks" {
				_, err = store.db.ExecContext(ctx, `INSERT INTO video_tasks(task_id,api_key_id,scenario,request_json,request_hash,resolution,duration,ratio_requested,created_at,updated_at,expires_at) VALUES('task-ref','legacy-owner','t2va','{}','hash','2K',5,'16:9',1,1,100)`)
			} else {
				_, err = store.db.ExecContext(ctx, `INSERT INTO idempotency_keys(api_key_id,key_hash,request_hash,task_id,created_at,expires_at) VALUES('legacy-owner','kh','rh','missing-task',1,100)`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := store.DeleteExternalAPIKey(ctx, key.ID, key.Version); !errors.Is(err, domain.ErrAPIKeyInUse) {
				t.Fatalf("delete err=%v", err)
			}
		})
	}
}

func TestAPIKeyBootstrapImportsOnceAndDoesNotMixExistingRows(t *testing.T) {
	store := openAPIKeyTestStore(t)
	ctx := context.Background()
	digest := sha256.Sum256([]byte("legacy-secret"))
	count, imported, err := store.ImportLegacyAPIKeys(ctx, []domain.ExternalAPIKeyInput{{ID: "legacy", Name: "legacy", KeyDigest: digest, KeyPrefix: "lega", KeySuffix: "cret", Enabled: true}})
	if err != nil || count != 1 || !imported {
		t.Fatalf("count=%d imported=%v err=%v", count, imported, err)
	}
	pending, err := store.APIKeyBootstrapPending(ctx)
	if err != nil || pending {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	count, imported, err = store.ImportLegacyAPIKeys(ctx, []domain.ExternalAPIKeyInput{{ID: "bad", Name: "bad"}})
	if err != nil || count != 1 || imported {
		t.Fatalf("second count=%d imported=%v err=%v", count, imported, err)
	}
}

func TestAPIKeyBootstrapMeasuresLegacyNamesInCharacters(t *testing.T) {
	store := openAPIKeyTestStore(t)
	digest := sha256.Sum256([]byte("unicode-secret"))
	name := strings.Repeat("密", 128)
	count, imported, err := store.ImportLegacyAPIKeys(context.Background(), []domain.ExternalAPIKeyInput{{ID: "legacy-unicode", Name: name, KeyDigest: digest, KeyPrefix: "mmx_abcd", KeySuffix: "1234", Enabled: true}})
	if err != nil || count != 1 || !imported {
		t.Fatalf("count=%d imported=%v err=%v", count, imported, err)
	}
}

func TestAPIKeyBootstrapExistingRowsIgnoreInvalidLegacyInput(t *testing.T) {
	store := openAPIKeyTestStore(t)
	digest := sha256.Sum256([]byte("database-secret"))
	if _, err := store.CreateExternalAPIKey(context.Background(), domain.ExternalAPIKeyInput{ID: "database-key", Name: "database", KeyDigest: digest, KeyPrefix: "mmx_abcd", KeySuffix: "1234", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	count, imported, err := store.ImportLegacyAPIKeys(context.Background(), []domain.ExternalAPIKeyInput{{ID: "", Name: ""}})
	if err != nil || count != 0 || imported {
		t.Fatalf("count=%d imported=%v err=%v", count, imported, err)
	}
	pending, err := store.APIKeyBootstrapPending(context.Background())
	if err != nil || pending {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
}

func openAPIKeyTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "keys.db"), Options{Now: func() time.Time { return time.UnixMilli(1234000) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
