package manager

import (
	"bytes"
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
	"mime"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	artifactservice "minimax-h3-tc/internal/artifact"
	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	monitorcache "minimax-h3-tc/internal/monitor"
)

const (
	SessionCookieName = "manager_session"
	loginBodyLimit    = 4 << 10
	loginFailureLimit = 5
	loginBlockPeriod  = time.Minute
	loginSourceLimit  = 4096
	failureCleanupGap = time.Minute
)

type TaskStore interface {
	ListAdminTasks(context.Context, domain.AdminTaskFilter) ([]domain.AdminTaskSummary, int, error)
	RequestAdminCancel(context.Context, string) error
	AdminDelete(context.Context, string) error
}

type ArtifactURLSigner interface {
	SignURL(artifactID, ownerID string) (string, error)
}

type NodeStore interface {
	ListModelNodes(context.Context) ([]domain.ModelNode, error)
	GetModelNode(context.Context, string) (domain.ModelNode, error)
	CreateModelNode(context.Context, domain.ModelNodeInput) (domain.ModelNode, error)
	UpdateModelNode(context.Context, string, int64, domain.ModelNodeInput) (domain.ModelNode, error)
	DeleteModelNode(context.Context, string, int64) error
}

type NodeSecretCodec interface {
	Seal(string) ([]byte, []byte, string, error)
	Open([]byte, []byte) (string, error)
}

type Dependencies struct {
	Admin          config.AdminConfig
	Cache          *monitorcache.Cache
	Store          TaskStore
	Nodes          NodeStore
	Logger         *slog.Logger
	Now            func() time.Time
	Rand           io.Reader
	Wake           func()
	ProbeNode      func(context.Context, NodeProbeInput) NodeProbeResult
	NodeSecrets    NodeSecretCodec
	ProfileService ProfileService
	Cleanups       CleanupStore
	WakeCleanup    func()
	APIKeyService  APIKeyService
	ArtifactURLs   ArtifactURLSigner
}

type handler struct {
	root            http.Handler
	cache           *monitorcache.Cache
	store           TaskStore
	nodes           NodeStore
	logger          *slog.Logger
	now             func() time.Time
	random          io.Reader
	usernameDigest  [sha256.Size]byte
	passwordDigest  [sha256.Size]byte
	sessionTTL      time.Duration
	secureCookie    bool
	monitorInterval time.Duration
	wake            func()
	probeNode       func(context.Context, NodeProbeInput) NodeProbeResult
	nodeSecrets     NodeSecretCodec
	artifactURLs    ArtifactURLSigner

	sessionMu          sync.Mutex
	sessions           map[[sha256.Size]byte]time.Time
	failureMu          sync.Mutex
	failures           map[string]loginFailure
	nextFailureCleanup time.Time
}

type loginFailure struct {
	count        int
	inflight     int
	blockedUntil time.Time
	lastSeen     time.Time
}

