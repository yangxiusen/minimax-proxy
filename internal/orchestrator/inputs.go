package orchestrator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/netguard"
	"minimax-h3-tc/internal/store/sqlite"
	"minimax-h3-tc/internal/upstream/nodeapi"
)

const maxInputArtifactBytes int64 = 256 << 20

type InputStore interface {
	GetTaskForExecution(context.Context, string) (domain.Task, error)
	GetInputSpoolFile(context.Context, string, string) (domain.InputSpoolFile, error)
	GetActiveArtifactLocation(context.Context, string, string) (sqlite.ArtifactLocation, error)
	RegisterInputArtifact(context.Context, string, string, string, string, string, int64, string, string) error
}

type InputImportClient interface {
	ImportArtifact(context.Context, string, nodeapi.ImportArtifactRequest) (nodeapi.Artifact, error)
}

type inputMaterializationError struct {
	phase string
	err   error
}

func (e inputMaterializationError) Error() string                     { return e.err.Error() }
func (e inputMaterializationError) Unwrap() error                     { return e.err }
func (e inputMaterializationError) InputMaterializationPhase() string { return e.phase }

type InputMaterializer struct {
	Store          InputStore
	Guard          *netguard.Guard
	Client         *http.Client
	Logger         *slog.Logger
	InputSpoolRoot string
}

