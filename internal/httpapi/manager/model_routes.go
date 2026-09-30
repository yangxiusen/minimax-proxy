package manager

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	"minimax-h3-tc/internal/domain"
	monitorcache "minimax-h3-tc/internal/monitor"
	"minimax-h3-tc/internal/protocol"
)

type ModelRouteStore interface {
	GetModelRoute(context.Context, string) (domain.ModelRoute, int64, error)
	ListModelRoutes(context.Context) ([]domain.ModelRoute, int64, error)
	BindModelRoute(context.Context, string, string, string, int64, int64) (domain.ModelRoute, int64, error)
}
type LegacyTaskRouteStore interface {
	BindLegacyTaskRoute(context.Context, string, string, int64, int64) (domain.Task, error)
}

type taskRoutingDTO struct {
	Model              string `json:"model"`
	ProtocolVersion    string `json:"protocol_version"`
	RouteState         string `json:"route_state"`
	SelectionMode      string `json:"selection_mode"`
	RouteVersion       int64  `json:"route_version"`
	NormalizerVersion  string `json:"normalizer_version"`
	CanBindLegacyRoute bool   `json:"can_bind_legacy_route"`
}

func taskRoutingSummary(task domain.Task) *taskRoutingDTO {
	var snapshot domain.RouteSnapshot
	_ = json.Unmarshal([]byte(task.RoutingSnapshotJSON), &snapshot)
	canBind := task.RouteState == "awaiting_route" && (task.Status == domain.StatusQueuedOpen || task.Status == domain.StatusQueuedLocked) && task.UpstreamJobID == "" && task.GradioEventID == "" && !task.OfficialSubmissionBaselineSaved && task.AttemptStartedAt.IsZero()
	return &taskRoutingDTO{Model: task.Model, ProtocolVersion: task.ProtocolVersion, RouteState: task.RouteState, SelectionMode: snapshot.SelectionMode, RouteVersion: snapshot.RouteVersion, NormalizerVersion: task.RequestNormalizer, CanBindLegacyRoute: canBind}
}

type managerActualMetadata struct {
	Duration   *float64 `json:"duration"`
	Ratio      *string  `json:"ratio"`
	Resolution *string  `json:"resolution"`
	Width      *int     `json:"width"`
	Height     *int     `json:"height"`
}

type managerRemoteDTO struct {
	Phase                  string `json:"phase"`
	NodeID                 string `json:"node_id"`
	CancelState            string `json:"cancel_state"`
	ReconciliationRequired bool   `json:"reconciliation_required"`
	LastErrorCode          string `json:"last_error_code"`
	UploadedMedia          *int   `json:"uploaded_media"`
	TotalMedia             *int   `json:"total_media"`
}

func (h *handler) loadManagerRemoteState(ctx context.Context, task domain.Task) (domain.Task, *managerRemoteDTO) {
	store, ok := h.store.(interface {
		GetRemoteRun(context.Context, string) (domain.RemoteRun, error)
	})
	if !ok {
		return task, nil
	}
	run, err := store.GetRemoteRun(ctx, task.TaskID)
	if err != nil {
		return task, nil
	}
	task.RemotePhase, task.RemoteUpstreamStatus, task.RemoteCancelState = run.Phase, run.UpstreamStatus, run.CancelState
	summary := &managerRemoteDTO{Phase: run.Phase, NodeID: run.NodeID, CancelState: run.CancelState, ReconciliationRequired: run.ReconciliationRequired, LastErrorCode: run.LastErrorCode}
	var input domain.GenerationRequest
	if json.Unmarshal([]byte(task.RequestJSON), &input) != nil {
		return task, summary
	}
	total, uploaded := 0, 0
	assets, canCount := h.store.(interface {
		GetRemoteAsset(context.Context, string, string, int) (domain.NodeAsset, error)
	})
	for index, item := range input.Content {
		if item.Type == "text" {
			continue
		}
		total++
		if canCount {
			_, err := assets.GetRemoteAsset(ctx, task.TaskID, run.NodeID, index)
			if err == nil {
				uploaded++
			} else if !errors.Is(err, domain.ErrTaskNotFound) {
				canCount = false
			}
		}
	}
	summary.TotalMedia = &total
	if canCount {
		summary.UploadedMedia = &uploaded
	}
	return task, summary
}

func actualManagerMetadata(task domain.Task) managerActualMetadata {
	var metadata managerActualMetadata
	if task.ResultMetadataJSON == "" || json.Unmarshal([]byte(task.ResultMetadataJSON), &metadata) != nil {
		return managerActualMetadata{}
	}
	if metadata.Duration != nil && (*metadata.Duration <= 0 || math.IsNaN(*metadata.Duration) || math.IsInf(*metadata.Duration, 0)) {
		metadata.Duration = nil
	}
	if metadata.Width != nil && *metadata.Width <= 0 {
		metadata.Width = nil
	}
	if metadata.Height != nil && *metadata.Height <= 0 {
		metadata.Height = nil
	}
	return metadata
}

