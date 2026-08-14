package v2

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	artifactservice "minimax-h3-tc/internal/artifact"
	"minimax-h3-tc/internal/config"
)

type FileService interface {
	Open(context.Context, string, string, string, artifactservice.Authorization) (*artifactservice.Content, error)
}

type FilesDependencies struct {
	Service       FileService
	APIKeys       []config.APIKeyConfig
	Authenticator BearerAuthenticator
	Logger        *slog.Logger
}

type filesHandler struct {
	service       FileService
	keys          []authKey
	authenticator BearerAuthenticator
	logger        *slog.Logger
}

func NewFilesHandler(dependencies FilesDependencies) http.Handler {
	h := &filesHandler{service: dependencies.Service, authenticator: dependencies.Authenticator, logger: dependencies.Logger}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	for _, key := range dependencies.APIKeys {
		if key.Enabled {
			h.keys = append(h.keys, authKey{id: key.ID, digest: sha256.Sum256([]byte(key.Key))})
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/files/{artifact_id}/content", h.content)
	return mux
}

func (h *filesHandler) content(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		h.writeFileError(w, http.StatusServiceUnavailable, "file_unavailable_error", "结果文件服务不可用")
		return
	}
	artifactID := r.PathValue("artifact_id")
	if len(artifactID) < 1 || len(artifactID) > 128 || strings.ContainsAny(artifactID, "/\\") {
		h.writeFileError(w, http.StatusNotFound, "file_not_found_error", "结果文件不存在")
		return
	}
	auth, err := h.authorization(r)
	if err != nil {
		h.writeFileError(w, http.StatusUnauthorized, "authorized_error", "结果文件鉴权失败")
		return
	}
	content, err := h.service.Open(r.Context(), requestID(r.Context()), artifactID, r.Header.Get("Range"), auth)
	if err != nil {
		switch {
		case errors.Is(err, artifactservice.ErrUnauthorized):
			h.writeFileError(w, http.StatusUnauthorized, "authorized_error", "结果文件鉴权失败")
		case errors.Is(err, artifactservice.ErrNotFound):
			h.writeFileError(w, http.StatusNotFound, "file_not_found_error", "结果文件不存在")
		case errors.Is(err, artifactservice.ErrExpired):
			h.writeFileError(w, http.StatusGone, "file_expired_error", "结果文件已过期或缺失")
		case errors.Is(err, artifactservice.ErrIntegrity):
			h.writeFileError(w, http.StatusBadGateway, "file_integrity_error", "结果文件完整性校验失败")
		case errors.Is(err, artifactservice.ErrInvalidRange):
			h.writeFileError(w, http.StatusRequestedRangeNotSatisfiable, "invalid_range_error", "Range 无效")
		case errors.Is(err, artifactservice.ErrBusy):
			h.writeFileError(w, http.StatusTooManyRequests, "rate_limit_error", "结果文件下载并发已满")
		default:
			h.writeFileError(w, http.StatusServiceUnavailable, "file_unavailable_error", "结果文件暂不可用")
		}
		return
	}
	defer content.Body.Close()
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Disposition", `inline; filename="`+artifactID+`.mp4"`)
	if content.ContentType != "" {
		w.Header().Set("Content-Type", content.ContentType)
	}
	if content.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(content.ContentLength, 10))
	}
	if content.ContentRange != "" {
		w.Header().Set("Content-Range", content.ContentRange)
	}
	if content.ETag != "" {
		w.Header().Set("ETag", content.ETag)
	}
	w.WriteHeader(content.StatusCode)
	buffer := make([]byte, 64<<10)
	written, err := io.CopyBuffer(w, content.Body, buffer)
	if err != nil || written != content.ContentLength {
		h.logger.WarnContext(r.Context(), "结果文件流传输中断", "request_id", requestID(r.Context()), "artifact_id", artifactID, "error_code", "file_stream_interrupted")
	}
}

func (h *filesHandler) authorization(r *http.Request) (artifactservice.Authorization, error) {
	header := r.Header.Get("Authorization")
	if header != "" {
		if !strings.HasPrefix(header, "Bearer ") || len(header) <= len("Bearer ") {
			return artifactservice.Authorization{}, artifactservice.ErrUnauthorized
		}
		ownerID := ""
		if h.authenticator != nil {
			ownerID, _ = h.authenticator.Authenticate(header[len("Bearer "):])
		} else {
			digest := sha256.Sum256([]byte(header[len("Bearer "):]))
			for _, key := range h.keys {
				if subtle.ConstantTimeCompare(digest[:], key.digest[:]) == 1 {
					ownerID = key.id
				}
			}
		}
		if ownerID == "" {
			return artifactservice.Authorization{}, artifactservice.ErrUnauthorized
		}
		return artifactservice.Authorization{BearerOwner: ownerID, Method: r.Method}, nil
	}
	for key := range r.URL.Query() {
		if key != "expires" && key != "signature" {
			return artifactservice.Authorization{}, artifactservice.ErrUnauthorized
		}
	}
	expires, err := artifactservice.ParseExpires(r.URL.Query().Get("expires"))
	if err != nil || r.URL.Query().Get("signature") == "" {
		return artifactservice.Authorization{}, artifactservice.ErrUnauthorized
	}
	return artifactservice.Authorization{Expires: expires, Signature: r.URL.Query().Get("signature"), Method: r.Method}, nil
}

func (h *filesHandler) writeFileError(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"type":"error","error":{"type":"`+kind+`","message":"`+message+`","http_code":"`+strconv.Itoa(status)+`"}}`)
}
