package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
)

const nodeRequestBodyLimit = 64 << 10

type nodeRequest struct {
	ID               string  `json:"id"`
	ServiceURL       string  `json:"service_url"`
	ProtocolVersion  string  `json:"protocol_version"`
	APIKey           *string `json:"api_key,omitempty"`
	UpstreamModel    string  `json:"upstream_model,omitempty"`
	MaxConcurrency   *int    `json:"max_concurrency,omitempty"`
	ReplaceResultURL *bool   `json:"replace_result_url,omitempty"`
	UseStoredAPIKey  bool    `json:"use_stored_api_key,omitempty"`
	PollInterval     string  `json:"poll_interval"`
	RequestTimeout   string  `json:"request_timeout"`
	Enabled          *bool   `json:"enabled"`
	Version          *int64  `json:"version"`
}

type nodeDTO struct {
	ID                string `json:"id"`
	ServiceURL        string `json:"service_url"`
	ProtocolVersion   string `json:"protocol_version"`
	APIKeyFingerprint string `json:"api_key_fingerprint,omitempty"`
	APIKeyConfigured  bool   `json:"api_key_configured"`
	UpstreamModel     string `json:"upstream_model,omitempty"`
	MaxConcurrency    int    `json:"max_concurrency"`
	ActiveTasks       int    `json:"active_tasks"`
	ReplaceResultURL  bool   `json:"replace_result_url"`
	PollInterval      string `json:"poll_interval"`
	RequestTimeout    string `json:"request_timeout"`
	Enabled           bool   `json:"enabled"`
	Version           int64  `json:"version"`
	CreatedAt         int64  `json:"created_at"`
	UpdatedAt         int64  `json:"updated_at"`
}

type NodeCheck struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
}

type NodeProbeInput struct {
	Node   domain.ModelNodeInput
	APIKey string
}

type NodeProbeResult struct {
	Reachable       bool           `json:"reachable"`
	Authenticated   bool           `json:"authenticated"`
	ProtocolVersion string         `json:"protocol_version,omitempty"`
	Checks          []NodeCheck    `json:"checks"`
	Capabilities    map[string]any `json:"capabilities,omitempty"`
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
		item := makeNodeDTO(node)
		if node.UsesOfficialV2() {
			if activity, ok := h.nodes.(OfficialNodeActivityStore); ok {
				item.ActiveTasks, err = activity.ActiveOfficialCount(r.Context(), node.ID)
				if err != nil {
					h.internalError(w, r, err)
					return
				}
			}
		}
		items = append(items, item)
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) createNode(w http.ResponseWriter, r *http.Request) {
	request, ok := h.readNodeRequest(w, r)
	if !ok {
		return
	}
	if request.Version != nil || request.UseStoredAPIKey {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "创建节点不能包含 version 或 use_stored_api_key")
		return
	}
	input, ok := h.normalizeNodeRequest(w, request, true)
	if !ok || !h.ensureObjectStorageReady(w, r, input) || !h.encryptNodeKey(w, r, request.APIKey, &input) {
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
	h.logger.InfoContext(r.Context(), "管理员已新增模型服务节点", "node_id", node.ID, "key_fingerprint", node.APIKeyFingerprint, "stage", "node_create")
	w.Header().Set("Location", "/manager/api/nodes/"+node.ID)
	h.writeJSON(w, http.StatusCreated, makeNodeDTO(node))
}