func NewHandler(dependencies Dependencies) http.Handler {
	now := dependencies.Now
	if now == nil {
		now = time.Now
	}
	random := dependencies.Rand
	if random == nil {
		random = rand.Reader
	}
	logger := dependencies.Logger
	if logger == nil {
		logger = slog.Default()
	}
	cache := dependencies.Cache
	if cache == nil {
		cache = monitorcache.NewCache(nil)
	}
	monitorInterval := dependencies.Admin.MonitorInterval
	if monitorInterval <= 0 {
		monitorInterval = 5 * time.Second
	}
	h := &handler{
		cache:           cache,
		store:           dependencies.Store,
		nodes:           dependencies.Nodes,
		logger:          logger,
		now:             now,
		random:          random,
		usernameDigest:  sha256.Sum256([]byte(dependencies.Admin.Username)),
		passwordDigest:  sha256.Sum256([]byte(dependencies.Admin.Password)),
		sessionTTL:      dependencies.Admin.SessionTTL,
		secureCookie:    dependencies.Admin.SecureCookie,
		monitorInterval: monitorInterval,
		wake:            dependencies.Wake,
		probeNode:       dependencies.ProbeNode,
		nodeSecrets:     dependencies.NodeSecrets,
		artifactURLs:    dependencies.ArtifactURLs,
		sessions:        make(map[[sha256.Size]byte]time.Time),
		failures:        make(map[string]loginFailure),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /manager/api/session", h.createSession)
	mux.Handle("DELETE /manager/api/session", h.authenticate(http.HandlerFunc(h.deleteSession)))
	mux.Handle("GET /manager/api/snapshot", h.authenticate(http.HandlerFunc(h.snapshot)))
	mux.Handle("GET /manager/api/tasks", h.authenticate(http.HandlerFunc(h.tasks)))
	mux.Handle("POST /manager/api/tasks/{task_id}/cancel", h.authenticate(http.HandlerFunc(h.cancelTask)))
	mux.Handle("DELETE /manager/api/tasks/{task_id}", h.authenticate(http.HandlerFunc(h.deleteTask)))
	mux.Handle("GET /manager/api/nodes", h.authenticate(http.HandlerFunc(h.listNodes)))
	mux.Handle("POST /manager/api/nodes", h.authenticate(http.HandlerFunc(h.createNode)))
	mux.Handle("PUT /manager/api/nodes/{node_id}", h.authenticate(http.HandlerFunc(h.updateNode)))
	mux.Handle("DELETE /manager/api/nodes/{node_id}", h.authenticate(http.HandlerFunc(h.deleteNode)))
	mux.Handle("POST /manager/api/nodes/test", h.authenticate(http.HandlerFunc(h.testNode)))
	RegisterProfileRoutes(mux, h.authenticate, dependencies.ProfileService, dependencies.Admin.Username, logger)
	registerCleanupRoutes(mux, h.authenticate, dependencies.Cleanups, dependencies.Admin.Username, dependencies.WakeCleanup, now, h.writeJSON, h.writeError)
	registerAPIKeyRoutes(mux, h.authenticate, dependencies.APIKeyService, logger)
	h.registerWebRoutes(mux)
	h.root = h.noStore(mux)
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.root.ServeHTTP(w, r)
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *handler) createSession(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	source := remoteSource(r.RemoteAddr)
	if !h.beginLoginAttempt(source, now) {
		h.writeError(w, http.StatusTooManyRequests, "rate_limit_error", "登录尝试过于频繁，请稍后重试")
		return
	}
	succeeded := false
	defer func() { h.finishLoginAttempt(source, now, succeeded) }()
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.loginRequestError(w, http.StatusBadRequest, "Content-Type 必须为 application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, loginBodyLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.loginRequestError(w, http.StatusBadRequest, "请求 JSON 无效")
		return
	}
	request, err := decodeLoginRequest(body)
	if err != nil {
		h.loginRequestError(w, http.StatusBadRequest, "请求 JSON 无效")
		return
	}
	usernameDigest := sha256.Sum256([]byte(request.Username))
	passwordDigest := sha256.Sum256([]byte(request.Password))
	usernameMatch := subtle.ConstantTimeCompare(usernameDigest[:], h.usernameDigest[:])
	passwordMatch := subtle.ConstantTimeCompare(passwordDigest[:], h.passwordDigest[:])
	if usernameMatch&passwordMatch != 1 {
		h.logger.WarnContext(r.Context(), "管理控制台登录失败", "event", "monitor_login_failed")
		h.writeError(w, http.StatusUnauthorized, "authentication_error", "账号或密码错误")
		return
	}

	token := make([]byte, 32)
	if _, err := io.ReadFull(h.random, token); err != nil {
		h.internalError(w, r, err)
		return
	}
	digest := sha256.Sum256(token)
	expires := now.Add(h.sessionTTL)
	h.sessionMu.Lock()
	h.cleanupSessionsLocked(now)
	h.sessions[digest] = expires
	h.sessionMu.Unlock()
	time.AfterFunc(h.sessionTTL, func() {
		h.sessionMu.Lock()
		if current, ok := h.sessions[digest]; ok && current.Equal(expires) {
			delete(h.sessions, digest)
		}
		h.sessionMu.Unlock()
	})
	succeeded = true
	h.logger.InfoContext(r.Context(), "管理控制台会话已创建", "event", "manager_session_created")
	h.setSessionCookie(w, hex.EncodeToString(token), expires, h.secureCookie || r.TLS != nil, 0)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) loginRequestError(w http.ResponseWriter, status int, message string) {
	h.writeError(w, status, "bad_request_error", message)
}

func (h *handler) deleteSession(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
		if token, err := hex.DecodeString(cookie.Value); err == nil && len(token) == 32 {
			digest := sha256.Sum256(token)
			h.sessionMu.Lock()
			delete(h.sessions, digest)
			h.sessionMu.Unlock()
		}
	}
	h.setSessionCookie(w, "", time.Unix(1, 0), h.secureCookie || r.TLS != nil, -1)
	h.logger.InfoContext(r.Context(), "管理控制台会话已销毁", "event", "manager_session_deleted")
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil || !h.validSession(cookie.Value, h.now()) {
			h.writeError(w, http.StatusUnauthorized, "authentication_error", "未登录或会话已过期")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handler) validSession(value string, now time.Time) bool {
	token, err := hex.DecodeString(value)
	if err != nil || len(token) != 32 {
		return false
	}
	digest := sha256.Sum256(token)
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()
	h.cleanupSessionsLocked(now)
	expires, ok := h.sessions[digest]
	return ok && now.Before(expires)
}

func (h *handler) cleanupSessionsLocked(now time.Time) {
	for digest, expires := range h.sessions {
		if !now.Before(expires) {
			delete(h.sessions, digest)
		}
	}
}

func (h *handler) beginLoginAttempt(source string, now time.Time) bool {
	h.failureMu.Lock()
	defer h.failureMu.Unlock()
	h.cleanupFailuresIfDueLocked(now)
	failure, ok := h.failures[source]
	if ok && failure.inflight == 0 && !failure.blockedUntil.IsZero() && !now.Before(failure.blockedUntil) {
		delete(h.failures, source)
		failure = loginFailure{}
		ok = false
	}
	if !ok && len(h.failures) >= loginSourceLimit {
		return false
	}
	if failure.count+failure.inflight >= loginFailureLimit {
		return false
	}
	failure.inflight++
	failure.lastSeen = now
	h.failures[source] = failure
	return true
}

func (h *handler) finishLoginAttempt(source string, now time.Time, succeeded bool) {
	h.failureMu.Lock()
	defer h.failureMu.Unlock()
	failure, ok := h.failures[source]
	if !ok {
		return
	}
	if failure.inflight > 0 {
		failure.inflight--
	}
	failure.lastSeen = now
	if succeeded {
		failure.count = 0
		failure.blockedUntil = time.Time{}
		if failure.inflight == 0 {
			delete(h.failures, source)
			return
		}
	} else {
		failure.count++
		if failure.count >= loginFailureLimit {
			failure.blockedUntil = now.Add(loginBlockPeriod)
		}
	}
	h.failures[source] = failure
}

func (h *handler) cleanupFailuresIfDueLocked(now time.Time) {
	if !h.nextFailureCleanup.IsZero() && now.Before(h.nextFailureCleanup) {
		return
	}
	for source, failure := range h.failures {
		if failure.inflight == 0 && ((!failure.blockedUntil.IsZero() && !now.Before(failure.blockedUntil)) || now.Sub(failure.lastSeen) >= loginBlockPeriod) {
			delete(h.failures, source)
		}
	}
	h.nextFailureCleanup = now.Add(failureCleanupGap)
}

func remoteSource(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return host
	}
	return remoteAddr
}