func (h *handler) presentationTask(ctx context.Context, id string) (domain.Task, bool, error) {
	store, ok := h.store.(executionTaskStore)
	if !ok {
		return domain.Task{}, false, nil
	}
	task, err := store.GetTaskForExecution(ctx, id)
	return task, true, err
}

func (h *handler) remoteManagerResult(ctx context.Context, task domain.Task) (domain.Task, *string, error) {
	if task.RemotePhase == "" {
		task, _ = h.loadManagerRemoteState(ctx, task)
	}
	if task.Status != domain.StatusSucceeded {
		return task, nil, nil
	}
	if h.remoteResults == nil {
		return task, nil, domain.ErrResultRefreshUnavailable
	}
	refreshed, err := h.remoteResults.Refresh(ctx, task)
	if refreshed.TaskID == task.TaskID && refreshed.ProtocolVersion == task.ProtocolVersion {
		if refreshed.RemotePhase == "" {
			refreshed.RemotePhase, refreshed.RemoteUpstreamStatus, refreshed.RemoteCancelState = task.RemotePhase, task.RemoteUpstreamStatus, task.RemoteCancelState
		}
		task = refreshed
	}
	if err != nil || task.ResultPublicURL == "" {
		return task, nil, domain.ErrResultRefreshUnavailable
	}
	return task, &task.ResultPublicURL, nil
}

func validTaskRoutingFilters(query url.Values) bool {
	if model := query.Get("model"); model != "" && !domain.ValidModelID(model) {
		return false
	}
	if p := query.Get("protocol_version"); p != "" {
		if _, ok := protocol.Lookup(p); !ok && p != "legacy_unknown" {
			return false
		}
	}
	return slices.Contains([]string{"", "ready", "awaiting_route", "migration_blocked", "legacy_unknown"}, query.Get("route_state"))
}

func remoteManagerPhase(task domain.Task) string {
	if task.Status == domain.StatusSucceeded || task.Status == domain.StatusFailed || task.Status == domain.StatusCancelled {
		return string(task.Status)
	}
	if task.RemoteCancelState == "requested" {
		return "cancelling"
	}
	switch task.RemotePhase {
	case domain.RemotePreparing, domain.RemotePrepared:
		return "preparing_inputs"
	case domain.RemoteSubmitIntent:
		return "submit_uncertain"
	case domain.RemoteSubmitted:
		if task.RemoteUpstreamStatus == "queued" {
			return "upstream_queued"
		}
		return "upstream_running"
	case domain.RemoteTerminal:
		if task.DeliveryRequired {
			return "result_delivery"
		}
	}
	return string(task.Status)
}

type modelRouteDTO struct {
	Model               string                  `json:"model"`
	ProtocolVersion     *string                 `json:"protocol_version"`
	SelectionMode       string                  `json:"selection_mode"`
	Version             int64                   `json:"version"`
	State               string                  `json:"state"`
	WaitReason          string                  `json:"wait_reason"`
	Candidates          []domain.RouteCandidate `json:"candidates"`
	LegacyAwaitingTasks int                     `json:"legacy_awaiting_tasks"`
}

