package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/logsafe"
	"minimax-h3-tc/internal/upstream/tk2sd"
)

// 诊断只存受控说明，不将原始错误中的 URL、凭据或文件路径写到任务与日志。
type inputError struct {
	Code, Source, Stage, Reason, Message string
	ContentIndex                         int
	ContentType, Role                    string
	HTTPStatus                           int
	cause                                error
}

func (e *inputError) Error() string {
	label := map[string]string{"object_storage": "对象存储", "media_url": "素材 URL", "local_spool": "本地暂存", "data_uri": "内联素材", "upstream": "上游 tk2sd", "proxy_store": "Proxy 素材记录"}[e.Source]
	if label == "" {
		label = "Proxy"
	}
	message := label + "：" + e.Message
	if e.HTTPStatus != 0 {
		message += fmt.Sprintf("（HTTP %d）", e.HTTPStatus)
	}
	if e.ContentIndex >= 0 {
		message += fmt.Sprintf("；content[%d] %s", e.ContentIndex, e.ContentType)
	}
	return message
}

func (e *inputError) Unwrap() error { return e.cause }

func inputFailure(code, source, stage, reason, message string, cause error) *inputError {
	return &inputError{Code: code, Source: source, Stage: stage, Reason: reason, Message: message, ContentIndex: -1, cause: cause}
}

func invalidInput(stage, reason, message string) *inputError {
	code := "tk2sd_input_invalid"
	switch stage {
	case "request_validate":
		code = "tk2sd_input_request_invalid"
	case "source_validate", "source_open":
		code = "tk2sd_input_source_invalid"
	case "local_file":
		code = "tk2sd_input_local_file_invalid"
	case "media_detect", "media_validate", "asset_validate":
		code = "tk2sd_input_media_invalid"
	case "integrity_check":
		code = "tk2sd_input_integrity_mismatch"
	}
	return inputFailure(code, "", stage, reason, message, ErrInvalidInput)
}

func withInput(err error, index int, kind, role, source string) *inputError {
	var detail *inputError
	if !errors.As(err, &detail) {
		detail = inputFailure("tk2sd_input_prepare_unavailable", "proxy", "prepare", "dependency_failed", "素材准备依赖暂不可用", err)
	}
	switch kind {
	case "", "text", "image_url", "video_url", "audio_url":
	default:
		kind = "unknown"
	}
	switch role {
	case "", "first_frame", "reference_image", "reference_video", "reference_audio":
	default:
		role = "unknown"
	}
	copy := *detail
	copy.ContentIndex, copy.ContentType, copy.Role = index, kind, role
	if copy.Source == "" {
		copy.Source = source
	}
	return &copy
}

func uploadFailure(err error) *inputError {
	detail := inputFailure("tk2sd_asset_upload_unavailable", "upstream", "asset_upload", "request_failed", "上游素材上传暂不可用", err)
	var upstream *tk2sd.HTTPError
	if errors.As(err, &upstream) {
		detail.HTTPStatus = upstream.StatusCode
	}
	if rejected(err) {
		detail.Code, detail.Reason, detail.Message = "tk2sd_asset_upload_rejected", "http_rejected", "上游素材上传被拒绝"
	} else if errors.Is(err, tk2sd.ErrInvalidResponse) {
		detail.Code, detail.Reason, detail.Message = "tk2sd_asset_response_invalid", "invalid_response", "上游素材上传响应不符合协议"
	}
	return detail
}

func inputCauseClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	case errors.Is(err, os.ErrPermission):
		return "permission_denied"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "incomplete_source"
	case errors.Is(err, tk2sd.ErrUploadSource):
		return "upload_source_failed"
	case errors.Is(err, tk2sd.ErrInvalidResponse):
		return "invalid_upstream_response"
	case errors.Is(err, ErrInvalidInput):
		return "invalid_input"
	default:
		return "dependency_error"
	}
}

func (p *Processor) logInputFailure(ctx context.Context, err error) *inputError {
	detail := withInput(err, -1, "", "", "proxy")
	var original *inputError
	if errors.As(err, &original) {
		detail = original
	}
	if detail.Source == "" {
		copy := *detail
		copy.Source = "proxy"
		detail = &copy
	}
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	task, node := logsafe.TaskTrace(ctx)
	logger.WarnContext(ctx, "远程任务素材准备失败", "event", "input_failed", "task_id", task, "node_id", node,
		"protocol", domain.ProtocolTK2SD, "stage", detail.Stage, "source", detail.Source, "reason", detail.Reason,
		"content_index", detail.ContentIndex, "content_type", detail.ContentType, "role", detail.Role,
		"error_code", detail.Code, "error_message", detail.Error(), "http_status", detail.HTTPStatus,
		"retryable", !errors.Is(err, ErrInvalidInput) && !rejected(err), "cause_class", inputCauseClass(err))
	return detail
}

func (m *InputMaterializer) logInput(ctx context.Context, event, message string, attrs ...any) {
	logger := m.Logger
	if logger == nil {
		logger = slog.Default()
	}
	task, node := logsafe.TaskTrace(ctx)
	fields := []any{"event", event, "task_id", task, "node_id", node, "protocol", domain.ProtocolTK2SD}
	logger.InfoContext(ctx, message, append(fields, attrs...)...)
}
