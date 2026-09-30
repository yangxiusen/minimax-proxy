package v2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"minimax-h3-tc/internal/callback"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/inputobject"
	"minimax-h3-tc/internal/inputspool"
	"minimax-h3-tc/internal/logsafe"
	"minimax-h3-tc/internal/protocol"
	"net/http"
	"strings"
)

type idempotentRecordFinder interface {
	FindIdempotentRecord(context.Context, string, string) (domain.Task, error)
}

func requestDigest(request domain.GenerationRequest, protocolID string) (string, error) {
	if protocolID == domain.ProtocolTK2SD {
		request.Resolution = ""
		request.Ratio = ""
		request.AIGCWatermark = nil
	}
	data, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func (h *handler) replayRouted(w http.ResponseWriter, r *http.Request, request domain.GenerationRequest, keyHash string) bool {
	if keyHash == "" {
		return false
	}
	finder, ok := h.store.(idempotentRecordFinder)
	if !ok {
		return false
	}
	existing, err := finder.FindIdempotentRecord(r.Context(), owner(r.Context()), keyHash)
	if errors.Is(err, domain.ErrTaskNotFound) {
		return false
	}
	if err != nil {
		h.internalError(w, r, err)
		return true
	}
	var normalized domain.GenerationRequest
	normalizer := existing.RequestNormalizer
	if normalizer == "" {
		normalizer = "legacy-h3-v1"
	}
	if normalizer == domain.ProtocolTK2SD {
		var snapshot domain.RouteSnapshot
		if json.Unmarshal([]byte(existing.RoutingSnapshotJSON), &snapshot) != nil {
			h.internalError(w, r, errors.New("任务快照无效"))
			return true
		}
		cap := domain.ModelCapability{SchemaVersion: 1, Modes: []domain.ModeCapability{{Scenario: snapshot.Requirements.Scenario, Durations: []int{snapshot.Requirements.Duration}, Roles: snapshot.Requirements.Roles}}, ResolutionPolicy: "ignored", MaxMedia: 12}
		n, e := protocol.Normalize(domain.ProtocolTK2SD, request, cap)
		err = e
		normalized = n.Request
	} else {
		if strings.EqualFold(strings.TrimSpace(request.Resolution), existing.Resolution) {
			request.Resolution = existing.Resolution
		}
		if normalizer == domain.ProtocolOfficial && request.Model != "MiniMax-H3" {
			n, e := protocol.Normalize(normalizer, request, protocol.BuiltinCapability(normalizer))
			normalized = n.Request
			err = e
		} else {
			v, e := ValidateCreate(request, nil)
			normalized = v.CreateRequest
			err = e
		}
	}
	if err != nil {
		h.storeError(w, r, domain.ErrIdempotencyConflict)
		return true
	}
	hash, err := requestDigest(normalized, normalizer)
	if err != nil {
		h.internalError(w, r, err)
		return true
	}
	if hash != existing.RequestHash {
		h.storeError(w, r, domain.ErrIdempotencyConflict)
		return true
	}
	h.writeCreateResult(w, r, existing)
	return true
}

func (h *handler) createRouted(w http.ResponseWriter, r *http.Request, request domain.GenerationRequest) {
	keyHash := ""
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		if !validIdempotencyKey(key) {
			h.writeError(w, r, 400, "bad_request_error", "Idempotency-Key 无效 (2024)")
			return
		}
		sum := sha256.Sum256([]byte(key))
		keyHash = hex.EncodeToString(sum[:])
	}
	if h.replayRouted(w, r, request, keyHash) {
		return
	}
	decision, err := h.routing.Resolve(r.Context(), request)
	if err != nil {
		h.routeError(w, r, err)
		return
	}
	n := decision.Normalized
	durationSource := "request"
	if !request.DurationPresent {
		durationSource = "default"
	}
	attributes := []any{"stage", "proxy_create", "event", "normalized", "request_id", requestID(r.Context()), "protocol", decision.Snapshot.ProtocolVersion, "duration_provided", request.DurationPresent, "duration_source", durationSource, "effective_duration", n.Request.Duration, "request", logsafe.Generation(n.Request, requestSecrets(r)...)}
	if decision.Snapshot.ProtocolVersion == domain.ProtocolTK2SD && !request.DurationPresent {
		h.logger.WarnContext(r.Context(), "请求未提供 duration，使用协议默认时长", attributes...)
	} else {
		h.logger.InfoContext(r.Context(), "模型请求参数已按协议校验", attributes...)
	}
	validated := ValidatedRequest{CreateRequest: n.Request, Scenario: n.Scenario, Prompt: n.Prompt, InputImageCount: n.InputImageCount}
	var activeProfile domain.ModelRequestProfile
	if decision.Snapshot.ParameterPlan == "h3_profile" {
		if h.activeProfiles != nil {
			activeProfile, err = h.activeProfiles.GetProfileByResolution(r.Context(), validated.Resolution)
			if err != nil {
				h.writeError(w, r, 400, "bad_request_error", "请求分辨率不存在或未配置 (2013)")
				return
			}
			var config domain.ProfileConfig
			if json.Unmarshal([]byte(activeProfile.ConfigJSON), &config) != nil {
				h.writeError(w, r, 503, "profile_unavailable_error", "模型参数配置暂不可用")
				return
			}
			dimension, ok := config.Ratios[validated.Ratio]
			if !ok {
				h.writeError(w, r, 400, "bad_request_error", "ratio 未配置尺寸映射 (2013)")
				return
			}
			validated.Resolution = activeProfile.Resolution
			validated.Width = dimension.BaseWidth
			validated.Height = dimension.BaseHeight
		} else {
			validated, err = ValidateCreate(n.Request, h.profiles)
			if err != nil {
				h.writeError(w, r, 400, "bad_request_error", err.Error())
				return
			}
		}
	}
	decision.Snapshot.Requirements.Resolution = validated.Resolution
	hash, err := requestDigest(validated.CreateRequest, decision.Snapshot.ProtocolVersion)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	var callbackTarget *callback.PreparedTarget
	if validated.CallbackURL != nil {
		if h.callbackService == nil || h.callbackCipher == nil {
			h.writeError(w, r, 503, "callback_unavailable_error", "callback 服务未配置")
			return
		}
		callbackTarget, err = h.callbackService.PrepareTarget(r.Context(), validated.CallbackURL, h.callbackCipher)
		if err != nil {
			h.writeError(w, r, 400, "callback_url_error", "callback URL challenge 失败")
			return
		}
	}
	id, err := newNumericID()
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	snapshot, _ := json.Marshal(decision.Snapshot)
	newTask := domain.NewTask{TaskID: id, APIKeyID: owner(r.Context()), Model: validated.Model, Scenario: validated.Scenario, Resolution: validated.Resolution, Duration: validated.Duration, Ratio: validated.Ratio, InputImageCount: validated.InputImageCount, RequestHash: hash, ProtocolVersion: decision.Snapshot.ProtocolVersion, RouteState: "ready", RoutingSnapshotJSON: string(snapshot), RoutingRevision: decision.Snapshot.RoutingRevision, RequestNormalizer: decision.Snapshot.NormalizerVersion}
	if activeProfile.ConfigJSON != "" {
		newTask.ConfigSnapshotJSON = activeProfile.ConfigJSON
		newTask.ConfigHash = activeProfile.ConfigHash
		newTask.Stages, err = freezeStages(id, validated, activeProfile.ConfigJSON)
		if err != nil {
			h.internalError(w, r, err)
			return
		}
	}
	if callbackTarget != nil {
		newTask.CallbackURLCiphertext = callbackTarget.Ciphertext
		newTask.CallbackURLNonce = callbackTarget.Nonce
		delivery, e := callback.NewDelivery("event_"+randomHex(16), id, "queued", 1, nil)
		if e != nil {
			h.internalError(w, r, e)
			return
		}
		newTask.CallbackDeliveryID = delivery.ID
		newTask.CallbackRequestBody = string(delivery.Body)
		newTask.CallbackRequestBodyHash = delivery.BodyHash
	}
	persisted := validated.CreateRequest
	persisted.CallbackURL = nil
	payload, err := json.Marshal(persisted)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	prepared, err := h.prepareRoutedInputs(r.Context(), id, owner(r.Context()), hash, payload)
	if err != nil {
		h.logger.WarnContext(r.Context(), "输入素材准备失败", "request_id", requestID(r.Context()), "task_id", id, "error_code", "input_preparation_failed", "error_reason", logsafe.Error(err))
		if errors.Is(err, inputobject.ErrNotReady) {
			h.writeError(w, r, 503, "object_storage_not_ready", "对象存储暂不可用")
		} else {
			h.writeError(w, r, 502, "input_preparation_failed", "输入素材准备失败")
		}
		return
	}
	newTask.RequestJSON = string(prepared.JSON)
	newTask.InputSpoolFiles = prepared.Files
	task, err := h.store.Create(r.Context(), newTask, keyHash, h.available)
	if err != nil {
		_ = prepared.Cleanup()
		h.routeError(w, r, err)
		return
	}
	if task.TaskID != id {
		_ = prepared.Cleanup()
	}
	if h.wake != nil {
		h.wake()
	}
	h.logger.InfoContext(r.Context(), "视频生成任务已按模型协议入队", "task_id", task.TaskID, "model", task.Model, "protocol", task.ProtocolVersion)
	h.writeCreateResult(w, r, task)
}
func (h *handler) prepareRoutedInputs(ctx context.Context, id, ownerID, hash string, payload []byte) (inputspool.PreparedRequest, error) {
	if h.inputObjects != nil {
		if finder, ok := h.store.(reusableInputObjectFinder); ok {
			_, files, err := finder.FindReusableInputObjects(ctx, ownerID, hash)
			if err == nil && len(files) > 0 {
				var request domain.GenerationRequest
				if err := json.Unmarshal(payload, &request); err != nil {
					return inputspool.PreparedRequest{}, err
				}
				for _, file := range files {
					if file.ContentIndex < 0 || file.ContentIndex >= len(request.Content) || file.ObjectURL == "" {
						return inputspool.PreparedRequest{}, errors.New("可复用输入元数据无效")
					}
					item := &request.Content[file.ContentIndex]
					if item.Type != file.ContentType || item.Role != file.Role || item.Media() == nil {
						return inputspool.PreparedRequest{}, errors.New("可复用输入与当前请求不匹配")
					}
					item.Media().URL = file.ObjectURL
				}
				data, err := json.Marshal(request)
				if err != nil {
					return inputspool.PreparedRequest{}, err
				}
				if h.logger != nil {
					h.logger.InfoContext(ctx, "输入素材对象存储已复用", "task_id", id, "api_key_id", ownerID, "input_count", len(files))
				}
				return inputspool.PreparedRequest{JSON: data, Files: cloneReusableInputObjects(id, files)}, nil
			}
			if err != nil && !errors.Is(err, domain.ErrTaskNotFound) {
				return inputspool.PreparedRequest{}, err
			}
		}
		p, err := h.inputObjects.Prepare(ctx, inputObjectNamespace(ownerID, hash), payload)
		if err != nil {
			return inputspool.PreparedRequest{}, err
		}
		if p.Enabled {
			for i := range p.Files {
				p.Files[i].TaskID = id
				p.Files[i].ID = objectInputID(id, p.Files[i])
			}
			return inputspool.PreparedRequest{JSON: p.JSON, Files: p.Files}, nil
		}
	}
	if h.inputSpooler != nil {
		return h.inputSpooler.PrepareRequest(ctx, id, payload)
	}
	return inputspool.PreparedRequest{JSON: payload}, nil
}
func (h *handler) routeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrUnsupportedModel):
		h.writeError(w, r, 400, "unsupported_model", domain.ErrUnsupportedModel.Error())
	case errors.Is(err, domain.ErrModelInput):
		h.writeError(w, r, 400, "model_input_unsupported", err.Error())
	case errors.Is(err, domain.ErrRouteAmbiguous):
		h.writeError(w, r, 409, "model_route_ambiguous", domain.ErrRouteAmbiguous.Error())
	case errors.Is(err, domain.ErrRouteChanged):
		h.writeError(w, r, 409, "model_route_changed", domain.ErrRouteChanged.Error())
	case errors.Is(err, domain.ErrRouteUnavailable):
		h.writeError(w, r, 503, "model_route_unavailable", domain.ErrRouteUnavailable.Error())
	default:
		h.storeError(w, r, err)
	}
}