func (h *handler) listModelRoutes(w http.ResponseWriter, r *http.Request) {
	if !h.modelQuery(w, r, "q", "state", "page_num", "page_size") || !h.noModelBody(w, r) {
		return
	}
	q, page, size, ok := h.modelPagination(w, r)
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	if !slices.Contains([]string{"", "ready", "ambiguous", "unavailable", "pending"}, state) {
		h.writeError(w, 400, "bad_request_error", "state 无效")
		return
	}
	if h.routeStore == nil {
		h.writeError(w, 503, "model_routes_unavailable", "模型线路尚未配置")
		return
	}
	routes, revision, err := h.routeStore.ListModelRoutes(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	items := make([]modelRouteDTO, 0, len(routes))
	catalogs := map[string]domain.ModelCatalog{}
	for _, route := range routes {
		if !strings.Contains(route.Model, q) {
			continue
		}
		dto, err := h.routeDTO(r.Context(), route, catalogs)
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		if state == "" || dto.State == state {
			items = append(items, dto)
		}
	}
	sort.Slice(items, func(a, b int) bool { return items[a].Model < items[b].Model })
	total := len(items)
	start, end := pageBounds(total, page, size)
	h.writeJSON(w, 200, map[string]any{"routing_revision": revision, "total": total, "page_num": page, "page_size": size, "items": items[start:end]})
}

func (h *handler) routeDTO(ctx context.Context, route domain.ModelRoute, catalogs map[string]domain.ModelCatalog) (modelRouteDTO, error) {
	dto := modelRouteDTO{Model: route.Model, SelectionMode: route.SelectionMode, Version: route.Version, LegacyAwaitingTasks: route.LegacyAwaitingTasks, Candidates: []domain.RouteCandidate{}}
	if route.ProtocolVersion != "" {
		p := route.ProtocolVersion
		dto.ProtocolVersion = &p
	}
	for _, candidate := range route.Candidates {
		candidate.NodeIDs = slices.Clone(candidate.NodeIDs)
		candidate.HealthyNodes = 0
		for _, id := range candidate.NodeIDs {
			healthy := false
			if h.routing != nil && h.routing.Healthy != nil {
				healthy = h.routing.Healthy(id)
			} else if node, ok := h.cache.Get(id); ok {
				healthy = node.Health == monitorcache.HealthHealthy && !node.Disabled && !node.SchedulingBlocked && !node.Applying
			}
			if h.catalogs != nil {
				catalog, ok := catalogs[id]
				if !ok {
					var err error
					catalog, err = h.catalogs.GetModelCatalog(ctx, id)
					if err != nil {
						return dto, err
					}
					catalogs[id] = catalog
				}
				if catalog.Source == "discovered" && (catalog.LastSuccessAt == 0 || catalog.ValidUntil <= h.now().Unix()) {
					healthy = false
				}
			}
			if healthy {
				candidate.HealthyNodes++
			}
		}
		dto.Candidates = append(dto.Candidates, candidate)
	}
	dto.State = "unavailable"
	dto.WaitReason = "no_protocol_nodes"
	if dto.ProtocolVersion == nil {
		dto.State = "pending"
		dto.WaitReason = "route_not_selected"
		if len(dto.Candidates) > 1 {
			dto.State = "ambiguous"
			dto.WaitReason = "multiple_protocols"
		}
		return dto, nil
	}
	for _, candidate := range dto.Candidates {
		if route.SelectionMode != "manual" && candidate.ProtocolVersion != route.ProtocolVersion {
			dto.State = "ambiguous"
			dto.WaitReason = "multiple_protocols"
			return dto, nil
		}
		if candidate.ProtocolVersion == route.ProtocolVersion {
			dto.WaitReason = "no_healthy_nodes"
			if candidate.HealthyNodes > 0 {
				dto.State = "ready"
				dto.WaitReason = ""
			}
		}
	}
	return dto, nil
}

func (h *handler) bindModelRoute(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Model           string `json:"model"`
		ProtocolVersion string `json:"protocol_version"`
		Version         int64  `json:"version"`
		RoutingRevision int64  `json:"routing_revision"`
	}
	if !h.modelQuery(w, r) || !h.readModelJSON(w, r, &request) {
		return
	}
	if !domain.ValidModelID(request.Model) || request.Version < 1 || request.RoutingRevision < 1 {
		h.writeError(w, 400, "bad_request_error", "模型或版本无效")
		return
	}
	if _, ok := protocol.Lookup(request.ProtocolVersion); !ok {
		h.writeError(w, 400, "unsupported_protocol", "不支持该协议")
		return
	}
	if h.routeStore == nil {
		h.writeError(w, 503, "model_routes_unavailable", "模型线路尚未配置")
		return
	}
	current, revision, err := h.routeStore.GetModelRoute(r.Context(), request.Model)
	if err != nil {
		if errors.Is(err, domain.ErrUnsupportedModel) {
			h.writeError(w, 404, "model_route_not_found", "模型线路不存在")
		} else {
			h.internalError(w, r, err)
		}
		return
	}
	if revision != request.RoutingRevision {
		h.writeError(w, 409, "model_catalog_changed", "模型目录已更新，请刷新后重试")
		return
	}
	if current.Version != request.Version {
		h.writeError(w, 409, "model_route_version_conflict", "模型线路已更新，请刷新后重试")
		return
	}
	found := false
	for _, candidate := range current.Candidates {
		if candidate.ProtocolVersion == request.ProtocolVersion {
			found = true
		}
	}
	if !found {
		h.writeError(w, 400, "unsupported_model", "该模型未开放此协议")
		return
	}
	result, revision, err := h.routeStore.BindModelRoute(r.Context(), request.Model, request.ProtocolVersion, h.adminUsername, request.Version, request.RoutingRevision)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrRouteChanged):
			h.writeError(w, 409, "model_route_version_conflict", "模型线路已更新，请刷新后重试")
		case errors.Is(err, domain.ErrCatalogConflict):
			h.writeError(w, 409, "model_catalog_changed", "模型目录已更新，请刷新后重试")
		case errors.Is(err, domain.ErrRouteUnavailable):
			h.writeError(w, 400, "unsupported_model", "该模型未开放此协议")
		case errors.Is(err, domain.ErrUnsupportedModel):
			h.writeError(w, 404, "model_route_not_found", "模型线路不存在")
		default:
			h.internalError(w, r, err)
		}
		return
	}
	dto, err := h.routeDTO(r.Context(), result, map[string]domain.ModelCatalog{})
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.wakeRegistry()
	h.logger.InfoContext(r.Context(), "管理员已绑定模型线路", "model", result.Model, "protocol", result.ProtocolVersion, "revision", revision)
	h.writeJSON(w, 200, struct {
		modelRouteDTO
		RoutingRevision int64 `json:"routing_revision"`
	}{dto, revision})
}

