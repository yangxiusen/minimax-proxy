package v2

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	artifactservice "minimax-h3-tc/internal/artifact"
	"minimax-h3-tc/internal/callback"
	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/inputobject"
	"minimax-h3-tc/internal/inputspool"
)

type TaskStore interface {
	Create(context.Context, domain.NewTask, string, func() bool) (domain.Task, error)
	Get(context.Context, string, string) (domain.Task, error)
	List(context.Context, string, domain.TaskFilter) ([]domain.Task, int, error)
	CancelOrDelete(context.Context, string, string) (domain.Action, error)
}

type ActiveProfileStore interface {
	GetProfileByResolution(context.Context, string) (domain.ModelRequestProfile, error)
}

type ArtifactURLSigner interface {
	SignURL(context.Context, string, string) (string, error)
}

type BearerAuthenticator interface {
	Authenticate(token string) (ownerID string, ok bool)
}

type idempotentTaskFinder interface {
	FindIdempotentTask(context.Context, string, string, string) (domain.Task, error)
}

type InputObjectPreparer interface {
	Prepare(context.Context, string, []byte) (inputobject.PreparedRequest, error)
}

type Dependencies struct {
	Store           TaskStore
	APIKeys         []config.APIKeyConfig
	Authenticator   BearerAuthenticator
	Profiles        map[string]config.GenerationProfile
	Logger          *slog.Logger
	Wake            func()
	Available       func() bool
	CallbackService *callback.Service
	CallbackCipher  callback.URLCipher
	ActiveProfiles  ActiveProfileStore
	ArtifactURLs    ArtifactURLSigner
	InputSpooler    *inputspool.Spooler
	InputObjects    InputObjectPreparer
}

type handler struct {
	store           TaskStore
	keys            []authKey
	authenticator   BearerAuthenticator
	profiles        map[string]config.GenerationProfile
	logger          *slog.Logger
	wake            func()
	available       func() bool
	callbackService *callback.Service
	callbackCipher  callback.URLCipher
	activeProfiles  ActiveProfileStore
	artifactURLs    ArtifactURLSigner
	inputSpooler    *inputspool.Spooler
	inputObjects    InputObjectPreparer
}

type authKey struct {
	id     string
	digest [32]byte
}
type contextKey string

const ownerKey contextKey = "api_key_id"

