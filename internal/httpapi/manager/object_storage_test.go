package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
)

type objectStorageStoreStub struct {
	config          domain.ObjectStorageConfig
	expectedVersion int64
	putCalls        int
}

func (stub *objectStorageStoreStub) GetObjectStorageConfig(context.Context) (domain.ObjectStorageConfig, error) {
	if stub.config.Version == 0 {
		return domain.ObjectStorageConfig{}, domain.ErrObjectStorageNotFound
	}
	return stub.config, nil
}

func (stub *objectStorageStoreStub) PutObjectStorageConfig(_ context.Context, expectedVersion int64, input domain.ObjectStorageConfig) (domain.ObjectStorageConfig, error) {
	stub.expectedVersion, stub.putCalls = expectedVersion, stub.putCalls+1
	input.Version = expectedVersion + 1
	if expectedVersion == 0 {
		input.Version = 1
	}
	input.LastTestStatus = "untested"
	stub.config = input
	return input, nil
}

func (stub *objectStorageStoreStub) MarkObjectStorageTest(_ context.Context, expectedVersion int64, passed bool) error {
	if stub.config.Version != expectedVersion {
		return domain.ErrObjectStorageVersionConflict
	}
	if passed {
		stub.config.LastTestStatus = "passed"
	} else {
		stub.config.LastTestStatus = "failed"
	}
	return nil
}

func TestObjectStorageConfigCRUDDoesNotExposeKeys(t *testing.T) {
	store := &objectStorageStoreStub{}
	h := testHandler(Dependencies{
		Admin:         config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
		ObjectStorage: store, NodeSecrets: testNodeSecrets{},
	})
	cookie := login(t, h, "admin", "secret", "192.0.2.11:1")
	body := `{"provider":"ucloud-us3","bucket_name":"video-bucket","file_host":"https://cn.example.com","public_base_url":"https://cdn.example.com","public_key":"public-secret","private_key":"private-secret","request_timeout":"30m"}`
	response := serve(h, http.MethodPut, "/manager/api/object-storage", body, "application/json", cookie, "192.0.2.11:1", false)
	if response.Code != http.StatusOK || store.putCalls != 1 || store.expectedVersion != 0 {
		t.Fatalf("status=%d calls=%d version=%d body=%s", response.Code, store.putCalls, store.expectedVersion, response.Body.String())
	}
	if string(store.config.PublicKeyCiphertext) != "encrypted:public-secret" || string(store.config.PrivateKeyCiphertext) != "encrypted:private-secret" {
		t.Fatalf("encrypted config=%+v", store.config)
	}
	if response.Body.String() == "" || containsAny(response.Body.String(), "public-secret", "private-secret", "encrypted:") {
		t.Fatalf("response leaked key: %s", response.Body.String())
	}

	get := serve(h, http.MethodGet, "/manager/api/object-storage", "", "", cookie, "192.0.2.11:1", false)
	if get.Code != http.StatusOK || containsAny(get.Body.String(), "public-secret", "private-secret", "encrypted:") {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}
}

func TestObjectStorageProbeUsesStoredConfigWithoutSaving(t *testing.T) {
	store := &objectStorageStoreStub{config: domain.ObjectStorageConfig{
		Provider: "ucloud-us3", BucketName: "video-bucket", FileHost: "https://cn.example.com", PublicBaseURL: "https://cdn.example.com",
		PublicKeyCiphertext: []byte("public-key"), PublicKeyNonce: []byte("nonce"), PublicKeyFingerprint: "sha256:public",
		PrivateKeyCiphertext: []byte("private-key"), PrivateKeyNonce: []byte("nonce"), PrivateKeyFingerprint: "sha256:private",
		RequestTimeout: 30 * time.Second, Version: 2,
	}}
	var probed ObjectStorageProbeInput
	h := testHandler(Dependencies{
		Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, ObjectStorage: store, NodeSecrets: testNodeSecrets{},
		ProbeObjectStorage: func(_ context.Context, input ObjectStorageProbeInput) ObjectStorageProbeResult {
			probed = input
			return ObjectStorageProbeResult{Passed: true, Checks: []NodeCheck{{Name: "public_read", Status: "passed"}}}
		},
	})
	cookie := login(t, h, "admin", "secret", "192.0.2.12:1")
	response := serve(h, http.MethodPost, "/manager/api/object-storage/test", `{"use_stored_config":true,"version":2}`, "application/json", cookie, "192.0.2.12:1", false)
	if response.Code != http.StatusOK || probed.PublicKey != "public-key" || probed.PrivateKey != "private-key" || store.putCalls != 0 {
		t.Fatalf("status=%d probed=%+v puts=%d body=%s", response.Code, probed, store.putCalls, response.Body.String())
	}
}

func TestObjectStorageConfigRejectsInsecureEndpoint(t *testing.T) {
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, ObjectStorage: &objectStorageStoreStub{}, NodeSecrets: testNodeSecrets{}})
	cookie := login(t, h, "admin", "secret", "192.0.2.13:1")
	body := map[string]any{"provider": "ucloud-us3", "bucket_name": "video-bucket", "file_host": "http://cn.example.com", "public_base_url": "https://cdn.example.com", "public_key": "public", "private_key": "private", "request_timeout": "30m"}
	data, _ := json.Marshal(body)
	response := serve(h, http.MethodPut, "/manager/api/object-storage", string(data), "application/json", cookie, "192.0.2.13:1", false)
	assertManagerError(t, response, http.StatusBadRequest, "bad_request_error")
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
