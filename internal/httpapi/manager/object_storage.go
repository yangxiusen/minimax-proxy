package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"minimax-h3-tc/internal/domain"
)

type objectStorageRequest struct {
	Provider            string `json:"provider"`
	BucketName          string `json:"bucket_name"`
	FileHost            string `json:"file_host"`
	PublicBaseURL       string `json:"public_base_url"`
	PublicKey           string `json:"public_key,omitempty"`
	PrivateKey          string `json:"private_key,omitempty"`
	UseStoredPrivateKey bool   `json:"use_stored_private_key,omitempty"`
	UseStoredConfig     bool   `json:"use_stored_config,omitempty"`
	RequestTimeout      string `json:"request_timeout"`
	Version             *int64 `json:"version,omitempty"`
}

type objectStorageDTO struct {
	Configured            bool   `json:"configured"`
	Provider              string `json:"provider,omitempty"`
	BucketName            string `json:"bucket_name,omitempty"`
	FileHost              string `json:"file_host,omitempty"`
	PublicBaseURL         string `json:"public_base_url,omitempty"`
	PublicKeyFingerprint  string `json:"public_key_fingerprint,omitempty"`
	PrivateKeyFingerprint string `json:"private_key_fingerprint,omitempty"`
	PrivateKeyConfigured  bool   `json:"private_key_configured"`
	RequestTimeout        string `json:"request_timeout,omitempty"`
	LastTestStatus        string `json:"last_test_status,omitempty"`
	LastTestedAt          int64  `json:"last_tested_at,omitempty"`
	Version               int64  `json:"version,omitempty"`
}

type ObjectStorageProbeInput struct {
	Config     domain.ObjectStorageConfig
	PublicKey  string
	PrivateKey string
}

type ObjectStorageProbeResult struct {
	Passed bool        `json:"passed"`
	Checks []NodeCheck `json:"checks"`
}

func (h *handler) getObjectStorage(w http.ResponseWriter, r *http.Request) {
	if h.objectStorage == nil {
		h.internalError(w, r, errors.New("object storage store is nil"))
		return
	}
	config, err := h.objectStorage.GetObjectStorageConfig(r.Context())
	if errors.Is(err, domain.ErrObjectStorageNotFound) {
		h.writeJSON(w, http.StatusOK, objectStorageDTO{Configured: false})
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, makeObjectStorageDTO(config))
}

func (h *handler) putObjectStorage(w http.ResponseWriter, r *http.Request) {
	request, ok := h.readObjectStorageRequest(w, r)
	if !ok {
		return
	}
	config, expectedVersion, ok := h.resolveObjectStorageRequest(w, r, request, false)
	if !ok {
		return
	}
	saved, err := h.objectStorage.PutObjectStorageConfig(r.Context(), expectedVersion, config)
	if errors.Is(err, domain.ErrObjectStorageVersionConflict) {
		h.writeError(w, http.StatusConflict, "object_storage_version_conflict", "对象存储配置已更新，请刷新后重试")
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), "管理员已保存对象存储配置", "provider", saved.Provider, "bucket", saved.BucketName, "stage", "object_storage_update")
	h.writeJSON(w, http.StatusOK, makeObjectStorageDTO(saved))
}