func (h *handler) bindLegacyTaskRoute(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ProtocolVersion string `json:"protocol_version"`
		Version         int64  `json:"version"`
		RoutingRevision int64  `json:"routing_revision"`
	}
	if !h.modelQuery(w, r) || !h.readModelJSON(w, r, &request) {
		return
	}
	id := r.PathValue("task_id")
	if !validTaskID(id) || request.Version < 1 || request.RoutingRevision < 1 {
		h.writeError(w, 400, "bad_request_error", "任务或版本无效")
		return
	}
	if _, ok := protocol.Lookup(request.ProtocolVersion); !ok {
		h.writeError(w, 400, "task_incompatible_with_protocol", "任务不支持该协议")
		return
	}
	var binder LegacyTaskRouteStore
	for _, candidate := range []any{h.store, h.nodes, h.routeStore} {
		if found, ok := candidate.(LegacyTaskRouteStore); ok {
			binder = found
			break
		}
	}
	if binder == nil || h.store == nil {
		h.writeError(w, 503, "task_routing_unavailable", "历史任务路由尚未配置")
		return
	}
	detail, err := h.store.GetAdminTaskDetail(r.Context(), id)
	if err != nil {
		h.writeTaskActionError(w, r, err)
		return
	}
	task := detail.Task
	if task.RouteState == "migration_blocked" {
		h.writeError(w, 409, "migration_reconciliation_required", "任务归属需要人工核实")
		return
	}
	if task.Version != request.Version {
		h.writeError(w, 409, "task_version_conflict", "任务已变化，请刷新后重试")
		return
	}
	if task.RouteState != "awaiting_route" || (task.Status != domain.StatusQueuedOpen && task.Status != domain.StatusQueuedLocked) || task.UpstreamJobID != "" || task.GradioEventID != "" || task.OfficialSubmissionBaselineSaved || !task.AttemptStartedAt.IsZero() {
		h.writeError(w, 409, "task_route_not_editable", "仅未提交的待绑定历史任务可修改")
		return
	}
	// 完整历史输入校验及事务内提交证据复核由 Store.BindLegacyTaskRoute 执行。
	result, err := binder.BindLegacyTaskRoute(r.Context(), id, request.ProtocolVersion, request.Version, request.RoutingRevision)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrTaskNotFound):
			h.writeError(w, 404, "task_not_found", "任务不存在")
		case errors.Is(err, domain.ErrStateConflict):
			latest, readErr := h.store.GetAdminTaskDetail(r.Context(), id)
			if readErr == nil && latest.Task.Version == request.Version {
				h.writeError(w, 409, "task_route_not_editable", "任务提交证据或状态不允许修改线路")
			} else {
				h.writeError(w, 409, "task_version_conflict", "任务已变化，请刷新后重试")
			}
		case errors.Is(err, domain.ErrTaskNotOperable):
			h.writeError(w, 409, "task_route_not_editable", "任务线路不可修改")
		case errors.Is(err, domain.ErrCatalogConflict), errors.Is(err, domain.ErrRouteChanged):
			h.writeError(w, 409, "catalog_changed", "模型目录已变化，请刷新后重试")
		case errors.Is(err, domain.ErrModelInput), errors.Is(err, domain.ErrUnsupportedModel), errors.Is(err, domain.ErrRouteUnavailable):
			h.writeError(w, 400, "task_incompatible_with_protocol", "历史任务输入不兼容所选协议")
		default:
			h.internalError(w, r, err)
		}
		return
	}
	h.wakeRegistry()
	h.writeJSON(w, 200, map[string]any{"task_id": result.TaskID, "protocol_version": result.ProtocolVersion, "route_state": result.RouteState, "version": result.Version})
}
