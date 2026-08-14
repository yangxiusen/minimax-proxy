package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"minimax-h3-tc/internal/store/sqlite"
)

const cleanupBodyLimit = 64 << 10

type CleanupStore interface {
	PreviewArtifactCleanup(context.Context, int, string) (sqlite.CleanupPreview, error)
	ConfirmArtifactCleanup(context.Context, string, string) (sqlite.CleanupJobDetail, error)
	GetArtifactCleanup(context.Context, string) (sqlite.CleanupJobDetail, []sqlite.CleanupNodeProgress, error)
	ListArtifactCleanupItems(context.Context, string, string, string, int) ([]sqlite.CleanupItemDetail, error)
	RetryArtifactCleanup(context.Context, string, []string) (sqlite.CleanupJobDetail, error)
}

type cleanupHandlers struct {
	store       CleanupStore
	requestedBy string
	wake        func()
	now         func() time.Time
	writeJSON   func(http.ResponseWriter, int, any)
	writeError  func(http.ResponseWriter, int, string, string)
}

func registerCleanupRoutes(mux *http.ServeMux, authenticate func(http.Handler) http.Handler, store CleanupStore, requestedBy string, wake func(), now func() time.Time, writeJSON func(http.ResponseWriter, int, any), writeError func(http.ResponseWriter, int, string, string)) {
	if store == nil {
		return
	}
	h := cleanupHandlers{store: store, requestedBy: requestedBy, wake: wake, now: now, writeJSON: writeJSON, writeError: writeError}
	mux.Handle("POST /manager/api/artifact-cleanups/preview", authenticate(http.HandlerFunc(h.preview)))
	mux.Handle("POST /manager/api/artifact-cleanups", authenticate(http.HandlerFunc(h.confirm)))
	mux.Handle("GET /manager/api/artifact-cleanups/{cleanup_id}", authenticate(http.HandlerFunc(h.get)))
	mux.Handle("GET /manager/api/artifact-cleanups/{cleanup_id}/items", authenticate(http.HandlerFunc(h.items)))
	mux.Handle("POST /manager/api/artifact-cleanups/{cleanup_id}/retry", authenticate(http.HandlerFunc(h.retry)))
}

func (h cleanupHandlers) preview(w http.ResponseWriter, r *http.Request) {
	if !sameOriginMutation(r) {
		h.writeError(w, http.StatusForbidden, "csrf_error", "请求来源无效")
		return
	}
	var request struct {
		OlderThanDays        int      `json:"older_than_days"`
		Scope                string   `json:"scope"`
		IncludeIntermediate  *bool    `json:"include_intermediate"`
		IncludeTestOutputs   *bool    `json:"include_test_outputs"`
		IncludeLegacyOrphans bool     `json:"include_legacy_orphans"`
		NodeIDs              []string `json:"node_ids"`
	}
	if err := decodeManagerJSON(w, r, &request); err != nil {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "请求 JSON 无效")
		return
	}
	if request.OlderThanDays < 1 || request.OlderThanDays > 3650 || request.IncludeLegacyOrphans || len(request.NodeIDs) > 0 || request.Scope != "" && request.Scope != "managed_task_artifacts" {
		h.writeError(w, http.StatusBadRequest, "cleanup_scope_forbidden", "当前仅支持受管任务产物清理")
		return
	}
	preview, err := h.store.PreviewArtifactCleanup(r.Context(), request.OlderThanDays, h.requestedBy)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal_error", "清理预览失败")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"preview_token": preview.Token, "expires_at": millisTime(preview.ExpiresAt), "cutoff_at": millisTime(preview.CutoffAt),
		"candidate_count": preview.CandidateCount, "candidate_bytes": preview.CandidateBytes,
		"skipped_active_count": preview.SkippedActiveCount, "by_node": preview.ByNode, "warnings": []string{},
	})
}

