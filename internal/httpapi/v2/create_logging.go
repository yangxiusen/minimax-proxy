package v2

import (
	"bytes"
	"encoding/json"
	"errors"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/logsafe"
	"net/http"
	"strings"
)

type createLogWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
	size   int
}

func (w *createLogWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *createLogWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.size += n
	if remaining := (32 << 10) - w.body.Len(); remaining > 0 {
		w.body.Write(data[:min(n, remaining)])
	}
	return n, err
}
func (w *createLogWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (h *handler) logCreateResponse(r *http.Request, w *createLogWriter) {
	if w.status == 0 {
		return
	}
	var body struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(w.body.Bytes(), &body)
	h.logger.InfoContext(r.Context(), "Proxy 创建任务响应", "stage", "proxy_create", "event", "response", "request_id", requestID(r.Context()), "task_id", body.TaskID, "status_code", w.status, "response_bytes", w.size, "response", logsafe.DiagnosticJSON(w.body.Bytes(), requestSecrets(r)...))
}
func requestSecrets(r *http.Request) []string {
	return []string{strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), r.Header.Get("Idempotency-Key")}
}
func (h *handler) writeCreateResult(w http.ResponseWriter, r *http.Request, task domain.Task) {
	if strings.TrimSpace(task.TaskID) == "" {
		h.internalError(w, r, errors.New("任务存储返回空 task_id"))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"task_id": task.TaskID})
}