func (h *handler) updateNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("node_id")
	request, ok := h.readNodeRequest(w, r)
	if !ok {
		return
	}
	if request.ID != "" || request.UseStoredAPIKey {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "更新节点不能包含 id 或 use_stored_api_key")
		return
	}
	if request.Version == nil || *request.Version < 1 {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "version 必须大于等于 1")
		return
	}
	request.ID = id
	input, ok := h.normalizeNodeRequest(w, request, false)
	if !ok || !h.ensureObjectStorageReady(w, r, input) {
		return
	}
	if request.APIKey != nil && *request.APIKey != "" {
		if !h.encryptNodeKey(w, r, request.APIKey, &input) {
			return
		}
	} else {
		if h.nodes == nil {
			h.internalError(w, r, errors.New("manager node store is nil"))
			return
		}
		current, err := h.nodes.GetModelNode(r.Context(), id)
		if err != nil {
			h.writeNodeError(w, r, err)
			return
		}
		if len(current.APIKeyCiphertext) == 0 || len(current.APIKeyNonce) == 0 {
			h.writeError(w, http.StatusBadRequest, "bad_request_error", "升级为 H3 节点时必须提供 api_key")
			return
		}
		input.APIKeyCiphertext = append([]byte(nil), current.APIKeyCiphertext...)
		input.APIKeyNonce = append([]byte(nil), current.APIKeyNonce...)
		input.APIKeyFingerprint = current.APIKeyFingerprint
	}
	node, err := h.nodes.UpdateModelNode(r.Context(), id, *request.Version, input)
	if err != nil {
		h.writeNodeError(w, r, err)
		return
	}
	h.wakeRegistry()
	h.logger.InfoContext(r.Context(), "管理员已更新模型服务节点", "node_id", node.ID, "key_fingerprint", node.APIKeyFingerprint, "stage", "node_update")
	h.writeJSON(w, http.StatusOK, makeNodeDTO(node))
}

func (h *handler) ensureObjectStorageReady(w http.ResponseWriter, r *http.Request, input domain.ModelNodeInput) bool {
	if !input.UsesOfficialV2() || !input.ReplaceResultURL {
		return true
	}
	if h.objectStorage == nil {
		h.writeError(w, http.StatusConflict, "object_storage_not_ready", "请先配置并测试通过对象存储")
		return false
	}
	config, err := h.objectStorage.GetObjectStorageConfig(r.Context())
	if err != nil || config.LastTestStatus != "passed" {
		h.writeError(w, http.StatusConflict, "object_storage_not_ready", "请先配置并测试通过对象存储")
		return false
	}
	return true
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
	input, ok := h.normalizeNodeRequest(w, request, request.UseStoredAPIKey)
	if !ok {
		return
	}
	apiKey, ok := h.resolveProbeKey(w, r, request, &input)
	if !ok {
		return
	}
	if h.probeNode == nil {
		h.internalError(w, r, errors.New("manager node prober is nil"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), input.RequestTimeout)
	defer cancel()
	checks := h.probeNode(ctx, NodeProbeInput{Node: input, APIKey: apiKey})
	if checks.Reachable && checks.Authenticated && checks.ProtocolVersion == input.ProtocolVersion {
		h.writeJSON(w, http.StatusOK, checks)
		return
	}
	h.logger.WarnContext(r.Context(), "模型服务节点连接测试未通过", "node_id", input.ID, "stage", "node_probe", "error_code", "node_probe_failed")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":  map[string]string{"type": "node_probe_failed", "message": "模型服务连接测试未通过"},
		"checks": checks,
	})
}

func (h *handler) resolveProbeKey(w http.ResponseWriter, r *http.Request, request nodeRequest, input *domain.ModelNodeInput) (string, bool) {
	if !request.UseStoredAPIKey {
		if request.APIKey == nil || !validNodeAPIKey(input.ProtocolVersion, *request.APIKey) {
			h.writeError(w, http.StatusBadRequest, "bad_request_error", nodeAPIKeyValidationMessage(input.ProtocolVersion))
			return "", false
		}
		return *request.APIKey, true
	}
	if request.APIKey != nil || h.nodes == nil {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "使用已保存密钥时不能传 api_key")
		return "", false
	}
	current, err := h.nodes.GetModelNode(r.Context(), input.ID)
	if err != nil {
		h.writeNodeError(w, r, err)
		return "", false
	}
	if len(current.APIKeyCiphertext) == 0 || len(current.APIKeyNonce) == 0 {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "节点没有已保存的 API Key，请填写 api_key")
		return "", false
	}
	if h.nodeSecrets == nil {
		h.writeError(w, http.StatusServiceUnavailable, "master_key_missing", "节点密钥主密钥未配置")
		return "", false
	}
	apiKey, err := h.nodeSecrets.Open(current.APIKeyNonce, current.APIKeyCiphertext)
	if err != nil {
		h.internalError(w, r, errors.New("节点 API Key 解密失败"))
		return "", false
	}
	return apiKey, true
}