func NewHandler(dependencies Dependencies) http.Handler {
	h := &handler{store: dependencies.Store, authenticator: dependencies.Authenticator, profiles: dependencies.Profiles, logger: dependencies.Logger, wake: dependencies.Wake, available: dependencies.Available, callbackService: dependencies.CallbackService, callbackCipher: dependencies.CallbackCipher, activeProfiles: dependencies.ActiveProfiles, artifactURLs: dependencies.ArtifactURLs, inputSpooler: dependencies.InputSpooler, inputObjects: dependencies.InputObjects}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	for _, key := range dependencies.APIKeys {
		if key.Enabled {
			h.keys = append(h.keys, authKey{id: key.ID, digest: sha256.Sum256([]byte(key.Key))})
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/video_generation", h.create)
	mux.HandleFunc("GET /v2/query/video_generation/{task_id}", h.get)
	mux.HandleFunc("GET /v2/query/video_generation", h.list)
	mux.HandleFunc("DELETE /v2/video_generation/{task_id}", h.cancelOrDelete)
	return h.requestContext(h.authenticate(mux))
}

type ErrorResponse struct {
	Type      string      `json:"type"`
	Error     ErrorDetail `json:"error"`
	RequestID string      `json:"request_id"`
}
type ErrorDetail struct {
	Type     string `json:"type"`
	Message  string `json:"message"`
	HTTPCode string `json:"http_code"`
}

type TaskResponse struct {
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	Status     domain.V2Status `json:"status"`
	Error      *TaskError      `json:"error,omitempty"`
	CreatedAt  int64           `json:"created_at"`
	UpdatedAt  int64           `json:"updated_at"`
	Content    *TaskContent    `json:"content,omitempty"`
	Resolution string          `json:"resolution"`
	Duration   int             `json:"duration"`
	Usage      TaskUsage       `json:"usage"`
	Ratio      string          `json:"ratio"`
	TaskType   string          `json:"task_type"`
	Modality   string          `json:"modality"`
}
type TaskContent struct {
	URL string `json:"url"`
}
type TaskError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type TaskUsage struct {
	TotalSeconds    int `json:"total_seconds"`
	InputSeconds    int `json:"input_seconds"`
	OutputSeconds   int `json:"output_seconds"`
	InputImageCount int `json:"input_image_count"`
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); mediaType != "application/json" {
		h.writeError(w, r, http.StatusBadRequest, "bad_request_error", "Content-Type 必须为 application/json (2013)")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request CreateRequest
	if err := decoder.Decode(&request); err != nil {
		h.writeError(w, r, http.StatusBadRequest, "bad_request_error", "请求 JSON 无效 (2013)")
		return
	}
	if err := ensureJSONEnd(decoder); err != nil {
		h.writeError(w, r, http.StatusBadRequest, "bad_request_error", "请求只能包含一个 JSON 对象 (2013)")
		return
	}
	validationProfiles := h.profiles
	if h.activeProfiles != nil {
		validationProfiles = nil
	}
	validated, err := ValidateCreate(request, validationProfiles)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, "bad_request_error", err.Error()+" (2013)")
		return
	}
	var activeProfile domain.ModelRequestProfile
	if h.activeProfiles != nil {
		activeProfile, err = h.activeProfiles.GetProfileByResolution(r.Context(), validated.Resolution)
		if errors.Is(err, domain.ErrProfileNotFound) || errors.Is(err, domain.ErrInvalidProfileConfig) {
			h.writeError(w, r, http.StatusBadRequest, "bad_request_error", "请求分辨率不存在或未配置 (2013)")
			return
		}
		if err != nil {
			h.writeError(w, r, http.StatusServiceUnavailable, "profile_unavailable_error", "模型参数配置暂不可用")
			return
		}
		var profileConfig domain.ProfileConfig
		if err := json.Unmarshal([]byte(activeProfile.ConfigJSON), &profileConfig); err != nil {
			h.writeError(w, r, http.StatusServiceUnavailable, "profile_unavailable_error", "模型参数配置暂不可用")
			return
		}
		dimension, ok := profileConfig.Ratios[validated.Ratio]
		if !ok {
			h.writeError(w, r, http.StatusBadRequest, "bad_request_error", "ratio 未配置尺寸映射 (2013)")
			return
		}
		validated.Resolution = activeProfile.Resolution
		validated.CreateRequest.Resolution = activeProfile.Resolution
		validated.Width, validated.Height = dimension.BaseWidth, dimension.BaseHeight
	}
	var callbackTarget *callback.PreparedTarget
	if validated.CallbackURL != nil {
		if h.callbackService == nil || h.callbackCipher == nil {
			h.writeError(w, r, http.StatusServiceUnavailable, "callback_unavailable_error", "callback 服务未配置")
			return
		}
		callbackTarget, err = h.callbackService.PrepareTarget(r.Context(), validated.CallbackURL, h.callbackCipher)
		if err != nil {
			h.writeError(w, r, http.StatusBadRequest, "callback_url_error", "callback URL challenge 失败")
			return
		}
	}
	canonical, err := json.Marshal(validated.CreateRequest)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	requestHash := sha256.Sum256(canonical)
	persistedRequest := validated.CreateRequest
	persistedRequest.CallbackURL = nil
	persistedJSON, err := json.Marshal(persistedRequest)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	idempotencyHash := ""
	if idempotency := r.Header.Get("Idempotency-Key"); idempotency != "" {
		if !validIdempotencyKey(idempotency) {
			h.writeError(w, r, http.StatusBadRequest, "bad_request_error", "Idempotency-Key 必须是 1-128 个可打印 ASCII 字符 (2024)")
			return
		}
		digest := sha256.Sum256([]byte(idempotency))
		idempotencyHash = hex.EncodeToString(digest[:])
	}
	requestHashHex := hex.EncodeToString(requestHash[:])
	if idempotencyHash != "" {
		if finder, ok := h.store.(idempotentTaskFinder); ok {
			existing, findErr := finder.FindIdempotentTask(r.Context(), owner(r.Context()), idempotencyHash, requestHashHex)
			switch {
			case findErr == nil:
				h.writeJSON(w, http.StatusOK, map[string]string{"task_id": existing.TaskID})
				return
			case errors.Is(findErr, domain.ErrIdempotencyConflict):
				h.storeError(w, r, findErr)
				return
			case errors.Is(findErr, domain.ErrTaskNotFound):
			default:
				h.internalError(w, r, findErr)
				return
			}
		}
	}
	taskID, err := newNumericID()
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	var prepared inputspool.PreparedRequest
	objectInputsEnabled := false
	if h.inputObjects != nil {
		objectPrepared, prepareErr := h.inputObjects.Prepare(r.Context(), inputObjectNamespace(owner(r.Context()), requestHashHex), persistedJSON)
		if prepareErr != nil {
			if errors.Is(prepareErr, inputobject.ErrNotReady) {
				h.writeError(w, r, http.StatusServiceUnavailable, "object_storage_not_ready", "对象存储暂不可用")
			} else {
				h.writeError(w, r, http.StatusBadGateway, "input_object_upload_failed", "输入素材上传对象存储失败")
			}
			return
		}
		objectInputsEnabled = objectPrepared.Enabled
		if objectInputsEnabled {
			persistedJSON = objectPrepared.JSON
			prepared.Files = objectPrepared.Files
			for index := range prepared.Files {
				prepared.Files[index].TaskID = taskID
				prepared.Files[index].ID = objectInputID(taskID, prepared.Files[index])
			}
		}
	}
	if !objectInputsEnabled && h.inputSpooler != nil {
		prepared, err = h.inputSpooler.PrepareRequest(r.Context(), taskID, persistedJSON)
		if err != nil {
			h.writeError(w, r, http.StatusBadRequest, "bad_request_error", err.Error()+" (2013)")
			return
		}
		persistedJSON = prepared.JSON
	}
	newTask := domain.NewTask{TaskID: taskID, APIKeyID: owner(r.Context()), Model: validated.Model, Scenario: validated.Scenario, RequestJSON: string(persistedJSON), RequestHash: requestHashHex, Resolution: validated.Resolution, Duration: validated.Duration, Ratio: validated.Ratio, InputImageCount: validated.InputImageCount, InputSpoolFiles: prepared.Files}
	if h.activeProfiles != nil {
		newTask.ConfigSnapshotJSON = activeProfile.ConfigJSON
		newTask.ConfigHash = activeProfile.ConfigHash
		newTask.Stages, err = freezeStages(taskID, validated, activeProfile.ConfigJSON)
		if err != nil {
			h.internalError(w, r, err)
			return
		}
	}
	if callbackTarget != nil {
		newTask.CallbackURLCiphertext = callbackTarget.Ciphertext
		newTask.CallbackURLNonce = callbackTarget.Nonce
		delivery, deliveryErr := callback.NewDelivery("event_"+randomHex(16), taskID, "queued", 1, nil)
		if deliveryErr != nil {
			h.internalError(w, r, deliveryErr)
			return
		}
		newTask.CallbackDeliveryID = delivery.ID
		newTask.CallbackRequestBody = string(delivery.Body)
		newTask.CallbackRequestBodyHash = delivery.BodyHash
	}
	task, err := h.store.Create(r.Context(), newTask, idempotencyHash, h.available)
	if err != nil {
		_ = prepared.Cleanup()
		h.storeError(w, r, err)
		return
	}
	if task.TaskID != newTask.TaskID {
		_ = prepared.Cleanup()
	}
	if h.wake != nil {
		h.wake()
	}
	h.logger.InfoContext(r.Context(), "视频生成任务已入队", "request_id", requestID(r.Context()), "task_id", task.TaskID, "api_key_id", task.APIKeyID, "scenario", task.Scenario)
	h.writeJSON(w, http.StatusOK, map[string]string{"task_id": task.TaskID})
}

