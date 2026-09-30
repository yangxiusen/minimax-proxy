package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/protocol"
	"minimax-h3-tc/internal/routing"
	"minimax-h3-tc/internal/upstream/tk2sd"
)

type ModelCatalogStore interface {
	GetModelCatalog(context.Context, string) (domain.ModelCatalog, error)
}
type catalogSummary struct {
	Source        string `json:"source"`
	Revision      int64  `json:"revision"`
	Status        string `json:"status"`
	ModelCount    int    `json:"model_count"`
	EnabledCount  int    `json:"enabled_count"`
	LastSuccessAt int64  `json:"last_success_at"`
	ValidUntil    int64  `json:"valid_until"`
	LastErrorCode string `json:"last_error_code"`
}
type modelPage struct {
	CatalogRevision int64              `json:"catalog_revision"`
	Source          string             `json:"source"`
	Status          string             `json:"status"`
	LastSuccessAt   int64              `json:"last_success_at"`
	ValidUntil      int64              `json:"valid_until"`
	LastErrorCode   string             `json:"last_error_code"`
	Total           int                `json:"total"`
	PageNum         int                `json:"page_num"`
	PageSize        int                `json:"page_size"`
	Items           []domain.NodeModel `json:"items"`
}

func (h *handler) listProtocols(w http.ResponseWriter, r *http.Request) {
	if !h.modelQuery(w, r) || !h.noModelBody(w, r) {
		return
	}
	h.writeJSON(w, 200, map[string]any{"items": protocol.Definitions()})
}

func (h *handler) discoverModels(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID              string  `json:"id"`
		ProtocolVersion string  `json:"protocol_version"`
		ServiceURL      string  `json:"service_url"`
		RequestTimeout  string  `json:"request_timeout"`
		APIKey          *string `json:"api_key"`
		UseStoredAPIKey bool    `json:"use_stored_api_key"`
	}
	if !h.modelQuery(w, r) || !h.readModelJSON(w, r, &request) {
		return
	}
	definition, ok := protocol.Lookup(request.ProtocolVersion)
	if !ok {
		h.writeError(w, 400, "unsupported_protocol", "不支持该协议")
		return
	}
	if definition.ModelSource != "discovered" {
		h.writeError(w, 400, "model_discovery_unsupported", "该协议不支持模型发现")
		return
	}
	timeout, err := time.ParseDuration(request.RequestTimeout)
	if err != nil {
		h.writeError(w, 400, "bad_request_error", "request_timeout 无效")
		return
	}
	id := request.ID
	if id == "" && !request.UseStoredAPIKey {
		id = "preview"
	}
	input, _, err := config.NormalizeModelNode(domain.ModelNodeInput{ID: id, ServiceURL: request.ServiceURL, ProtocolVersion: request.ProtocolVersion, RequestTimeout: timeout, PollInterval: time.Second, MaxConcurrency: 1})
	if err != nil {
		h.writeError(w, 400, "bad_request_error", "发现连接参数无效")
		return
	}
	key, ok := h.resolveProbeKey(w, r, nodeRequest{ID: request.ID, APIKey: request.APIKey, UseStoredAPIKey: request.UseStoredAPIKey}, &input)
	if !ok {
		return
	}
	if h.inventory == nil {
		h.writeError(w, 503, "model_discovery_unavailable", "模型发现尚未配置")
		return
	}
	catalog, err := h.inventory.Preview(r.Context(), input, key)
	if err != nil {
		h.writeModelError(w, r, err)
		return
	}
	type previewItem struct {
		ModelID      string                 `json:"model_id"`
		Capabilities domain.ModelCapability `json:"capabilities"`
		Verified     bool                   `json:"verified"`
	}
	items := make([]previewItem, 0, len(catalog.Items))
	for _, m := range catalog.Items {
		items = append(items, previewItem{m.ModelID, m.Capabilities, m.Verified})
	}
	h.writeJSON(w, 200, map[string]any{"source": catalog.Source, "items": items})
}

func (h *handler) listNodeModels(w http.ResponseWriter, r *http.Request) {
	if !h.modelQuery(w, r, "q", "page_num", "page_size", "include_missing") || !h.noModelBody(w, r) {
		return
	}
	q, p, size, ok := h.modelPagination(w, r)
	if !ok {
		return
	}
	missing := false
	if value, exists := r.URL.Query()["include_missing"]; exists {
		if value[0] != "true" && value[0] != "false" {
			h.writeError(w, 400, "bad_request_error", "include_missing 无效")
			return
		}
		missing = value[0] == "true"
	}
	catalog, ok := h.readNodeCatalog(w, r, r.PathValue("node_id"))
	if !ok {
		return
	}
	h.writeJSON(w, 200, catalogPage(catalog, q, p, size, missing))
}

