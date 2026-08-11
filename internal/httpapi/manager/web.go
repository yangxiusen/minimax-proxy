package manager

import (
	"embed"
	"net/http"
)

//go:embed web/login.html web/manager.html web/styles.css web/login.js web/manager.js
var webAssets embed.FS

func (h *handler) registerWebRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /manager", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/manager/", http.StatusPermanentRedirect)
	})
	mux.HandleFunc("GET /manager/login", h.loginPage)
	mux.HandleFunc("GET /manager/", h.managerPage)
	mux.HandleFunc("GET /manager/assets/{name}", h.asset)
}

func (h *handler) loginPage(w http.ResponseWriter, r *http.Request) {
	if h.requestHasValidSession(r) {
		http.Redirect(w, r, "/manager/", http.StatusSeeOther)
		return
	}
	h.serveEmbedded(w, "web/login.html", "text/html; charset=utf-8")
}

func (h *handler) managerPage(w http.ResponseWriter, r *http.Request) {
	if !h.requestHasValidSession(r) {
		http.Redirect(w, r, "/manager/login", http.StatusSeeOther)
		return
	}
	h.serveEmbedded(w, "web/manager.html", "text/html; charset=utf-8")
}

func (h *handler) asset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	contentType := ""
	switch name {
	case "styles.css":
		contentType = "text/css; charset=utf-8"
	case "login.js", "manager.js":
		contentType = "text/javascript; charset=utf-8"
	default:
		http.NotFound(w, r)
		return
	}
	h.serveEmbedded(w, "web/"+name, contentType)
}

func (h *handler) requestHasValidSession(r *http.Request) bool {
	cookie, err := r.Cookie(SessionCookieName)
	return err == nil && h.validSession(cookie.Value, h.now())
}

func (h *handler) serveEmbedded(w http.ResponseWriter, name, contentType string) {
	data, err := webAssets.ReadFile(name)
	if err != nil {
		http.Error(w, "服务内部错误", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}