func inputObjectNamespace(ownerID, requestHash string) string {
	digest := sha256.Sum256([]byte(ownerID + "\x00" + requestHash))
	return hex.EncodeToString(digest[:])
}

func objectInputID(taskID string, file domain.InputSpoolFile) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", taskID, file.ContentIndex, file.ObjectURL)))
	return "input_" + hex.EncodeToString(digest[:16])
}

func freezeStages(taskID string, request ValidatedRequest, configJSON string) ([]domain.NewTaskStage, error) {
	var config domain.ProfileConfig
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return nil, errors.New("已发布配置快照损坏")
	}
	mapping, ok := config.Ratios[request.Ratio]
	if !ok {
		return nil, errors.New("已发布配置缺少请求比例映射")
	}
	modelMode := config.Generation.ModelMode
	if modelMode == "" {
		modelMode = "high_quality"
	}
	const mainModel = "__follow_model_mode__"
	const fps = 24
	seed, err := randomSeed()
	if err != nil {
		return nil, err
	}
	loras := make([]map[string]any, 0, len(config.LoRAs))
	for _, lora := range config.LoRAs {
		loras = append(loras, map[string]any{"name": lora.Name, "strength": lora.Strength})
	}
	stageParameters := []struct {
		stageType  string
		parameters map[string]any
		expected   map[string]any
	}{
		{
			stageType: "generation",
			parameters: map[string]any{
				"scenario": request.Scenario, "prompt": request.Prompt,
				"width": mapping.BaseWidth, "height": mapping.BaseHeight,
				"duration": request.Duration, "fps": fps, "steps": config.Generation.Steps, "seed": seed,
				"model_mode": modelMode, "sage_attention": config.Generation.SageAttention,
				"easycache_enabled": config.Generation.CacheMode == "easycache",
				"te_speed_enabled":  config.Generation.CacheMode == "te_speed",
				"loras":             loras, "ref_image_size": "match",
				"fl2va_model": mainModel, "ref2va_model": mainModel,
			},
			expected: map[string]any{"preserve_timeline": true, "preserve_audio": true},
		},
	}
	if config.Interpolation.Enabled {
		stageParameters = append(stageParameters, struct {
			stageType  string
			parameters map[string]any
			expected   map[string]any
		}{
			stageType: "interpolation",
			parameters: map[string]any{
				"engine": config.Interpolation.Engine, "scale": config.Interpolation.Scale,
			},
			expected: map[string]any{"fps_multiplier": config.Interpolation.Scale, "preserve_timeline": true, "preserve_audio": true},
		})
	}
	if config.Restoration.Enabled {
		stageParameters = append(stageParameters, struct {
			stageType  string
			parameters map[string]any
			expected   map[string]any
		}{
			stageType: "restoration",
			parameters: map[string]any{
				"engine": config.Restoration.Engine, "scale": config.Restoration.Scale,
				"source_width": mapping.BaseWidth, "source_height": mapping.BaseHeight,
				"target_width": mapping.TargetWidth, "target_height": mapping.TargetHeight,
			},
			expected: map[string]any{"preserve_timeline": true, "preserve_audio": true},
		})
	}
	if request.AIGCWatermark != nil && *request.AIGCWatermark {
		stageParameters = append(stageParameters, struct {
			stageType  string
			parameters map[string]any
			expected   map[string]any
		}{
			stageType: "watermark",
			parameters: map[string]any{
				"enabled": true, "aigc_watermark": true,
			},
			expected: map[string]any{"preserve_timeline": true, "preserve_audio": true},
		})
	}
	stages := make([]domain.NewTaskStage, 0, len(stageParameters))
	for index, stage := range stageParameters {
		snapshot, err := json.Marshal(map[string]any{
			"task_id": taskID, "stage_type": stage.stageType,
			"parameters": stage.parameters, "expected_media": stage.expected,
		})
		if err != nil {
			return nil, err
		}
		stages = append(stages, domain.NewTaskStage{
			ID: "stage_" + randomHex(16), StageType: stage.stageType, StageOrder: (index + 1) * 10,
			MaxAttempts: 3, ConfigSnapshotJSON: string(snapshot),
		})
	}
	return stages, nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	if len(taskID) < 1 || len(taskID) > 64 {
		h.writeError(w, r, http.StatusBadRequest, "bad_request_error", "invalid task_id (2001)")
		return
	}
	task, err := h.store.Get(r.Context(), owner(r.Context()), taskID)
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	response, err := h.mapTask(r.Context(), task)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]TaskResponse{"task": response})
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	allowed := map[string]bool{"page_num": true, "page_size": true, "filter.status": true, "filter.task_ids": true, "filter.model": true, "filter.task_type": true}
	for key := range r.URL.Query() {
		if !allowed[key] {
			h.writeError(w, r, http.StatusBadRequest, "bad_request_error", "未知查询参数 (2013)")
			return
		}
	}
	pageNum, err := parsePage(r.URL.Query().Get("page_num"), 1, 1, int(^uint(0)>>1))
	if err != nil {
		h.writeError(w, r, 400, "bad_request_error", "page_num 无效 (2013)")
		return
	}
	pageSize, err := parsePage(r.URL.Query().Get("page_size"), 20, 1, 100)
	if err != nil {
		h.writeError(w, r, 400, "bad_request_error", "page_size 无效 (2013)")
		return
	}
	status := domain.V2Status(r.URL.Query().Get("filter.status"))
	if status != "" && !validStatus(status) {
		h.writeError(w, r, 400, "bad_request_error", "filter.status 无效 (2013)")
		return
	}
	taskIDs := r.URL.Query()["filter.task_ids"]
	if len(taskIDs) > 100 {
		h.writeError(w, r, 400, "bad_request_error", "filter.task_ids 最多 100 个 (2013)")
		return
	}
	model, taskType := r.URL.Query().Get("filter.model"), r.URL.Query().Get("filter.task_type")
	if (model != "" && model != "MiniMax-H3") || (taskType != "" && taskType != "generation") {
		h.writeJSON(w, http.StatusOK, map[string]any{"items": []TaskResponse{}, "total": 0})
		return
	}
	items, total, err := h.store.List(r.Context(), owner(r.Context()), domain.TaskFilter{Status: status, TaskIDs: taskIDs, PageNum: pageNum, PageSize: pageSize})
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	responses := make([]TaskResponse, 0, len(items))
	for _, item := range items {
		response, err := h.mapTask(r.Context(), item)
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		responses = append(responses, response)
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"items": responses, "total": total})
}