func (h *handler) refreshNodeModels(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Version         int64 `json:"version"`
		CatalogRevision int64 `json:"catalog_revision"`
	}
	if !h.modelQuery(w, r) || !h.readModelJSON(w, r, &request) {
		return
	}
	if !config.ValidModelNodeID(r.PathValue("node_id")) || request.Version < 1 || request.CatalogRevision < 1 {
		h.writeError(w, 400, "bad_request_error", "节点或目录版本无效")
		return
	}
	if h.inventory == nil {
		h.writeError(w, 503, "model_discovery_unavailable", "模型发现尚未配置")
		return
	}
	catalog, err := h.inventory.Refresh(r.Context(), r.PathValue("node_id"), request.Version, request.CatalogRevision)
	if err != nil {
		h.writeModelError(w, r, err)
		return
	}
	h.wakeRegistry()
	h.writeJSON(w, 200, catalogPage(catalog, "", 1, 50, false))
}

func (h *handler) readNodeCatalog(w http.ResponseWriter, r *http.Request, id string) (domain.ModelCatalog, bool) {
	if !config.ValidModelNodeID(id) {
		h.writeError(w, 400, "bad_request_error", "node_id 无效")
		return domain.ModelCatalog{}, false
	}
	if h.nodes == nil || h.catalogs == nil {
		h.writeError(w, 503, "model_catalog_unavailable", "模型目录尚未配置")
		return domain.ModelCatalog{}, false
	}
	if _, err := h.nodes.GetModelNode(r.Context(), id); err != nil {
		h.writeNodeError(w, r, err)
		return domain.ModelCatalog{}, false
	}
	catalog, err := h.catalogs.GetModelCatalog(r.Context(), id)
	if err != nil {
		h.writeModelError(w, r, err)
		return domain.ModelCatalog{}, false
	}
	return catalog, true
}

func catalogPage(c domain.ModelCatalog, q string, page, size int, missing bool) modelPage {
	items := make([]domain.NodeModel, 0, len(c.Items))
	for _, m := range c.Items {
		if (missing || m.Present) && strings.Contains(m.ModelID, q) {
			items = append(items, m)
		}
	}
	sort.Slice(items, func(a, b int) bool { return items[a].ModelID < items[b].ModelID })
	total := len(items)
	start, end := pageBounds(total, page, size)
	return modelPage{c.Revision, c.Source, c.Status, c.LastSuccessAt, c.ValidUntil, c.LastErrorCode, total, page, size, items[start:end]}
}

func pageBounds(total, page, size int) (int, int) {
	if page > total/size+1 {
		return total, total
	}
	start := min((page-1)*size, total)
	return start, min(start+size, total)
}

func (h *handler) modelPagination(w http.ResponseWriter, r *http.Request) (string, int, int, bool) {
	q := r.URL.Query().Get("q")
	page, err := parsePositiveInt(r.URL.Query().Get("page_num"), 1)
	size, e := parsePositiveInt(r.URL.Query().Get("page_size"), 50)
	if err != nil || e != nil || size > 100 || !utf8.ValidString(q) || utf8.RuneCountInString(q) > 128 {
		h.writeError(w, 400, "bad_request_error", "分页或搜索参数无效")
		return "", 0, 0, false
	}
	return q, page, size, true
}

func (h *handler) modelQuery(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		h.writeError(w, 400, "bad_request_error", "查询参数无效")
		return false
	}
	for key, values := range query {
		if !slices.Contains(allowed, key) || len(values) != 1 {
			h.writeError(w, 400, "bad_request_error", "查询参数无效")
			return false
		}
	}
	return true
}
func (h *handler) noModelBody(w http.ResponseWriter, r *http.Request) bool {
	if r.Body != nil {
		var b [1]byte
		n, err := r.Body.Read(b[:])
		if n != 0 || (err != nil && err != io.EOF) {
			h.writeError(w, 400, "bad_request_error", "此接口不接受请求正文")
			return false
		}
	}
	return true
}
func (h *handler) readModelJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	if !h.modelMutation(w, r) {
		return false
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		h.writeError(w, 400, "bad_request_error", "Content-Type 必须为 application/json")
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, nodeRequestBodyLimit))
	if err != nil || !validUniqueNodeObject(body) || !validModelFields(body, out) {
		h.writeError(w, 400, "bad_request_error", "请求 JSON 无效")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		h.writeError(w, 400, "bad_request_error", "请求 JSON 无效")
		return false
	}
	return true
}

func (h *handler) modelMutation(w http.ResponseWriter, r *http.Request) bool {
	if !sameOriginMutation(r) {
		h.writeError(w, http.StatusForbidden, "csrf_error", "请求来源无效")
		return false
	}
	return true
}

