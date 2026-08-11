package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
)

const nodeRequestBodyLimit = 32 << 10

type nodeRequest struct {
	ID             string `json:"id"`
	BaseURL        string `json:"base_url"`
	JobsBaseURL    string `json:"jobs_base_url"`
	PublicBaseURL  string `json:"public_base_url"`
	HealthPath     string `json:"health_path"`
	SubmitAPIName  string `json:"submit_api_name"`
	CheckAPIName   string `json:"check_api_name"`
	PollInterval   string `json:"poll_interval"`
	RequestTimeout string `json:"request_timeout"`
	Enabled        *bool  `json:"enabled"`
	Version        *int64 `json:"version"`
}

type nodeDTO struct {
	ID             string `json:"id"`
	BaseURL        string `json:"base_url"`
	JobsBaseURL    string `json:"jobs_base_url"`
	PublicBaseURL  string `json:"public_base_url"`
	HealthPath     string `json:"health_path"`
	SubmitAPIName  string `json:"submit_api_name"`
	CheckAPIName   string `json:"check_api_name"`
	PollInterval   string `json:"poll_interval"`
	RequestTimeout string `json:"request_timeout"`
	Enabled        bool   `json:"enabled"`
	Version        int64  `json:"version"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

type NodeCheck struct {
	OK        bool   `json:"ok"`
	ErrorCode string `json:"error_code,omitempty"`
}

type NodeProbeResult struct {
	Gradio NodeCheck `json:"gradio"`
	Jobs   NodeCheck `json:"jobs"`
}

func (h *handler) listNodes(w http.ResponseWriter, r *http.Request) {
	if h.nodes == nil {
		h.internalError(w, r, errors.New("manager node store is nil"))
		return
	}
	nodes, err := h.nodes.ListModelNodes(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	items := make([]nodeDTO, 0, len(nodes))
	for _, node := range nodes {
		items = append(items, makeNodeDTO(node))
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) createNode(w http.ResponseWriter, r *http.Request) {
	request, ok := h.readNodeRequest(w, r)
	if !ok {
		return
	}
	if request.Version != nil {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "创建节点不能包含 version")
		return
	}
	input, ok := h.normalizeNodeRequest(w, request, true)
	if !ok {
		return
	}
	if h.nodes == nil {
		h.internalError(w, r, errors.New("manager node store is nil"))
		return
	}
	node, err := h.nodes.CreateModelNode(r.Context(), input)
	if err != nil {
		h.writeNodeError(w, r, err)
		return
	}
	h.wakeRegistry()
	h.logger.InfoContext(r.Context(), "管理员已新增模型服务节点", "node_id", node.ID, "stage", "node_create")
	w.Header().Set("Location", "/manager/api/nodes/"+node.ID)
	h.writeJSON(w, http.StatusCreated, makeNodeDTO(node))
}

func (h *handler) updateNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("node_id")
	request, ok := h.readNodeRequest(w, r)
	if !ok {
		return
	}
	if request.ID != "" {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "更新节点不能包含 id")
		return
	}
	if request.Version == nil || *request.Version < 1 {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "version 必须大于等于 1")
		return
	}
	request.ID = id
	input, ok := h.normalizeNodeRequest(w, request, true)
	if !ok {
		return
	}
	if h.nodes == nil {
		h.internalError(w, r, errors.New("manager node store is nil"))
		return
	}
	node, err := h.nodes.UpdateModelNode(r.Context(), id, *request.Version, input)
	if err != nil {
		h.writeNodeError(w, r, err)
		return
	}
	h.wakeRegistry()
	h.logger.InfoContext(r.Context(), "管理员已更新模型服务节点", "node_id", node.ID, "stage", "node_update")
	h.writeJSON(w, http.StatusOK, makeNodeDTO(node))
}

func (h *handler) deleteNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("node_id")
	if !config.ValidModelNodeID(id) {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "node_id 无效")
		return
	}
	query := r.URL.Query()
	if len(query) != 1 || len(query["version"]) != 1 {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "version 查询参数无效")
		return
	}
	version, err := strconv.ParseInt(query.Get("version"), 10, 64)
	if err != nil || version < 1 {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "version 查询参数无效")
		return
	}
	if h.nodes == nil {
		h.internalError(w, r, errors.New("manager node store is nil"))
		return
	}
	if err := h.nodes.DeleteModelNode(r.Context(), id, version); err != nil {
		h.writeNodeError(w, r, err)
		return
	}
	h.wakeRegistry()
	h.logger.InfoContext(r.Context(), "管理员已删除模型服务节点", "node_id", id, "stage", "node_delete")
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) testNode(w http.ResponseWriter, r *http.Request) {
	request, ok := h.readNodeRequest(w, r)
	if !ok {
		return
	}
	if request.Version != nil {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "连接测试不能包含 version")
		return
	}
	input, ok := h.normalizeNodeRequest(w, request, true)
	if !ok {
		return
	}
	if h.probeNode == nil {
		h.internalError(w, r, errors.New("manager node prober is nil"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), input.RequestTimeout)
	defer cancel()
	checks := h.probeNode(ctx, input)
	if checks.Gradio.OK && checks.Jobs.OK {
		h.writeJSON(w, http.StatusOK, checks)
		return
	}
	h.logger.WarnContext(r.Context(), "模型服务节点连接测试未通过", "node_id", input.ID, "stage", "node_probe", "error_code", "node_probe_failed")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":  map[string]string{"type": "node_probe_failed", "message": "模型服务连接测试未全部通过"},
		"checks": checks,
	})
}

func (h *handler) readNodeRequest(w http.ResponseWriter, r *http.Request) (nodeRequest, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "Content-Type 必须为 application/json")
		return nodeRequest{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, nodeRequestBodyLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil || !validUniqueNodeObject(body) {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "请求 JSON 无效")
		return nodeRequest{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request nodeRequest
	if err := decoder.Decode(&request); err != nil {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "请求 JSON 无效")
		return nodeRequest{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "请求 JSON 无效")
		return nodeRequest{}, false
	}
	return request, true
}

func validUniqueNodeObject(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return false
		}
		if _, exists := seen[key]; exists {
			return false
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return false
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return false
	}
	var extra any
	return errors.Is(decoder.Decode(&extra), io.EOF)
}

func (h *handler) normalizeNodeRequest(w http.ResponseWriter, request nodeRequest, requireEnabled bool) (domain.ModelNodeInput, bool) {
	if requireEnabled && request.Enabled == nil {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "enabled 为必填字段")
		return domain.ModelNodeInput{}, false
	}
	pollInterval, err := time.ParseDuration(strings.TrimSpace(request.PollInterval))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "poll_interval 无效")
		return domain.ModelNodeInput{}, false
	}
	requestTimeout, err := time.ParseDuration(strings.TrimSpace(request.RequestTimeout))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "request_timeout 无效")
		return domain.ModelNodeInput{}, false
	}
	input := domain.ModelNodeInput{
		ID: strings.TrimSpace(request.ID), BaseURL: request.BaseURL, JobsBaseURL: request.JobsBaseURL, PublicBaseURL: request.PublicBaseURL,
		HealthPath: request.HealthPath, SubmitAPIName: request.SubmitAPIName, CheckAPIName: request.CheckAPIName,
		PollInterval: pollInterval, RequestTimeout: requestTimeout, Enabled: request.Enabled != nil && *request.Enabled,
	}
	normalized, _, err := config.NormalizeModelNode(input)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", err.Error())
		return domain.ModelNodeInput{}, false
	}
	return normalized, true
}

func (h *handler) writeNodeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrNodeNotFound):
		h.writeError(w, http.StatusNotFound, "node_not_found", "模型服务节点不存在")
	case errors.Is(err, domain.ErrNodeIDConflict):
		h.writeError(w, http.StatusConflict, "node_id_conflict", "模型服务节点 ID 已存在且不能复用")
	case errors.Is(err, domain.ErrNodeVersionConflict):
		h.writeError(w, http.StatusConflict, "node_version_conflict", "节点配置已被其他操作更新，请刷新后重试")
	case errors.Is(err, domain.ErrNodeHasActiveTask):
		h.writeError(w, http.StatusConflict, "node_has_active_task", "节点存在活动任务，仅允许停用且不能修改连接配置")
	case errors.Is(err, domain.ErrNodeMustBeDisabled):
		h.writeError(w, http.StatusConflict, "node_must_be_disabled", "节点必须先停用才能删除")
	default:
		h.internalError(w, r, err)
	}
}

func (h *handler) wakeRegistry() {
	if h.wake != nil {
		h.wake()
	}
}

func makeNodeDTO(node domain.ModelNode) nodeDTO {
	return nodeDTO{
		ID: node.ID, BaseURL: node.BaseURL, JobsBaseURL: node.JobsBaseURL, PublicBaseURL: node.PublicBaseURL,
		HealthPath: node.HealthPath, SubmitAPIName: node.SubmitAPIName, CheckAPIName: node.CheckAPIName,
		PollInterval: node.PollInterval.String(), RequestTimeout: node.RequestTimeout.String(), Enabled: node.Enabled,
		Version: node.Version, CreatedAt: unixTime(node.CreatedAt), UpdatedAt: unixTime(node.UpdatedAt),
	}
}
