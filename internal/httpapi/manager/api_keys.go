package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"minimax-h3-tc/internal/authkey"
	"minimax-h3-tc/internal/domain"
)

type APIKeyService interface {
	List(context.Context) ([]domain.ExternalAPIKey, error)
	Create(context.Context, string) (authkey.CreatedExternalAPIKey, error)
	Update(context.Context, string, int64, domain.ExternalAPIKeyUpdate) (domain.ExternalAPIKey, error)
	Delete(context.Context, string, int64) error
}

type apiKeyAPI struct {
	service APIKeyService
	logger  *slog.Logger
}

type apiKeyDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Key       string `json:"key,omitempty"`
	MaskedKey string `json:"masked_key"`
	Enabled   bool   `json:"enabled"`
	Version   int64  `json:"version"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type createAPIKeyRequest struct {
	Name string `json:"name"`
}
type updateAPIKeyRequest struct {
	Name    string `json:"name"`
	Enabled *bool  `json:"enabled"`
	Version *int64 `json:"version"`
}

func registerAPIKeyRoutes(mux *http.ServeMux, authenticate func(http.Handler) http.Handler, service APIKeyService, logger *slog.Logger) {
	if mux == nil || authenticate == nil || service == nil {
		return
	}
	api := &apiKeyAPI{service: service, logger: logger}
	register := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && !sameOriginMutation(r) {
				writeProfileError(w, http.StatusForbidden, "forbidden", "跨站请求被拒绝")
				return
			}
			handler(w, r)
		})))
	}
	register("GET /manager/api/api-keys", api.list)
	register("POST /manager/api/api-keys", api.create)
	register("PUT /manager/api/api-keys/{api_key_id}", api.update)
	register("DELETE /manager/api/api-keys/{api_key_id}", api.delete)
}

func (api *apiKeyAPI) list(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) != 0 {
		writeProfileError(w, http.StatusBadRequest, "bad_request_error", "不支持查询参数")
		return
	}
	items, err := api.service.List(r.Context())
	if err != nil {
		api.writeError(w, r, err)
		return
	}
	response := make([]apiKeyDTO, 0, len(items))
	enabled := 0
	for _, item := range items {
		response = append(response, makeAPIKeyDTO(item))
		if item.Enabled {
			enabled++
		}
	}
	writeProfileJSON(w, http.StatusOK, map[string]any{"items": response, "enabled_count": enabled})
}

func (api *apiKeyAPI) create(w http.ResponseWriter, r *http.Request) {
	var request createAPIKeyRequest
	if !readAPIKeyJSON(w, r, &request) {
		return
	}
	if !validAPIKeyName(request.Name) {
		writeProfileError(w, http.StatusBadRequest, "bad_request_error", "name 长度必须为 1 至 128 个字符")
		return
	}
	created, err := api.service.Create(r.Context(), request.Name)
	if err != nil {
		api.writeError(w, r, err)
		return
	}
	api.logger.InfoContext(r.Context(), "管理员已创建对外 API Key", "api_key_id", created.ID, "stage", "api_key_create")
	w.Header().Set("Location", "/manager/api/api-keys/"+created.ID)
	response := makeAPIKeyDTO(created.ExternalAPIKey)
	response.Key = created.Key
	writeProfileJSON(w, http.StatusCreated, response)
}

func (api *apiKeyAPI) update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("api_key_id")
	var request updateAPIKeyRequest
	if !validAPIKeyID(id) {
		writeProfileError(w, http.StatusBadRequest, "bad_request_error", "API Key 请求参数无效")
		return
	}
	if !readAPIKeyJSON(w, r, &request) {
		return
	}
	if !validAPIKeyName(request.Name) || request.Enabled == nil || request.Version == nil || *request.Version < 1 {
		writeProfileError(w, http.StatusBadRequest, "bad_request_error", "API Key 请求参数无效")
		return
	}
	updated, err := api.service.Update(r.Context(), id, *request.Version, domain.ExternalAPIKeyUpdate{Name: request.Name, Enabled: *request.Enabled})
	if err != nil {
		api.writeError(w, r, err)
		return
	}
	api.logger.InfoContext(r.Context(), "管理员已更新对外 API Key", "api_key_id", id, "enabled", updated.Enabled, "stage", "api_key_update")
	writeProfileJSON(w, http.StatusOK, makeAPIKeyDTO(updated))
}

func (api *apiKeyAPI) delete(w http.ResponseWriter, r *http.Request) {
	id, query := r.PathValue("api_key_id"), r.URL.Query()
	if !validAPIKeyID(id) || len(query) != 1 || len(query["version"]) != 1 {
		writeProfileError(w, http.StatusBadRequest, "bad_request_error", "version 查询参数无效")
		return
	}
	version, err := strconv.ParseInt(query.Get("version"), 10, 64)
	if err != nil || version < 1 {
		writeProfileError(w, http.StatusBadRequest, "bad_request_error", "version 查询参数无效")
		return
	}
	if err := api.service.Delete(r.Context(), id, version); err != nil {
		api.writeError(w, r, err)
		return
	}
	api.logger.InfoContext(r.Context(), "管理员已删除对外 API Key", "api_key_id", id, "stage", "api_key_delete")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *apiKeyAPI) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, kind, message := http.StatusInternalServerError, "server_error", "服务内部错误"
	switch {
	case errors.Is(err, domain.ErrAPIKeyNotFound):
		status, kind, message = http.StatusNotFound, "api_key_not_found", "密钥不存在"
	case errors.Is(err, domain.ErrAPIKeyNameConflict), errors.Is(err, domain.ErrAPIKeyDigestConflict):
		status, kind, message = http.StatusConflict, "api_key_name_conflict", "密钥名称已存在"
	case errors.Is(err, domain.ErrAPIKeyVersionConflict):
		status, kind, message = http.StatusConflict, "api_key_version_conflict", "密钥配置已变化，请刷新后重试"
	case errors.Is(err, domain.ErrAPIKeyInUse):
		status, kind, message = http.StatusConflict, "key_in_use", "该密钥仍有关联任务或幂等记录，请停用后保留"
	case errors.Is(err, authkey.ErrCacheRefresh):
		status, kind, message = http.StatusServiceUnavailable, "cache_refresh_failed", "密钥已保存，但鉴权缓存暂未刷新，请重新加载"
	default:
		api.logger.ErrorContext(r.Context(), "对外 API Key 接口处理失败", "error_type", "internal", "stage", "api_key_management")
	}
	writeProfileError(w, status, kind, message)
}

func readAPIKeyJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProfileError(w, http.StatusBadRequest, "bad_request_error", "Content-Type 必须为 application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	body, err := io.ReadAll(r.Body)
	if err != nil || !validUniqueJSONObject(body) {
		writeProfileError(w, http.StatusBadRequest, "bad_request_error", "请求 JSON 无效或包含重复字段")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeProfileError(w, http.StatusBadRequest, "bad_request_error", "请求 JSON 无效或包含未知字段")
		return false
	}
	return true
}
func validAPIKeyName(value string) bool {
	length := utf8.RuneCountInString(strings.TrimSpace(value))
	return length >= 1 && length <= 128
}
func validAPIKeyID(value string) bool {
	length := utf8.RuneCountInString(value)
	return length >= 1 && length <= 128 && !strings.ContainsAny(value, "/\\")
}
func makeAPIKeyDTO(key domain.ExternalAPIKey) apiKeyDTO {
	return apiKeyDTO{ID: key.ID, Name: key.Name, Key: key.Key, MaskedKey: key.MaskedKey(), Enabled: key.Enabled, Version: key.Version, CreatedAt: key.CreatedAt.UnixMilli(), UpdatedAt: key.UpdatedAt.UnixMilli()}
}