func (h *handler) cancelOrDelete(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	if len(taskID) < 1 || len(taskID) > 64 {
		h.writeError(w, r, 400, "bad_request_error", "invalid task_id (2001)")
		return
	}
	action, err := h.store.CancelOrDelete(r.Context(), owner(r.Context()), taskID)
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	if h.wake != nil {
		h.wake()
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"task_id": taskID, "action": string(action), "status": string(action)})
}

func (h *handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") || len(header) <= 7 {
			h.writeError(w, r, http.StatusUnauthorized, "authorized_error", "login fail: 请在 Authorization 中携带 API Key (1004)")
			return
		}
		matched := ""
		if h.authenticator != nil {
			matched, _ = h.authenticator.Authenticate(header[7:])
		} else {
			digest := sha256.Sum256([]byte(header[7:]))
			for _, key := range h.keys {
				if subtle.ConstantTimeCompare(digest[:], key.digest[:]) == 1 {
					matched = key.id
				}
			}
		}
		if matched == "" {
			h.writeError(w, r, http.StatusUnauthorized, "authorized_error", "login fail: API Key 无效 (1004)")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ownerKey, matched)))
	})
}

func (h *handler) requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if len(id) < 1 || len(id) > 64 {
			id = randomHex(16)
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey("request_id"), id)))
	})
}