func validModelFields(body []byte, out any) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return false
	}
	allowed := map[string]bool{}
	t := reflect.TypeOf(out).Elem()
	for j := 0; j < t.NumField(); j++ {
		name, _, _ := strings.Cut(t.Field(j).Tag.Get("json"), ",")
		allowed[name] = true
	}
	for name, value := range fields {
		if !allowed[name] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	return true
}

func (h *handler) writeModelError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, routing.ErrDiscoveryUnsupported):
		h.writeError(w, 400, "model_discovery_unsupported", "该协议不支持模型发现")
	case errors.Is(err, routing.ErrDiscoveryFailed):
		h.writeError(w, 502, "model_discovery_failed", "上游模型发现失败")
	case errors.Is(err, domain.ErrCatalogConflict):
		h.writeError(w, 409, "catalog_version_conflict", "模型目录已更新，请刷新后重试")
	case errors.Is(err, domain.ErrUnsupportedModel):
		h.writeError(w, 400, "unsupported_model", "模型未配置或未开放")
	case errors.Is(err, domain.ErrRouteUnavailable):
		h.writeError(w, 400, "models_required", "启用节点需要有效的开放模型清单")
	default:
		h.writeNodeError(w, r, err)
	}
}

func (h *handler) nodeWithCatalog(ctx context.Context, node domain.ModelNode) (nodeDTO, error) {
	dto := makeNodeDTO(node)
	var c domain.ModelCatalog
	if h.catalogs != nil {
		var err error
		c, err = h.catalogs.GetModelCatalog(ctx, node.ID)
		if err != nil {
			return dto, err
		}
	} else if node.ModelCatalog != nil {
		c = *node.ModelCatalog
	}
	if c.Source != "" {
		summary := &catalogSummary{Source: c.Source, Revision: c.Revision, Status: c.Status, LastSuccessAt: c.LastSuccessAt, ValidUntil: c.ValidUntil, LastErrorCode: c.LastErrorCode}
		for _, m := range c.Items {
			if m.Present {
				summary.ModelCount++
				if m.Enabled {
					summary.EnabledCount++
				}
			}
		}
		dto.ModelCatalog = summary
	}
	return dto, nil
}