func (h *handler) setSessionCookie(w http.ResponseWriter, value string, expires time.Time, secure bool, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/manager",
		Expires:  expires,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

type snapshotResponse struct {
	UpdatedAt        int64           `json:"updated_at"`
	StaleAfterSecond int64           `json:"stale_after_seconds"`
	Summary          snapshotSummary `json:"summary"`
	Upstreams        []upstreamDTO   `json:"upstreams"`
}

type snapshotSummary struct {
	Healthy   int `json:"healthy"`
	Unhealthy int `json:"unhealthy"`
	Unknown   int `json:"unknown"`
	Running   int `json:"running"`
}

type upstreamDTO struct {
	ID                 string                     `json:"id"`
	Address            string                     `json:"address"`
	Enabled            bool                       `json:"enabled"`
	Applying           bool                       `json:"applying"`
	Health             monitorcache.HealthStatus  `json:"health"`
	Runtime            monitorcache.RuntimeStatus `json:"runtime"`
	PrivateQueue       *int                       `json:"private_queue"`
	CPUPercent         *float64                   `json:"cpu_percent"`
	MemoryPercent      *float64                   `json:"memory_percent"`
	GPUPercent         *float64                   `json:"gpu_percent"`
	VRAMPercent        *float64                   `json:"vram_percent"`
	CheckedAt          int64                      `json:"checked_at"`
	LastHealthyAt      int64                      `json:"last_healthy_at"`
	UpdatedAt          int64                      `json:"updated_at"`
	CurrentTask        *currentTaskDTO            `json:"current_task"`
	LatestFinishedTask *finishedTaskDTO           `json:"latest_finished_task"`
	LastError          *errorDTO                  `json:"last_error"`
}

type currentTaskDTO struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	StartedAt int64  `json:"started_at"`
}