func (h *handler) storeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrResourceUnavailable):
		h.writeError(w, r, http.StatusServiceUnavailable, "resource_unavailable_error", "资源不足，请稍后重试")
	case errors.Is(err, domain.ErrTaskNotFound):
		h.writeError(w, r, 400, "bad_request_error", "invalid task_id (2001)")
	case errors.Is(err, domain.ErrTaskNotOperable), errors.Is(err, domain.ErrStateConflict):
		h.writeError(w, r, 400, "bad_request_error", "task cannot be cancelled or deleted in its current state (2021)")
	case errors.Is(err, domain.ErrIdempotencyConflict):
		h.writeError(w, r, 409, "idempotency_error", "idempotency key was used with a different request (2024)")
	case errors.Is(err, domain.ErrPerKeyLimit), errors.Is(err, domain.ErrGlobalLimit):
		h.writeError(w, r, 429, "rate_limit_error", "unfinished task limit exceeded (1002)")
	default:
		h.internalError(w, r, err)
	}
}

func (h *handler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "接口处理失败", "request_id", requestID(r.Context()), "error_code", "internal_error", "error_type", fmt.Sprintf("%T", err))
	h.writeError(w, r, 500, "server_error", "internal error (1000)")
}

func (h *handler) writeError(w http.ResponseWriter, r *http.Request, status int, kind, message string) {
	h.writeJSON(w, status, ErrorResponse{Type: "error", Error: ErrorDetail{Type: kind, Message: message, HTTPCode: strconv.Itoa(status)}, RequestID: requestID(r.Context())})
}
func (h *handler) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (h *handler) mapTask(ctx context.Context, task domain.Task) (TaskResponse, error) {
	ratio := task.RatioActual
	if ratio == "" {
		ratio = task.RatioRequested
	}
	response := TaskResponse{ID: task.TaskID, Model: task.Model, Status: task.Status.V2(), CreatedAt: task.CreatedAt.Unix(), UpdatedAt: task.UpdatedAt.Unix(), Resolution: task.Resolution, Duration: task.Duration, Usage: TaskUsage{TotalSeconds: task.UsageTotalSeconds, InputSeconds: task.UsageInputSeconds, OutputSeconds: task.UsageOutputSeconds, InputImageCount: task.UsageInputImageCount}, Ratio: ratio, TaskType: "generation", Modality: "video"}
	if task.Status == domain.StatusSucceeded {
		if task.ResultArtifactID != "" {
			if h.artifactURLs == nil {
				return TaskResponse{}, errors.New("artifact URL signer is not configured")
			}
			resultURL, err := h.artifactURLs.SignURL(ctx, task.ResultArtifactID, task.APIKeyID)
			if err != nil {
				return TaskResponse{}, err
			}
			response.Content = &TaskContent{URL: resultURL}
		} else if resultURL, ok := artifactservice.LegacyPublicURL(task.ResultPublicURL); ok {
			response.Content = &TaskContent{URL: resultURL}
		}
	}
	if task.Status == domain.StatusFailed {
		code, message := domain.LocalizeOfficialError(task.ErrorCode, task.ErrorMessage, task.UpstreamFeedback)
		response.Error = &TaskError{Code: code, Message: message}
	}
	return response, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else {
		return fmt.Errorf("extra JSON")
	}
}
func parsePage(value string, fallback, min, max int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < min || parsed > max {
		return 0, errors.New("invalid page")
	}
	return parsed, nil
}
func validStatus(status domain.V2Status) bool {
	return status == domain.V2Queued || status == domain.V2Running || status == domain.V2Succeeded || status == domain.V2Failed || status == domain.V2Cancelled
}
func validIdempotencyKey(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, char := range []byte(value) {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}
func owner(ctx context.Context) string { value, _ := ctx.Value(ownerKey).(string); return value }
func requestID(ctx context.Context) string {
	value, _ := ctx.Value(contextKey("request_id")).(string)
	return value
}
func randomHex(size int) string {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buffer)
}
func newNumericID() (string, error) {
	limit := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	number, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%018d", number), nil
}

func randomSeed() (int64, error) {
	number, err := rand.Int(rand.Reader, new(big.Int).SetInt64(int64(^uint64(0)>>1)))
	if err != nil {
		return 0, err
	}
	return number.Int64(), nil
}
