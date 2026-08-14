package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"

	"minimax-h3-tc/internal/domain"
)

const profileRequestBodyLimit = 1 << 20

type ProfileService interface {
	Create(context.Context, string, domain.ProfileConfig, string) (domain.ModelRequestProfile, error)
	Update(context.Context, string, int64, domain.ProfileConfig, string) (domain.ModelRequestProfile, error)
	Get(context.Context, string) (domain.ModelRequestProfile, error)
	List(context.Context) ([]domain.ModelRequestProfile, error)
	Delete(context.Context, string, int64) error
}

type profileAPI struct {
	service       ProfileService
	administrator string
	logger        *slog.Logger
}

func RegisterProfileRoutes(mux *http.ServeMux, authenticate func(http.Handler) http.Handler, service ProfileService, administrator string, logger *slog.Logger) {
	if mux == nil || authenticate == nil || service == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	api := &profileAPI{service: service, administrator: administrator, logger: logger}
	register := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && !sameOriginMutation(r) {
				writeProfileError(w, http.StatusForbidden, "forbidden", "跨站请求被拒绝")
				return
			}
			handler(w, r)
		})))
	}
	register("GET /manager/api/request-profiles", api.list)
	register("POST /manager/api/request-profiles", api.create)
	register("GET /manager/api/request-profiles/{profile_id}", api.get)
	register("PUT /manager/api/request-profiles/{profile_id}", api.update)
	register("DELETE /manager/api/request-profiles/{profile_id}", api.delete)
}

type createProfileRequest struct{ domain.ProfileConfig }
type updateProfileRequest struct {
	domain.ProfileConfig
	RowVersion int64 `json:"row_version"`
}
type deleteProfileRequest struct {
	RowVersion int64 `json:"row_version"`
}

func (api *profileAPI) list(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) != 0 {
		writeProfileError(w, http.StatusBadRequest, "bad_request_error", "不支持查询参数")
		return
	}
	items, err := api.service.List(r.Context())
	if err != nil {
		api.writeError(w, r, err)
		return
	}
	writeProfileJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (api *profileAPI) get(w http.ResponseWriter, r *http.Request) {
	profile, err := api.service.Get(r.Context(), r.PathValue("profile_id"))
	if err != nil {
		api.writeError(w, r, err)
		return
	}
	writeProfileJSON(w, http.StatusOK, profile)
}

func (api *profileAPI) create(w http.ResponseWriter, r *http.Request) {
	var request createProfileRequest
	if !readProfileJSON(w, r, &request) {
		return
	}
	profile, err := api.service.Create(r.Context(), request.Resolution, request.ProfileConfig, api.administrator)
	if err != nil {
		api.writeError(w, r, err)
		return
	}
	api.logger.InfoContext(r.Context(), "管理员已创建模型请求配置", "profile_id", profile.ID, "resolution", profile.Resolution, "stage", "profile_create")
	w.Header().Set("Location", "/manager/api/request-profiles/"+profile.ID)
	writeProfileJSON(w, http.StatusCreated, profile)
}

func (api *profileAPI) update(w http.ResponseWriter, r *http.Request) {
	var request updateProfileRequest
	if !readProfileJSON(w, r, &request) {
		return
	}
	if request.RowVersion < 1 {
		writeProfileError(w, http.StatusBadRequest, "bad_request_error", "row_version 必须大于等于 1")
		return
	}
	profile, err := api.service.Update(r.Context(), r.PathValue("profile_id"), request.RowVersion, request.ProfileConfig, api.administrator)
	if err != nil {
		api.writeError(w, r, err)
		return
	}
	api.logger.InfoContext(r.Context(), "管理员已更新模型请求配置，修改立即生效", "profile_id", profile.ID, "resolution", profile.Resolution, "stage", "profile_update")
	writeProfileJSON(w, http.StatusOK, profile)
}

func (api *profileAPI) delete(w http.ResponseWriter, r *http.Request) {
	var request deleteProfileRequest
	if !readProfileJSON(w, r, &request) {
		return
	}
	if request.RowVersion < 1 {
		writeProfileError(w, http.StatusBadRequest, "bad_request_error", "row_version 必须大于等于 1")
		return
	}
	if err := api.service.Delete(r.Context(), r.PathValue("profile_id"), request.RowVersion); err != nil {
		api.writeError(w, r, err)
		return
	}
	api.logger.InfoContext(r.Context(), "管理员已删除模型请求配置", "profile_id", r.PathValue("profile_id"), "stage", "profile_delete")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *profileAPI) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrProfileNotFound):
		writeProfileError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, domain.ErrInvalidProfileConfig):
		writeProfileError(w, http.StatusUnprocessableEntity, "invalid_profile_config", err.Error())
	case errors.Is(err, domain.ErrProfileVersionConflict):
		writeProfileError(w, http.StatusConflict, "row_version_conflict", err.Error())
	case errors.Is(err, domain.ErrProfileKeyConflict):
		writeProfileError(w, http.StatusConflict, "profile_key_conflict", "逻辑分辨率名称已存在")
	default:
		api.logger.ErrorContext(r.Context(), "模型请求配置接口处理失败", "error_type", fmt.Sprintf("%T", err))
		writeProfileError(w, http.StatusInternalServerError, "server_error", "服务内部错误")
	}
}

func readProfileJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProfileError(w, http.StatusBadRequest, "bad_request_error", "Content-Type 必须为 application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, profileRequestBodyLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil || !validUniqueJSONObject(body) {
		writeProfileError(w, http.StatusBadRequest, "bad_request_error", "请求 JSON 无效或包含重复字段")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeProfileError(w, http.StatusBadRequest, "bad_request_error", "请求 JSON 无效或包含未知字段")
		return false
	}
	return true
}

func validUniqueJSONObject(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := validateUniqueJSONValue(decoder); err != nil {
		return false
	}
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func validateUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON 对象键无效")
			}
			if _, exists := seen[key]; exists {
				return errors.New("JSON 字段重复")
			}
			seen[key] = struct{}{}
			if err := validateUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := validateUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("JSON 结构无效")
	}
}

func writeProfileJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeProfileError(w http.ResponseWriter, status int, kind, message string) {
	writeProfileJSON(w, status, map[string]any{"error": map[string]string{"type": kind, "message": message}})
}