type finishedTaskDTO struct {
	ID              string `json:"id"`
	APIKeyID        string `json:"api_key_id"`
	Status          string `json:"status"`
	DurationSeconds int64  `json:"duration_seconds"`
	FinishedAt      int64  `json:"finished_at"`
}

type errorDTO struct {
	Code    string `json:"code"`
	Summary string `json:"summary"`
}

func (h *handler) snapshot(w http.ResponseWriter, _ *http.Request) {
	nodes := h.cache.List()
	staleAfter := int64((3*h.monitorInterval + time.Second - 1) / time.Second)
	response := snapshotResponse{StaleAfterSecond: staleAfter, Upstreams: make([]upstreamDTO, 0, len(nodes))}
	for _, node := range nodes {
		item := upstreamDTO{
			ID: node.ID, Address: node.Address, Enabled: !node.Disabled, Applying: node.Applying, Health: node.Health, Runtime: node.Runtime,
			PrivateQueue: node.PrivateQueue, CPUPercent: node.CPUPercent, MemoryPercent: node.MemoryPercent,
			GPUPercent: node.GPUPercent, VRAMPercent: node.VRAMPercent, CheckedAt: unixTime(node.CheckedAt),
			LastHealthyAt: unixTime(node.LastHealthyAt), UpdatedAt: unixTime(node.UpdatedAt),
		}
		if !node.Disabled {
			switch node.Health {
			case monitorcache.HealthHealthy:
				response.Summary.Healthy++
			case monitorcache.HealthUnhealthy:
				response.Summary.Unhealthy++
			default:
				response.Summary.Unknown++
			}
			if node.Runtime == monitorcache.RuntimeRunning {
				response.Summary.Running++
			}
		}
		if item.UpdatedAt > response.UpdatedAt {
			response.UpdatedAt = item.UpdatedAt
		}
		if node.CurrentTask != nil {
			item.CurrentTask = &currentTaskDTO{ID: node.CurrentTask.ID, Status: node.CurrentTask.Status, StartedAt: unixTime(node.CurrentTask.StartedAt)}
		}
		if node.LatestFinishedTask != nil {
			item.LatestFinishedTask = &finishedTaskDTO{ID: node.LatestFinishedTask.ID, APIKeyID: node.LatestFinishedTask.APIKeyID, Status: node.LatestFinishedTask.Status, DurationSeconds: node.LatestFinishedTask.DurationSeconds, FinishedAt: unixTime(node.LatestFinishedTask.FinishedAt)}
		}
		if node.LastError != nil {
			item.LastError = &errorDTO{Code: node.LastError.Code, Summary: node.LastError.Summary}
		}
		response.Upstreams = append(response.Upstreams, item)
	}
	h.writeJSON(w, http.StatusOK, response)
}

