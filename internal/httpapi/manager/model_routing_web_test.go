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

func TestManagerTaskTableShowsModelAndSearchesIt(t *testing.T) {
	page, err := webAssets.ReadFile("web/manager.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := webAssets.ReadFile("web/manager.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "<span>模型</span>") || !strings.Contains(string(page), "任务 ID / 客户 / 模型") {
		t.Fatal("manager task table does not expose model or model search")
	}
	if !strings.Contains(string(js), "item.model ||") {
		t.Fatal("task row does not display model")
	}
}

func TestManagerTK2SDDashboardHasSeparateQueueAndAccountView(t *testing.T) {
	js, err := webAssets.ReadFile("web/manager.js")
	if err != nil {
		t.Fatal(err)
	}
	styles, err := webAssets.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"tk2sd_dashboard", "tk2sd_proxy_queued", "renderTK2SDDashboard", "Proxy 待派发", "上游排队", "待核实", "失败", "取消", "账号占用", "账号状态", "登录状态"} {
		if !strings.Contains(string(js), value) {
			t.Errorf("dashboard UI missing %q", value)
		}
	}
	if !strings.Contains(string(styles), ".tk-dashboard") {
		t.Fatal("dashboard layout missing")
	}
}
