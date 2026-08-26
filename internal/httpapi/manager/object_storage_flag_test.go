package manager

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
)

func TestObjectStorageConfigurationRoundTripsBase64InputUploadFlag(t *testing.T) {
	store := &objectStorageStoreStub{}
	h := testHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, ObjectStorage: store, NodeSecrets: testNodeSecrets{}})
	cookie := login(t, h, "admin", "secret", "192.0.2.11:1")
	body := `{"provider":"ucloud-us3","bucket_name":"video-results","file_host":"https://region.ufileos.com","public_base_url":"https://video.example.com","public_key":"public-key","private_key":"private-key","request_timeout":"30s","upload_base64_inputs":true}`
	response := serve(h, http.MethodPut, "/manager/api/object-storage", body, "application/json", cookie, "192.0.2.11:1", false)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !store.config.UploadBase64Inputs {
		t.Fatal("upload_base64_inputs was not persisted")
	}
	var result struct {
		UploadBase64Inputs bool `json:"upload_base64_inputs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.UploadBase64Inputs {
		t.Fatal("upload_base64_inputs was not returned")
	}
}

func TestObjectStorageWebFormIncludesBase64InputUploadFlag(t *testing.T) {
	page, err := webAssets.ReadFile("web/manager.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := webAssets.ReadFile("web/manager.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`name="upload_base64_inputs"`, `storageField("upload_base64_inputs").checked`, `upload_base64_inputs: storageField("upload_base64_inputs").checked`} {
		if !strings.Contains(string(page)+string(script), expected) {
			t.Errorf("storage UI missing %q", expected)
		}
	}
}
