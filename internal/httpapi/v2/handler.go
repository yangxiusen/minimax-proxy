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

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
)

type TaskStore interface {
	Create(context.Context, domain.NewTask, string, func() bool) (domain.Task, error)
	Get(context.Context, string, string) (domain.Task, error)
	List(context.Context, string, domain.TaskFilter) ([]domain.Task, int, error)
	CancelOrDelete(context.Context, string, string) (domain.Action, error)
}

type Dependencies struct {
	Store     TaskStore
	APIKeys   []config.APIKeyConfig
	Profiles  map[string]config.GenerationProfile
	Logger    *slog.Logger
	Wake      func()
	Available func() bool
}

type handler struct {
	store     TaskStore
	keys      []authKey
	profiles  map[string]config.GenerationProfile
	logger    *slog.Logger
	wake      func()
	available func() bool
}

type authKey struct {
	id     string
	digest [32]byte
}
type contextKey string

const ownerKey contextKey = "api_key_id"

func NewHandler(dependencies Dependencies) http.Handler {
	h := &handler{store: dependencies.Store, profiles: dependencies.Profiles, logger: dependencies.Logger, wake: dependencies.Wake, available: dependencies.Available}
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
	validated, err := ValidateCreate(request, h.profiles)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, "bad_request_error", err.Error()+" (2013)")
		return
	}
	canonical, err := json.Marshal(validated.CreateRequest)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	requestHash := sha256.Sum256(canonical)
	idempotencyHash := ""
	if idempotency := r.Header.Get("Idempotency-Key"); idempotency != "" {
		if !validIdempotencyKey(idempotency) {
			h.writeError(w, r, http.StatusBadRequest, "bad_request_error", "Idempotency-Key 必须是 1-128 个可打印 ASCII 字符 (2024)")
			return
		}
		digest := sha256.Sum256([]byte(idempotency))
		idempotencyHash = hex.EncodeToString(digest[:])
	}
	taskID, err := newNumericID()
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	task, err := h.store.Create(r.Context(), domain.NewTask{TaskID: taskID, APIKeyID: owner(r.Context()), Model: validated.Model, Scenario: validated.Scenario, RequestJSON: string(canonical), RequestHash: hex.EncodeToString(requestHash[:]), Resolution: validated.Resolution, Duration: validated.Duration, Ratio: validated.Ratio, InputImageCount: validated.InputImageCount}, idempotencyHash, h.available)
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	if h.wake != nil {
		h.wake()
	}
	h.logger.InfoContext(r.Context(), "视频生成任务已入队", "request_id", requestID(r.Context()), "task_id", task.TaskID, "api_key_id", task.APIKeyID, "scenario", task.Scenario)
	h.writeJSON(w, http.StatusOK, map[string]string{"task_id": task.TaskID})
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
	h.writeJSON(w, http.StatusOK, map[string]TaskResponse{"task": mapTask(task)})
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
		responses = append(responses, mapTask(item))
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
		digest := sha256.Sum256([]byte(header[7:]))
		matched := ""
		for _, key := range h.keys {
			if subtle.ConstantTimeCompare(digest[:], key.digest[:]) == 1 {
				matched = key.id
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

func mapTask(task domain.Task) TaskResponse {
	ratio := task.RatioActual
	if ratio == "" {
		ratio = task.RatioRequested
	}
	response := TaskResponse{ID: task.TaskID, Model: task.Model, Status: task.Status.V2(), CreatedAt: task.CreatedAt.Unix(), UpdatedAt: task.UpdatedAt.Unix(), Resolution: task.Resolution, Duration: task.Duration, Usage: TaskUsage{TotalSeconds: task.UsageTotalSeconds, InputSeconds: task.UsageInputSeconds, OutputSeconds: task.UsageOutputSeconds, InputImageCount: task.UsageInputImageCount}, Ratio: ratio, TaskType: "generation", Modality: "video"}
	if task.Status == domain.StatusSucceeded {
		response.Content = &TaskContent{URL: task.ResultPublicURL}
	}
	if task.Status == domain.StatusFailed {
		response.Error = &TaskError{Code: task.ErrorCode, Message: task.ErrorMessage}
	}
	return response
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
