package manager

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelRoutingAssetIsServed(t *testing.T) {
	r := httptest.NewRequest("GET", "/manager/assets/model-routing.js", nil)
	r.SetPathValue("name", "model-routing.js")
	w := httptest.NewRecorder()
	(&handler{}).asset(w, r)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("asset status=%d type=%s", w.Code, w.Header().Get("Content-Type"))
	}
	page, err := webAssets.ReadFile("web/manager.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"/manager/assets/model-routing.js", "model-routes-dialog", "node-models", "tk2sd-v1"} {
		if !strings.Contains(string(page), value) {
			t.Errorf("page missing %q", value)
		}
	}
}