type tasksResponse struct {
	Items    []taskDTO `json:"items"`
	Total    int       `json:"total"`
	PageNum  int       `json:"page_num"`
	PageSize int       `json:"page_size"`
}

type taskDTO struct {
	ID              string          `json:"id"`
	APIKeyID        string          `json:"api_key_id"`
	Status          domain.V2Status `json:"status"`
	UpstreamID      string          `json:"upstream_id"`
	Scenario        string          `json:"scenario"`
	Resolution      string          `json:"resolution"`
	DurationSeconds *int64          `json:"duration_seconds,omitempty"`
	CreatedAt       int64           `json:"created_at"`
	Phase           string          `json:"phase"`
	RetryCount      int             `json:"retry_count"`
	CanCancel       bool            `json:"can_cancel"`
	CanDelete       bool            `json:"can_delete"`
	VideoURL        *string         `json:"video_url"`
}

func (h *handler) tasks(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	allowed := map[string]bool{"page_num": true, "page_size": true, "status": true, "upstream_id": true, "search": true}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 {
			h.writeError(w, http.StatusBadRequest, "bad_request_error", "未知或重复的查询参数")
			return
		}
	}
	pageNum, err := parsePositiveInt(query.Get("page_num"), 1)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "page_num 无效")
		return
	}
	pageSize := 10
	if value := query.Get("page_size"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed != 10 && parsed != 20 {
			h.writeError(w, http.StatusBadRequest, "bad_request_error", "page_size 仅允许 10 或 20")
			return
		}
		pageSize = parsed
	}
	status := domain.V2Status(query.Get("status"))
	if status != "" && !validStatus(status) {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "status 无效")
		return
	}
	if h.store == nil {
		h.internalError(w, r, errors.New("monitor task store is nil"))
		return
	}
	filter := domain.AdminTaskFilter{Status: status, UpstreamID: query.Get("upstream_id"), Search: query.Get("search"), PageNum: pageNum, PageSize: pageSize}
	items, total, err := h.store.ListAdminTasks(r.Context(), filter)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	response := tasksResponse{Items: make([]taskDTO, 0, len(items)), Total: total, PageNum: pageNum, PageSize: pageSize}
	now := h.now()
	for _, item := range items {
		videoURL, err := h.publicVideoURL(item)
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		response.Items = append(response.Items, taskDTO{ID: item.TaskID, APIKeyID: item.APIKeyID, Status: item.Status, UpstreamID: item.UpstreamID, Scenario: item.Scenario, Resolution: item.Resolution, DurationSeconds: taskDurationSeconds(item, now), CreatedAt: unixTime(item.CreatedAt), Phase: taskPhase(item), RetryCount: item.RetryCount, CanCancel: item.InternalStatus.AdminCanCancel(), CanDelete: item.InternalStatus.AdminCanDelete(), VideoURL: videoURL})
	}
	h.writeJSON(w, http.StatusOK, response)
}

func (h *handler) cancelTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	if !validTaskID(taskID) {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "task_id 无效")
		return
	}
	if h.store == nil {
		h.internalError(w, r, errors.New("monitor task store is nil"))
		return
	}
	if err := h.store.RequestAdminCancel(r.Context(), taskID); err != nil {
		h.writeTaskActionError(w, r, err)
		return
	}
	if h.wake != nil {
		h.wake()
	}
	h.logger.InfoContext(r.Context(), "管理员已请求中止任务", "task_id", taskID, "stage", "admin_cancel")
	h.writeJSON(w, http.StatusAccepted, map[string]string{"action": "cancel_requested", "task_id": taskID})
}

func (h *handler) deleteTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	if !validTaskID(taskID) {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "task_id 无效")
		return
	}
	if h.store == nil {
		h.internalError(w, r, errors.New("monitor task store is nil"))
		return
	}
	if err := h.store.AdminDelete(r.Context(), taskID); err != nil {
		h.writeTaskActionError(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), "管理员已删除任务", "task_id", taskID, "stage", "admin_delete")
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) writeTaskActionError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrTaskNotFound):
		h.writeError(w, http.StatusNotFound, "task_not_found", "任务不存在")
	case errors.Is(err, domain.ErrTaskNotOperable), errors.Is(err, domain.ErrStateConflict):
		h.writeError(w, http.StatusConflict, "task_not_operable", "任务当前状态不可操作")
	default:
		h.internalError(w, r, err)
	}
}