func validEnabledModels(ids []string) bool {
	if len(ids) > 256 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !domain.ValidModelID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (h *handler) prepareNodeCatalog(w http.ResponseWriter, r *http.Request, request nodeRequest, input *domain.ModelNodeInput, current *domain.ModelNode, key string) bool {
	definition, _ := protocol.Lookup(input.ProtocolVersion)
	var old domain.ModelCatalog
	if current != nil && h.catalogs != nil {
		var err error
		old, err = h.catalogs.GetModelCatalog(r.Context(), current.ID)
		if err != nil {
			h.writeModelError(w, r, err)
			return false
		}
	}
	if request.EnabledModels != nil {
		if !validEnabledModels(*request.EnabledModels) {
			h.writeError(w, 400, "unsupported_model", "enabled_models 包含非法或重复的模型 ID")
			return false
		}
		if current != nil && request.CatalogRevision == nil {
			h.writeError(w, 400, "bad_request_error", "修改 enabled_models 必须提供 catalog_revision")
			return false
		}
		if current != nil && *request.CatalogRevision != old.Revision {
			h.writeError(w, 409, "catalog_version_conflict", "模型目录已更新，请刷新后重试")
			return false
		}
	}
	if request.CatalogRevision != nil && current != nil && *request.CatalogRevision != old.Revision {
		h.writeError(w, 409, "catalog_version_conflict", "模型目录已更新，请刷新后重试")
		return false
	}
	changed := current == nil || current.ServiceURL != input.ServiceURL || current.ProtocolVersion != input.ProtocolVersion || current.APIKeyFingerprint != input.APIKeyFingerprint
	catalog := old
	catalog.Items = slices.Clone(old.Items)
	switch definition.ModelSource {
	case "discovered":
		needsDiscovery := changed || (current != nil && !current.Enabled && input.Enabled && (old.LastSuccessAt == 0 || old.ValidUntil <= h.now().Unix()))
		if needsDiscovery {
			if current == nil && !input.Enabled && request.EnabledModels == nil {
				catalog = domain.ModelCatalog{Source: "discovered", Status: "uninitialized", Items: []domain.NodeModel{}}
			} else {
				if h.inventory == nil {
					h.writeError(w, 503, "model_discovery_unavailable", "模型发现尚未配置")
					return false
				}
				if key == "" {
					if h.nodeSecrets == nil {
						h.writeError(w, 503, "master_key_missing", "节点密钥主密钥未配置")
						return false
					}
					var err error
					key, err = h.nodeSecrets.Open(input.APIKeyNonce, input.APIKeyCiphertext)
					if err != nil {
						h.writeError(w, 503, "master_key_missing", "节点密钥无法读取")
						return false
					}
				}
				var err error
				catalog, err = h.inventory.Preview(r.Context(), *input, key)
				if err != nil {
					h.writeModelError(w, r, err)
					return false
				}
				if current != nil && request.EnabledModels == nil {
					selected := map[string]bool{}
					for _, m := range old.Items {
						selected[m.ModelID] = m.Enabled
					}
					for j := range catalog.Items {
						catalog.Items[j].Enabled = selected[catalog.Items[j].ModelID]
					}
				}
			}
		}
	case "builtin":
		if current == nil || changed || catalog.Source == "" {
			catalog = domain.ModelCatalog{Source: "builtin", Status: "ready", Items: []domain.NodeModel{{ModelID: "MiniMax-H3", Enabled: true, Present: true, Verified: true, Capabilities: protocol.BuiltinCapability(input.ProtocolVersion)}}}
		}
	case "manual":
		if input.LegacyModelCompat {
			if request.EnabledModels != nil && !slices.Equal(*request.EnabledModels, []string{"MiniMax-H3"}) {
				h.writeError(w, 400, "unsupported_model", "历史别名节点仅开放 MiniMax-H3")
				return false
			}
			if catalog.Source == "" {
				catalog = domain.ModelCatalog{Source: "manual", Status: "ready", Items: []domain.NodeModel{{ModelID: "MiniMax-H3", Enabled: true, Present: true, Capabilities: protocol.BuiltinCapability(input.ProtocolVersion)}}}
			}
		} else if request.EnabledModels != nil {
			catalog = domain.ModelCatalog{Source: "manual", Status: "ready", Items: []domain.NodeModel{}}
			for _, id := range *request.EnabledModels {
				catalog.Items = append(catalog.Items, domain.NodeModel{ModelID: id, Enabled: true, Present: true, Capabilities: protocol.BuiltinCapability(input.ProtocolVersion)})
			}
		} else if current == nil || current.ProtocolVersion != input.ProtocolVersion {
			catalog = domain.ModelCatalog{Source: "manual", Status: "empty", Items: []domain.NodeModel{}}
		}
	}
	if request.EnabledModels != nil {
		selected := map[string]bool{}
		for _, id := range *request.EnabledModels {
			selected[id] = true
		}
		for j := range catalog.Items {
			m := &catalog.Items[j]
			m.Enabled = selected[m.ModelID] && m.Present
			if m.Present {
				delete(selected, m.ModelID)
			}
		}
		if len(selected) > 0 {
			h.writeError(w, 400, "unsupported_model", "所选模型不在当前目录中")
			return false
		}
	}
	active := 0
	for _, m := range catalog.Items {
		if m.Present && m.Enabled {
			active++
		}
	}
	if input.Enabled && (active == 0 || (catalog.Source == "discovered" && (catalog.LastSuccessAt == 0 || catalog.ValidUntil <= h.now().Unix()))) {
		h.writeError(w, 400, "models_required", "启用节点必须至少开放一个模型")
		return false
	}
	if len(catalog.Items) == 0 && catalog.Status != "uninitialized" {
		catalog.Status = "empty"
	}
	catalog.NodeID = input.ID
	catalog.Revision = old.Revision
	input.ModelCatalog = &catalog
	return true
}

func (h *handler) probeTK2SD(ctx context.Context, input domain.ModelNodeInput, key string) NodeProbeResult {
	result := NodeProbeResult{ProtocolVersion: domain.ProtocolTK2SD, Checks: []NodeCheck{}, Capabilities: map[string]any{"model_source": "discovered"}}
	target, err := url.Parse(input.ServiceURL)
	if err == nil {
		client := http.Client{Timeout: input.RequestTimeout}
		if h.inventory != nil && h.inventory.HTTPClient != nil {
			client = *h.inventory.HTTPClient
			client.Timeout = input.RequestTimeout
		}
		err = tk2sd.NewClient(target, key, &client, 1<<20).Health(ctx)
	}
	if err == nil {
		result.Reachable = true
		result.Authenticated = true
		result.Checks = append(result.Checks, NodeCheck{Name: "health", Status: "passed"})
		return result
	}
	var remote *tk2sd.HTTPError
	result.Reachable = errors.As(err, &remote) && remote.StatusCode > 0
	code := "node_probe_failed"
	if remote != nil && remote.Class == tk2sd.ErrorAuthentication {
		code = "node_authentication_failed"
	}
	result.Checks = append(result.Checks, NodeCheck{Name: "health", Status: "failed", ErrorCode: code})
	return result
}
