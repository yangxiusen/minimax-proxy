package tk2sd

import (
	"context"
	"io"
	"log/slog"
	"minimax-h3-tc/internal/logsafe"
	"net/http"
	"strings"
)

func (c *Client) WithLogger(logger *slog.Logger) *Client {
	copy := *c
	copy.logger = logger
	return &copy
}
func (c *Client) log() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return slog.Default()
}
func (c *Client) diagnosticOperation(r *http.Request) string {
	if r == nil || r.URL == nil || c.baseURL == nil {
		return ""
	}
	endpoint := strings.TrimPrefix(r.URL.Path, c.baseURL.Path)
	switch {
	case endpoint == tasksPath && r.Method == http.MethodPost:
		return "create"
	case strings.HasPrefix(endpoint, tasksPath+"/") && r.Method == http.MethodGet:
		return "query"
	case strings.HasPrefix(endpoint, tasksPath+"/") && r.Method == http.MethodDelete:
		return "cancel"
	case strings.HasPrefix(endpoint, "/v1/tasks/") && !strings.HasSuffix(endpoint, "/video"):
		return "metadata"
	case endpoint == "/v1/assets":
		return "upload"
	default:
		return ""
	}
}
func (c *Client) diagnosticFields(r *http.Request) []any {
	task, node := logsafe.TaskTrace(r.Context())
	return []any{"stage", "tk2sd_api", "operation", c.diagnosticOperation(r), "task_id", task, "node_id", node, "method", r.Method, "path", c.safeText(strings.TrimPrefix(r.URL.Path, c.baseURL.Path), 256)}
}
func (c *Client) logRequest(r *http.Request) {
	if c.diagnosticOperation(r) == "" {
		return
	}
	var summary any = map[string]any{}
	if r.GetBody != nil {
		body, err := r.GetBody()
		if err == nil {
			data, readErr := io.ReadAll(io.LimitReader(body, c.maxBody+1))
			_ = body.Close()
			if readErr == nil {
				summary = logsafe.DiagnosticJSON(data, c.apiKey, r.Header.Get("Idempotency-Key"))
			} else {
				summary = map[string]any{"omitted": "body_read_error"}
			}
		}
	} else if r.Body != nil {
		summary = map[string]any{"body_type": "multipart", "binary_omitted": true}
	}
	attrs := append(c.diagnosticFields(r), "event", "request", "request", summary)
	c.log().InfoContext(r.Context(), "tk2sd 请求参数", attrs...)
}
func (c *Client) logResponse(response *http.Response, data []byte, err error) {
	if response == nil || response.Request == nil || c.diagnosticOperation(response.Request) == "" {
		return
	}
	r := response.Request
	attrs := append(c.diagnosticFields(r), "event", "response", "status_code", response.StatusCode, "response_bytes", len(data), "response", logsafe.DiagnosticJSON(data, c.apiKey, r.Header.Get("Idempotency-Key")))
	if err != nil {
		attrs = append(attrs, "error_reason", logsafe.Error(err))
		c.log().WarnContext(r.Context(), "tk2sd 返回结果异常", attrs...)
	} else {
		c.log().InfoContext(r.Context(), "tk2sd 返回结果", attrs...)
	}
}
func (c *Client) logTransportError(r *http.Request, err error) {
	if c.diagnosticOperation(r) == "" {
		return
	}
	attrs := append(c.diagnosticFields(r), "event", "response", "status_code", 0, "error_reason", logsafe.Error(err))
	c.log().WarnContext(r.Context(), "tk2sd 请求未取得响应", attrs...)
}
func (c *Client) logTaskID(ctx context.Context, id string, valid bool) {
	task, node := logsafe.TaskTrace(ctx)
	if !valid {
		c.log().WarnContext(ctx, "tk2sd 创建返回缺少有效 id，保留提交待确认状态", "stage", "tk2sd_api", "operation", "create", "event", "invalid_response", "task_id", task, "node_id", node, "error_code", "missing_task_id", "expected_id_field", "id")
		return
	}
	c.log().InfoContext(ctx, "tk2sd 返回任务标识", "stage", "tk2sd_api", "operation", "create", "event", "task_id_mapping", "task_id", task, "node_id", node, "upstream_task_id", c.safeText(id, 128))
}