func taskPhase(item domain.AdminTaskSummary) string {
	if item.RetryCount > 0 && (item.InternalStatus == domain.StatusDispatching || item.InternalStatus == domain.StatusRunning || item.InternalStatus == domain.StatusReconciling) {
		return "retrying"
	}
	switch item.InternalStatus {
	case domain.StatusQueuedOpen, domain.StatusQueuedLocked:
		return "queued"
	case domain.StatusReconciling:
		return "recovering"
	default:
		return string(item.InternalStatus)
	}
}

func (h *handler) publicVideoURL(item domain.AdminTaskSummary) (*string, error) {
	if item.Status != domain.V2Succeeded {
		return nil, nil
	}
	if item.ResultArtifactID != "" {
		if h.artifactURLs == nil {
			return nil, errors.New("artifact URL signer is not configured")
		}
		value, err := h.artifactURLs.SignURL(item.ResultArtifactID, item.APIKeyID)
		if err != nil {
			return nil, err
		}
		return &value, nil
	}
	value, ok := artifactservice.LegacyPublicURL(item.ResultPublicURL)
	if !ok {
		return nil, nil
	}
	return &value, nil
}

func validTaskID(taskID string) bool { return len(taskID) >= 1 && len(taskID) <= 64 }

func taskDurationSeconds(item domain.AdminTaskSummary, now time.Time) *int64 {
	if item.StartedAt.IsZero() {
		return nil
	}
	end := item.FinishedAt
	if end.IsZero() {
		if item.Status != domain.V2Running {
			return nil
		}
		end = now
	}
	seconds := int64(end.Sub(item.StartedAt) / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	return &seconds
}

func (h *handler) noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (h *handler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "管理接口处理失败", "error_type", fmt.Sprintf("%T", err))
	h.writeError(w, http.StatusInternalServerError, "server_error", "服务内部错误")
}

func (h *handler) writeError(w http.ResponseWriter, status int, kind, message string) {
	h.writeJSON(w, status, map[string]any{"error": map[string]string{"type": kind, "message": message}})
}

func (h *handler) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeLoginRequest(data []byte) (loginRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return loginRequest{}, errors.New("login request must be an object")
	}
	var request loginRequest
	seenUsername, seenPassword := false, false
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return loginRequest{}, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return loginRequest{}, errors.New("invalid login request key")
		}
		switch key {
		case "username":
			if seenUsername {
				return loginRequest{}, errors.New("invalid username field")
			}
			value, err := loginStringToken(decoder)
			if err != nil {
				return loginRequest{}, errors.New("invalid username field")
			}
			request.Username = value
			seenUsername = true
		case "password":
			if seenPassword {
				return loginRequest{}, errors.New("invalid password field")
			}
			value, err := loginStringToken(decoder)
			if err != nil {
				return loginRequest{}, errors.New("invalid password field")
			}
			request.Password = value
			seenPassword = true
		default:
			return loginRequest{}, errors.New("unknown login request field")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return loginRequest{}, err
	}
	if !seenUsername || !seenPassword {
		return loginRequest{}, errors.New("missing login request field")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return loginRequest{}, errors.New("extra JSON")
	}
	return request, nil
}

func loginStringToken(decoder *json.Decoder) (string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	value, ok := token.(string)
	if !ok {
		return "", errors.New("login field must be a string")
	}
	return value, nil
}

func parsePositiveInt(value string, fallback int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, errors.New("invalid positive integer")
	}
	return parsed, nil
}

func validStatus(status domain.V2Status) bool {
	return status == domain.V2Queued || status == domain.V2Running || status == domain.V2Succeeded || status == domain.V2Failed || status == domain.V2Cancelled
}

func unixTime(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}
