package inputspool

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"minimax-h3-tc/internal/domain"
)

const maxOfficialRequestBytes = 64 << 20

type RestoreStore interface {
	GetInputSpoolFile(context.Context, string, string) (domain.InputSpoolFile, error)
}

type Restorer struct {
	root            string
	store           RestoreStore
	maxRequestBytes int
}

func NewRestorer(root string, store RestoreStore) *Restorer {
	return &Restorer{root: root, store: store, maxRequestBytes: maxOfficialRequestBytes}
}

func (r *Restorer) Restore(ctx context.Context, taskID string, requestJSON []byte) ([]byte, error) {
	if r == nil {
		return nil, errors.New("输入还原器未配置")
	}
	if len(requestJSON) > r.maxRequestBytes {
		return nil, errors.New("官方请求体不能超过 64 MiB")
	}
	var request map[string]any
	if err := json.Unmarshal(requestJSON, &request); err != nil {
		return nil, errors.New("官方请求 JSON 无效")
	}
	content, _ := request["content"].([]any)
	changed := false
	for index, rawItem := range content {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item, _ := rawItem.(map[string]any)
		contentType, _ := item["type"].(string)
		if contentType != "image_url" && contentType != "video_url" && contentType != "audio_url" {
			continue
		}
		urlObject, _ := item[contentType].(map[string]any)
		rawURL, _ := urlObject["url"].(string)
		if !strings.HasPrefix(rawURL, "proxy-input://") {
			continue
		}
		if r.store == nil || strings.TrimSpace(r.root) == "" {
			return nil, errors.New("输入还原器未配置")
		}
		inputID, err := parseProxyInputReference(rawURL, taskID)
		if err != nil {
			return nil, err
		}
		file, err := r.store.GetInputSpoolFile(ctx, taskID, inputID)
		if err != nil {
			return nil, fmt.Errorf("读取输入暂存元数据失败: %w", err)
		}
		role, _ := item["role"].(string)
		if file.ID != inputID || file.TaskID != taskID || file.ContentIndex != index || file.ContentType != contentType || file.Role != role || file.SourceKind != "data_uri" {
			return nil, errors.New("输入暂存元数据与请求不匹配")
		}
		payload, err := r.readVerified(file)
		if err != nil {
			return nil, err
		}
		urlObject["url"] = "data:" + file.MediaType + ";base64," + base64.StdEncoding.EncodeToString(payload)
		changed = true
	}
	if !changed {
		return append([]byte(nil), requestJSON...), nil
	}
	restored, err := json.Marshal(request)
	if err != nil {
		return nil, errors.New("序列化官方请求失败")
	}
	if len(restored) > r.maxRequestBytes {
		return nil, errors.New("官方请求体不能超过 64 MiB")
	}
	return restored, nil
}

func parseProxyInputReference(rawURL, taskID string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "proxy-input" || parsed.Host != taskID || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("输入暂存引用无效")
	}
	inputID := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if inputID == "" || strings.Contains(inputID, "/") || inputID != parsed.Path[1:] {
		return "", errors.New("输入暂存引用无效")
	}
	return inputID, nil
}

func (r *Restorer) readVerified(file domain.InputSpoolFile) ([]byte, error) {
	path := filepath.Join(r.root, filepath.FromSlash(file.RelativePath))
	if !safeUnder(r.root, path) || filepath.ToSlash(file.RelativePath) != file.RelativePath {
		return nil, errors.New("输入暂存文件路径无效")
	}
	if filepath.Ext(path) != file.Extension {
		return nil, errors.New("输入暂存文件扩展名不匹配")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("输入暂存文件不可读")
	}
	if info.Size() != file.SizeBytes || info.Size() <= 0 || info.Size() > maxDecodedBytes(file.ContentType) {
		return nil, errors.New("输入暂存文件大小不匹配")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("读取输入暂存文件失败")
	}
	digest := sha256.Sum256(payload)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), file.SHA256) {
		return nil, errors.New("输入暂存文件校验失败")
	}
	detected := normalizeMediaType(detectMediaType(payload))
	if detected == "" || detected != normalizeMediaType(file.MediaType) {
		return nil, errors.New("输入暂存文件类型不匹配")
	}
	return payload, nil
}

func maxDecodedBytes(contentType string) int64 {
	switch contentType {
	case "image_url":
		return 30 << 20
	case "video_url":
		return 50 << 20
	case "audio_url":
		return 15 << 20
	default:
		return 0
	}
}