func (m *InputMaterializer) Materialize(ctx context.Context, taskID, nodeID, requestID string, client InputImportClient) ([]nodeapi.InputArtifact, error) {
	if m == nil || m.Store == nil || client == nil {
		return nil, errors.New("输入素材服务未配置")
	}
	task, err := m.Store.GetTaskForExecution(ctx, taskID)
	if err != nil {
		return nil, err
	}
	var request struct {
		Content []struct {
			Type     string `json:"type"`
			Role     string `json:"role"`
			ImageURL *struct {
				URL string `json:"url"`
			} `json:"image_url"`
			VideoURL *struct {
				URL string `json:"url"`
			} `json:"video_url"`
			AudioURL *struct {
				URL string `json:"url"`
			} `json:"audio_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(task.RequestJSON), &request); err != nil {
		return nil, errors.New("任务输入素材快照损坏")
	}
	logger := m.logger()
	logger.InfoContext(ctx, "输入素材处理开始", "task_id", taskID, "node_id", nodeID, "stage", "input_materialization")
	result := make([]nodeapi.InputArtifact, 0)
	for index, item := range request.Content {
		if item.Type == "text" {
			continue
		}
		rawURL := ""
		switch item.Type {
		case "image_url":
			if item.ImageURL != nil {
				rawURL = item.ImageURL.URL
			}
		case "video_url":
			if item.VideoURL != nil {
				rawURL = item.VideoURL.URL
			}
		case "audio_url":
			if item.AudioURL != nil {
				rawURL = item.AudioURL.URL
			}
		default:
			return nil, errors.New("任务输入素材类型无效")
		}
		logicalID := stableInputID(taskID, index, rawURL)
		if inputTaskID, inputID, ok := parseProxyInputRef(rawURL); ok {
			if inputTaskID != taskID {
				return nil, errors.New("输入临时文件引用不属于当前任务")
			}
			logicalID = inputID
		}
		if local, localErr := m.Store.GetActiveArtifactLocation(ctx, logicalID, nodeID); localErr == nil {
			result = append(result, nodeapi.InputArtifact{ArtifactID: local.NodeArtifactID, Role: item.Role})
			continue
		} else if !errors.Is(localErr, sqlite.ErrArtifactNotFound) {
			return nil, localErr
		}
		file, mediaType, suffix, size, digest, cleanup, err := m.fetch(ctx, taskID, rawURL, item.Type)
		if err != nil {
			phaseErr := inputMaterializationError{phase: "read", err: err}
			logger.ErrorContext(ctx, "输入素材读取失败", "task_id", taskID, "node_id", nodeID, "input_index", index, "role", item.Role, "stage", "input_materialization", "error_code", "input_materialization_failed", "error_reason", materializationErrorMessage(phaseErr))
			return nil, phaseErr
		}
		if strings.HasPrefix(rawURL, "proxy-input://") {
			logger.InfoContext(ctx, "输入素材临时文件读取完成", "task_id", taskID, "node_id", nodeID, "input_index", index, "role", item.Role, "media_type", mediaType, "size_bytes", size, "stage", "input_materialization")
		} else if strings.HasPrefix(rawURL, "data:") {
			logger.InfoContext(ctx, "输入素材 Base64 解码完成", "task_id", taskID, "node_id", nodeID, "input_index", index, "role", item.Role, "media_type", mediaType, "size_bytes", size, "stage", "input_materialization")
		} else {
			logger.InfoContext(ctx, "输入素材下载完成", "task_id", taskID, "node_id", nodeID, "input_index", index, "role", item.Role, "media_type", mediaType, "size_bytes", size, "stage", "input_materialization")
		}
		logger.InfoContext(ctx, "开始向节点导入输入素材", "task_id", taskID, "node_id", nodeID, "input_index", index, "role", item.Role, "media_type", mediaType, "size_bytes", size, "stage", "input_materialization")
		artifact, importErr := client.ImportArtifact(ctx, requestID+fmt.Sprintf("-input-%d", index), nodeapi.ImportArtifactRequest{
			OperationID: "import-" + logicalID + "-" + nodeID, SourceArtifactID: logicalID,
			ExternalTaskID: taskID, ExpectedSize: size, ExpectedSHA256: digest, Kind: inputKind(mediaType),
			Filename: logicalID + suffix, Content: file,
		})
		fileName := file.Name()
		closeErr := file.Close()
		var removeErr error
		if cleanup {
			removeErr = os.Remove(fileName)
		}
		if importErr != nil {
			phaseErr := inputMaterializationError{phase: "import", err: importErr}
			logger.ErrorContext(ctx, "输入素材节点导入失败", "task_id", taskID, "node_id", nodeID, "input_index", index, "role", item.Role, "stage", "input_materialization", "error_code", "input_materialization_failed", "error_reason", materializationErrorMessage(phaseErr))
			return nil, phaseErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, removeErr
		}
		if artifact.ArtifactID == "" || artifact.SizeBytes != size || !strings.EqualFold(artifact.SHA256, digest) || artifact.State != "active" {
			return nil, errors.New("节点导入素材完整性校验失败")
		}
		manifest := ""
		if len(artifact.MediaManifest) > 0 && string(artifact.MediaManifest) != "null" {
			manifest = string(artifact.MediaManifest)
		}
		if err := m.Store.RegisterInputArtifact(ctx, logicalID, taskID, proxyInputKind(mediaType), nodeID, artifact.ArtifactID, size, digest, manifest); err != nil {
			return nil, err
		}
		logger.InfoContext(ctx, "输入素材节点导入完成", "task_id", taskID, "node_id", nodeID, "input_index", index, "role", item.Role, "artifact_id", artifact.ArtifactID, "size_bytes", size, "stage", "input_materialization")
		result = append(result, nodeapi.InputArtifact{ArtifactID: artifact.ArtifactID, Role: item.Role})
	}
	return result, nil
}

func (m *InputMaterializer) logger() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

func (m *InputMaterializer) fetch(ctx context.Context, taskID, rawURL, contentType string) (*os.File, string, string, int64, string, bool, error) {
	if refTaskID, inputID, ok := parseProxyInputRef(rawURL); ok {
		if refTaskID != taskID {
			return nil, "", "", 0, "", false, errors.New("输入临时文件引用不属于当前任务")
		}
		return m.fetchProxyInput(ctx, taskID, inputID, contentType)
	}
	temporary, err := os.CreateTemp("", "minimax-h3-input-*")
	if err != nil {
		return nil, "", "", 0, "", false, err
	}
	fail := func(err error) (*os.File, string, string, int64, string, bool, error) {
		name := temporary.Name()
		_ = temporary.Close()
		_ = os.Remove(name)
		return nil, "", "", 0, "", false, err
	}
	mediaType, suffix := "", ".bin"
	var source io.ReadCloser
	if strings.HasPrefix(rawURL, "data:") {
		header, payload, ok := strings.Cut(rawURL, ",")
		if !ok || !strings.HasSuffix(strings.ToLower(header), ";base64") {
			return fail(errors.New("素材 Data URI 无效"))
		}
		mediaType = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64"))
		source = io.NopCloser(base64.NewDecoder(base64.StdEncoding.Strict(), bytes.NewBufferString(payload)))
	} else {
		guard := m.Guard
		if guard == nil {
			guard = netguard.New(netguard.Options{})
		}
		if _, err := guard.Validate(ctx, rawURL); err != nil {
			return fail(err)
		}
		client := m.Client
		if client == nil {
			client = guard.Client(30*time.Second, maxInputArtifactBytes, 3)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return fail(err)
		}
		response, err := client.Do(request)
		if err != nil {
			return fail(err)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			response.Body.Close()
			return fail(fmt.Errorf("素材下载 HTTP %d", response.StatusCode))
		}
		if response.ContentLength > maxInputArtifactBytes {
			response.Body.Close()
			return fail(errors.New("输入素材超过 256 MiB"))
		}
		mediaType, _, _ = mime.ParseMediaType(response.Header.Get("Content-Type"))
		source = response.Body
	}
	defer source.Close()
	if !strings.HasPrefix(mediaType, strings.TrimSuffix(contentType, "_url")+"/") {
		mediaType = map[string]string{"image_url": "image/jpeg", "video_url": "video/mp4", "audio_url": "audio/mpeg"}[contentType]
	}
	suffix = mediaSuffix(mediaType)
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(source, maxInputArtifactBytes+1))
	if err != nil {
		return fail(err)
	}
	if size <= 0 || size > maxInputArtifactBytes {
		return fail(errors.New("输入素材为空或超过 256 MiB"))
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return temporary, mediaType, suffix, size, hex.EncodeToString(hash.Sum(nil)), true, nil
}

func (m *InputMaterializer) fetchProxyInput(ctx context.Context, taskID, inputID, contentType string) (*os.File, string, string, int64, string, bool, error) {
	if m.InputSpoolRoot == "" {
		return nil, "", "", 0, "", false, errors.New("输入临时目录未配置")
	}
	fileMeta, err := m.Store.GetInputSpoolFile(ctx, taskID, inputID)
	if err != nil {
		return nil, "", "", 0, "", false, err
	}
	if !strings.HasPrefix(fileMeta.MediaType, strings.TrimSuffix(contentType, "_url")+"/") {
		return nil, "", "", 0, "", false, errors.New("输入临时文件媒体类型不匹配")
	}
	path := filepath.Join(m.InputSpoolRoot, filepath.FromSlash(fileMeta.RelativePath))
	if !safeUnder(m.InputSpoolRoot, path) {
		return nil, "", "", 0, "", false, errors.New("输入临时文件路径无效")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", "", 0, "", false, fmt.Errorf("input_spool_missing: %w", err)
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		_ = file.Close()
		return nil, "", "", 0, "", false, err
	}
	if size != fileMeta.SizeBytes || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), fileMeta.SHA256) {
		_ = file.Close()
		return nil, "", "", 0, "", false, errors.New("input_spool_integrity_failed")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, "", "", 0, "", false, err
	}
	return file, fileMeta.MediaType, fileMeta.Extension, fileMeta.SizeBytes, fileMeta.SHA256, false, nil
}

func parseProxyInputRef(rawURL string) (taskID, inputID string, ok bool) {
	const prefix = "proxy-input://"
	if !strings.HasPrefix(rawURL, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(rawURL, prefix)
	taskID, inputID, ok = strings.Cut(rest, "/")
	if !ok || taskID == "" || inputID == "" || strings.Contains(inputID, "/") {
		return "", "", false
	}
	return taskID, inputID, true
}

func safeUnder(root, path string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func stableInputID(taskID string, index int, source string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", taskID, index, source)))
	return "input_" + hex.EncodeToString(digest[:16])
}

func mediaSuffix(mediaType string) string {
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	}
	if values, err := mime.ExtensionsByType(mediaType); err == nil && len(values) > 0 {
		return values[0]
	}
	return ".bin"
}

func inputKind(mediaType string) string {
	switch {
	case strings.HasPrefix(mediaType, "image/"):
		return "image"
	case strings.HasPrefix(mediaType, "video/"):
		return "video"
	case strings.HasPrefix(mediaType, "audio/"):
		return "audio"
	default:
		return "binary"
	}
}

func proxyInputKind(mediaType string) string {
	switch inputKind(mediaType) {
	case "audio":
		return "audio_source"
	case "video":
		return "intermediate_video"
	default:
		return "test_output"
	}
}