func (h *handler) encryptNodeKey(w http.ResponseWriter, r *http.Request, apiKey *string, input *domain.ModelNodeInput) bool {
	if apiKey == nil || !validNodeAPIKey(input.ProtocolVersion, *apiKey) {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", nodeAPIKeyValidationMessage(input.ProtocolVersion))
		return false
	}
	if h.nodeSecrets == nil {
		h.writeError(w, http.StatusServiceUnavailable, "master_key_missing", "节点密钥主密钥未配置")
		return false
	}
	nonce, ciphertext, fingerprint, err := h.nodeSecrets.Seal(*apiKey)
	if err != nil {
		h.internalError(w, r, errors.New("节点 API Key 加密失败"))
		return false
	}
	input.APIKeyNonce = nonce
	input.APIKeyCiphertext = ciphertext
	input.APIKeyFingerprint = fingerprint
	return true
}

var nodeAPIKeyPattern = regexp.MustCompile(`^[A-Za-z0-9]{32}$`)

func validNodeAPIKey(protocol, value string) bool {
	if protocol == "minimax-v2" {
		trimmed := strings.TrimSpace(value)
		if len(trimmed) < 1 || len(trimmed) > 512 {
			return false
		}
		for _, character := range trimmed {
			if unicode.IsControl(character) {
				return false
			}
		}
		return true
	}
	return nodeAPIKeyPattern.MatchString(value)
}

func nodeAPIKeyValidationMessage(protocol string) string {
	if protocol == "minimax-v2" {
		return "api_key 必须是 1 至 512 位且不含控制字符"
	}
	return "api_key 必须是 32 位字母或数字"
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

func (h *handler) normalizeNodeRequest(w http.ResponseWriter, request nodeRequest, allowMissingEnabled bool) (domain.ModelNodeInput, bool) {
	if !allowMissingEnabled && request.Enabled == nil {
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
	protocol := strings.TrimSpace(request.ProtocolVersion)
	if protocol == "minimax-v2" {
		if request.MaxConcurrency == nil || request.ReplaceResultURL == nil {
			h.writeError(w, http.StatusBadRequest, "bad_request_error", "minimax-v2 必须提供 max_concurrency 和 replace_result_url")
			return domain.ModelNodeInput{}, false
		}
	} else if request.UpstreamModel != "" || request.MaxConcurrency != nil || request.ReplaceResultURL != nil {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "官方协议专属字段不能用于当前协议")
		return domain.ModelNodeInput{}, false
	}
	input := domain.ModelNodeInput{
		ID: strings.TrimSpace(request.ID), ServiceURL: request.ServiceURL, ProtocolVersion: protocol,
		PollInterval: pollInterval, RequestTimeout: requestTimeout, Enabled: request.Enabled != nil && *request.Enabled,
	}
	if protocol == "minimax-v2" {
		input.UpstreamModel = request.UpstreamModel
		input.MaxConcurrency = *request.MaxConcurrency
		input.ReplaceResultURL = *request.ReplaceResultURL
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
	serviceURL := node.ServiceURL
	if serviceURL == "" {
		serviceURL = node.BaseURL
	}
	protocol := node.ProtocolVersion
	if protocol == "" {
		protocol = "legacy-gradio-v1"
	}
	return nodeDTO{
		ID: node.ID, ServiceURL: serviceURL, ProtocolVersion: protocol,
		APIKeyFingerprint: node.APIKeyFingerprint, APIKeyConfigured: len(node.APIKeyCiphertext) > 0,
		UpstreamModel: node.UpstreamModel, MaxConcurrency: effectiveNodeConcurrency(node), ReplaceResultURL: node.ReplaceResultURL,
		PollInterval: node.PollInterval.String(), RequestTimeout: node.RequestTimeout.String(), Enabled: node.Enabled,
		Version: node.Version, CreatedAt: unixTime(node.CreatedAt), UpdatedAt: unixTime(node.UpdatedAt),
	}
}

func effectiveNodeConcurrency(node domain.ModelNode) int {
	if node.UsesOfficialV2() && node.MaxConcurrency > 0 {
		return node.MaxConcurrency
	}
	return 1
}