func (h *handler) testObjectStorage(w http.ResponseWriter, r *http.Request) {
	request, ok := h.readObjectStorageRequest(w, r)
	if !ok {
		return
	}
	config, _, ok := h.resolveObjectStorageRequest(w, r, request, true)
	if !ok {
		return
	}
	publicKey, err := h.nodeSecrets.Open(config.PublicKeyNonce, config.PublicKeyCiphertext)
	if err != nil {
		h.internalError(w, r, errors.New("对象存储 Public Key 解密失败"))
		return
	}
	privateKey, err := h.nodeSecrets.Open(config.PrivateKeyNonce, config.PrivateKeyCiphertext)
	if err != nil {
		h.internalError(w, r, errors.New("对象存储 Private Key 解密失败"))
		return
	}
	if h.probeObjectStorage == nil {
		h.internalError(w, r, errors.New("object storage prober is nil"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), config.RequestTimeout)
	defer cancel()
	result := h.probeObjectStorage(ctx, ObjectStorageProbeInput{Config: config, PublicKey: publicKey, PrivateKey: privateKey})
	if request.UseStoredConfig && request.Version != nil {
		if err := h.objectStorage.MarkObjectStorageTest(r.Context(), *request.Version, result.Passed); err != nil {
			h.internalError(w, r, err)
			return
		}
	}
	if result.Passed {
		h.writeJSON(w, http.StatusOK, result)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": "object_storage_probe_failed", "message": "对象存储连接测试未通过"}, "checks": result.Checks})
}

func (h *handler) readObjectStorageRequest(w http.ResponseWriter, r *http.Request) (objectStorageRequest, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "Content-Type 必须为 application/json")
		return objectStorageRequest{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, nodeRequestBodyLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil || !validUniqueNodeObject(body) {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "请求 JSON 无效")
		return objectStorageRequest{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request objectStorageRequest
	if err := decoder.Decode(&request); err != nil {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", "请求 JSON 无效")
		return objectStorageRequest{}, false
	}
	return request, true
}

func (h *handler) resolveObjectStorageRequest(w http.ResponseWriter, r *http.Request, request objectStorageRequest, probe bool) (domain.ObjectStorageConfig, int64, bool) {
	if h.objectStorage == nil || h.nodeSecrets == nil {
		h.internalError(w, r, errors.New("object storage dependencies are nil"))
		return domain.ObjectStorageConfig{}, 0, false
	}
	if request.UseStoredConfig {
		if !probe || request.Version == nil {
			h.writeError(w, http.StatusBadRequest, "bad_request_error", "use_stored_config 仅用于已保存配置测试且 version 必填")
			return domain.ObjectStorageConfig{}, 0, false
		}
		stored, err := h.objectStorage.GetObjectStorageConfig(r.Context())
		if err != nil || stored.Version != *request.Version {
			h.writeError(w, http.StatusConflict, "object_storage_version_conflict", "对象存储配置已更新，请刷新后重试")
			return domain.ObjectStorageConfig{}, 0, false
		}
		return stored, stored.Version, true
	}
	config, err := normalizeObjectStorageRequest(request)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "bad_request_error", err.Error())
		return domain.ObjectStorageConfig{}, 0, false
	}
	expectedVersion := int64(0)
	if request.Version != nil {
		expectedVersion = *request.Version
	}
	if request.UseStoredPrivateKey {
		if expectedVersion < 1 || request.PrivateKey != "" {
			h.writeError(w, http.StatusBadRequest, "bad_request_error", "保留密钥需要有效 version 且不能传 private_key")
			return domain.ObjectStorageConfig{}, 0, false
		}
		stored, err := h.objectStorage.GetObjectStorageConfig(r.Context())
		if err != nil || stored.Version != expectedVersion {
			h.writeError(w, http.StatusConflict, "object_storage_version_conflict", "对象存储配置已更新，请刷新后重试")
			return domain.ObjectStorageConfig{}, 0, false
		}
		config.PrivateKeyCiphertext, config.PrivateKeyNonce, config.PrivateKeyFingerprint = stored.PrivateKeyCiphertext, stored.PrivateKeyNonce, stored.PrivateKeyFingerprint
		if request.PublicKey == "" {
			config.PublicKeyCiphertext, config.PublicKeyNonce, config.PublicKeyFingerprint = stored.PublicKeyCiphertext, stored.PublicKeyNonce, stored.PublicKeyFingerprint
		}
	}
	if len(config.PublicKeyCiphertext) == 0 {
		nonce, ciphertext, fingerprint, err := h.nodeSecrets.Seal(request.PublicKey)
		if err != nil {
			h.internalError(w, r, errors.New("对象存储 Public Key 加密失败"))
			return domain.ObjectStorageConfig{}, 0, false
		}
		config.PublicKeyNonce, config.PublicKeyCiphertext, config.PublicKeyFingerprint = nonce, ciphertext, fingerprint
	}
	if len(config.PrivateKeyCiphertext) == 0 {
		nonce, ciphertext, fingerprint, err := h.nodeSecrets.Seal(request.PrivateKey)
		if err != nil {
			h.internalError(w, r, errors.New("对象存储 Private Key 加密失败"))
			return domain.ObjectStorageConfig{}, 0, false
		}
		config.PrivateKeyNonce, config.PrivateKeyCiphertext, config.PrivateKeyFingerprint = nonce, ciphertext, fingerprint
	}
	return config, expectedVersion, true
}

var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`)

func normalizeObjectStorageRequest(request objectStorageRequest) (domain.ObjectStorageConfig, error) {
	request.Provider = strings.TrimSpace(request.Provider)
	request.BucketName = strings.TrimSpace(request.BucketName)
	request.FileHost = strings.TrimSpace(request.FileHost)
	request.PublicBaseURL = strings.TrimSpace(request.PublicBaseURL)
	if request.Provider != "ucloud-us3" {
		return domain.ObjectStorageConfig{}, errors.New("provider 仅支持 ucloud-us3")
	}
	if !bucketPattern.MatchString(request.BucketName) {
		return domain.ObjectStorageConfig{}, errors.New("bucket_name 格式无效")
	}
	for field, value := range map[string]string{"file_host": request.FileHost, "public_base_url": request.PublicBaseURL} {
		target, err := url.Parse(value)
		if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
			return domain.ObjectStorageConfig{}, errors.New(field + " 必须是无凭据、查询或片段的 HTTPS URL")
		}
	}
	timeout, err := time.ParseDuration(strings.TrimSpace(request.RequestTimeout))
	if err != nil || timeout < time.Second || timeout > 30*time.Minute {
		return domain.ObjectStorageConfig{}, errors.New("request_timeout 必须在 1s 到 30m 之间")
	}
	if !request.UseStoredPrivateKey && (!validStorageKey(request.PublicKey) || !validStorageKey(request.PrivateKey)) {
		return domain.ObjectStorageConfig{}, errors.New("public_key 和 private_key 必须为 1 至 512 位且不含控制字符")
	}
	return domain.ObjectStorageConfig{Provider: request.Provider, BucketName: request.BucketName, FileHost: request.FileHost, PublicBaseURL: request.PublicBaseURL, RequestTimeout: timeout}, nil
}

func validStorageKey(value string) bool {
	return len(value) >= 1 && len(value) <= 512 && strings.IndexFunc(value, unicode.IsControl) < 0
}

func makeObjectStorageDTO(config domain.ObjectStorageConfig) objectStorageDTO {
	return objectStorageDTO{
		Configured: true, Provider: config.Provider, BucketName: config.BucketName, FileHost: config.FileHost, PublicBaseURL: config.PublicBaseURL,
		PublicKeyFingerprint: config.PublicKeyFingerprint, PrivateKeyFingerprint: config.PrivateKeyFingerprint,
		PrivateKeyConfigured: len(config.PrivateKeyCiphertext) > 0, RequestTimeout: config.RequestTimeout.String(), LastTestStatus: config.LastTestStatus,
		LastTestedAt: unixTime(config.LastTestedAt), Version: config.Version,
	}
}