func (h cleanupHandlers) confirm(w http.ResponseWriter, r *http.Request) {
	if !sameOriginMutation(r) {
		h.writeError(w, http.StatusForbidden, "csrf_error", "请求来源无效")
		return
	}
	var request struct {
		PreviewToken string `json:"preview_token"`
		Confirmation string `json:"confirmation"`
	}
	if err := decodeManagerJSON(w, r, &request); err != nil || request.PreviewToken == "" || request.Confirmation == "" {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "请求 JSON 无效")
		return
	}
	job, err := h.store.ConfirmArtifactCleanup(r.Context(), request.PreviewToken, request.Confirmation)
	if errors.Is(err, sqlite.ErrCleanupPreviewStale) {
		h.writeError(w, http.StatusConflict, "cleanup_preview_stale", "预览已过期或候选已变化，请重新预览")
		return
	}
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "cleanup_confirmation_invalid", "清理确认无效")
		return
	}
	if h.wake != nil {
		h.wake()
	}
	h.writeJSON(w, http.StatusAccepted, cleanupJobResponse(job, nil))
}

func (h cleanupHandlers) get(w http.ResponseWriter, r *http.Request) {
	job, nodes, err := h.store.GetArtifactCleanup(r.Context(), r.PathValue("cleanup_id"))
	if errors.Is(err, sqlite.ErrCleanupNotFound) {
		h.writeError(w, http.StatusNotFound, "not_found_error", "清理作业不存在")
		return
	}
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal_error", "清理作业查询失败")
		return
	}
	h.writeJSON(w, http.StatusOK, cleanupJobResponse(job, nodes))
}

func (h cleanupHandlers) items(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "50"))
	if err != nil || limit < 1 || limit > 100 || r.URL.Query().Get("cursor") != "" {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "查询参数无效")
		return
	}
	items, err := h.store.ListArtifactCleanupItems(r.Context(), r.PathValue("cleanup_id"), r.URL.Query().Get("status"), r.URL.Query().Get("node_id"), limit)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal_error", "清理明细查询失败")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": ""})
}

func (h cleanupHandlers) retry(w http.ResponseWriter, r *http.Request) {
	if !sameOriginMutation(r) {
		h.writeError(w, http.StatusForbidden, "csrf_error", "请求来源无效")
		return
	}
	var request struct {
		NodeIDs []string `json:"node_ids"`
	}
	if err := decodeManagerJSON(w, r, &request); err != nil {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "请求 JSON 无效")
		return
	}
	job, err := h.store.RetryArtifactCleanup(r.Context(), r.PathValue("cleanup_id"), request.NodeIDs)
	if errors.Is(err, sqlite.ErrCleanupNotRetryable) {
		h.writeError(w, http.StatusConflict, "cleanup_not_retryable", "没有可重试的失败项")
		return
	}
	if errors.Is(err, sqlite.ErrCleanupNotFound) {
		h.writeError(w, http.StatusNotFound, "not_found_error", "清理作业不存在")
		return
	}
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal_error", "清理重试失败")
		return
	}
	if h.wake != nil {
		h.wake()
	}
	h.writeJSON(w, http.StatusAccepted, cleanupJobResponse(job, nil))
}

func decodeManagerJSON(w http.ResponseWriter, r *http.Request, target any) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return errors.New("content type")
	}
	r.Body = http.MaxBytesReader(w, r.Body, cleanupBodyLimit)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("extra JSON")
	}
	return nil
}

func sameOriginMutation(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	origin := r.Header.Get("Origin")
	return origin == "" || origin == "http://"+r.Host || origin == "https://"+r.Host
}

func cleanupJobResponse(job sqlite.CleanupJobDetail, nodes []sqlite.CleanupNodeProgress) map[string]any {
	return map[string]any{
		"cleanup_id": job.ID, "reason": job.Reason, "status": job.Status, "scope": job.Scope,
		"older_than_days": job.OlderThanDays, "cutoff_at": millisTime(job.CutoffAt),
		"total_count": job.TotalCount, "succeeded_count": job.SucceededCount, "failed_count": job.FailedCount,
		"skipped_count": job.SkippedCount, "candidate_bytes": job.CandidateBytes, "deleted_bytes": job.DeletedBytes,
		"created_at": millisTime(job.CreatedAt), "started_at": millisTime(job.StartedAt), "finished_at": millisTime(job.FinishedAt),
		"updated_at": millisTime(job.UpdatedAt), "by_node": nodes, "error_summary": job.ErrorSummary,
	}
}

func millisTime(value int64) string {
	if value <= 0 {
		return ""
	}
	return time.UnixMilli(value).UTC().Format(time.RFC3339)
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
