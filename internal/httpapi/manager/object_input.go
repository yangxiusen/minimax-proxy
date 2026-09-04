package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"minimax-h3-tc/internal/domain"
)

func (h *handler) enrichHistoricalObjectInputs(ctx context.Context, requestBody any) {
	request, ok := requestBody.(map[string]any)
	if !ok || h.objectStorage == nil {
		return
	}
	config, err := h.objectStorage.GetObjectStorageConfig(ctx)
	if err != nil {
		return
	}
	metadataContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	content, _ := request["content"].([]any)
	for index, rawItem := range content {
		item, _ := rawItem.(map[string]any)
		if _, exists := item["input_id"]; exists {
			continue
		}
		rawURL := mediaInputURL(item)
		target, ok := managedObjectURL(rawURL, config.PublicBaseURL)
		if !ok {
			continue
		}
		extension := strings.ToLower(path.Ext(target.Path))
		itemType, _ := item["type"].(string)
		mediaType, supported := historicalMediaType(itemType, extension)
		if !supported {
			continue
		}
		size, _, available := h.headObjectInput(metadataContext, target.String())
		inputID := historicalObjectInputID(index, rawURL)
		item["source_kind"] = "object_storage"
		item["media_type"] = mediaType
		item["extension"] = extension
		item["file_name"] = path.Base(target.Path)
		item["input_id"] = inputID
		item["input_ref"] = "object-input://" + inputID
		if size > 0 {
			item["size_bytes"] = size
		}
		if available {
			item["file_state"] = "available"
		} else {
			item["file_state"] = "unverified"
		}
	}
}

func (h *handler) resolveHistoricalObjectInput(ctx context.Context, taskID, inputID string) (domain.InputSpoolFile, error) {
	if h.objectStorage == nil {
		return domain.InputSpoolFile{}, domain.ErrTaskNotFound
	}
	detail, err := h.store.GetAdminTaskDetail(ctx, taskID)
	if err != nil {
		return domain.InputSpoolFile{}, err
	}
	config, err := h.objectStorage.GetObjectStorageConfig(ctx)
	if err != nil {
		return domain.InputSpoolFile{}, domain.ErrTaskNotFound
	}
	var request map[string]any
	if err := json.Unmarshal([]byte(detail.Task.RequestJSON), &request); err != nil {
		return domain.InputSpoolFile{}, domain.ErrTaskNotFound
	}
	content, _ := request["content"].([]any)
	for index, rawItem := range content {
		item, _ := rawItem.(map[string]any)
		rawURL := mediaInputURL(item)
		if historicalObjectInputID(index, rawURL) != inputID {
			continue
		}
		target, ok := managedObjectURL(rawURL, config.PublicBaseURL)
		if !ok {
			return domain.InputSpoolFile{}, domain.ErrTaskNotFound
		}
		itemType, _ := item["type"].(string)
		role, _ := item["role"].(string)
		if role == "" {
			role = "input"
		}
		extension := strings.ToLower(path.Ext(target.Path))
		mediaType, supported := historicalMediaType(itemType, extension)
		if !supported {
			return domain.InputSpoolFile{}, domain.ErrTaskNotFound
		}
		return domain.InputSpoolFile{
			ID: inputID, TaskID: taskID, ContentIndex: index, ContentType: itemType, Role: role,
			SourceKind: "data_uri", MediaType: mediaType, Extension: extension,
			RelativePath: strings.TrimPrefix(target.Path, "/"), ObjectURL: target.String(),
		}, nil
	}
	return domain.InputSpoolFile{}, domain.ErrTaskNotFound
}

func historicalMediaType(contentType, extension string) (string, bool) {
	switch contentType {
	case "image_url":
		switch extension {
		case ".jpg", ".jpeg":
			return "image/jpeg", true
		case ".png":
			return "image/png", true
		case ".webp":
			return "image/webp", true
		}
	case "video_url":
		if extension == ".mp4" {
			return "video/mp4", true
		}
	case "audio_url":
		switch extension {
		case ".wav":
			return "audio/wav", true
		case ".mp3":
			return "audio/mpeg", true
		}
	}
	return "", false
}

func (h *handler) headObjectInput(ctx context.Context, rawURL string) (int64, string, bool) {
	headContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(headContext, http.MethodHead, rawURL, nil)
	if err != nil {
		return 0, "", false
	}
	response, err := h.inputObjectClient.Do(request)
	if err != nil {
		return 0, "", false
	}
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, "", false
	}
	mediaType := ""
	if value := response.Header.Get("Content-Type"); value != "" {
		mediaType, _, _ = mime.ParseMediaType(value)
	}
	size := response.ContentLength
	if size <= 0 {
		size, _ = strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
	}
	return size, mediaType, true
}

func managedObjectURL(rawURL, publicBaseURL string) (*url.URL, bool) {
	target, targetErr := url.Parse(rawURL)
	base, baseErr := url.Parse(publicBaseURL)
	if targetErr != nil || baseErr != nil || target.Scheme != "https" || base.Scheme != "https" ||
		target.User != nil || target.Fragment != "" || target.RawQuery != "" ||
		base.User != nil || base.Fragment != "" || base.RawQuery != "" || base.Host == "" ||
		!strings.EqualFold(target.Host, base.Host) {
		return nil, false
	}
	prefix := strings.TrimRight(base.Path, "/") + "/MiniMax-H3/inputs/"
	if !strings.HasPrefix(target.Path, prefix) {
		return nil, false
	}
	relativePath := strings.TrimPrefix(target.Path, prefix)
	if relativePath == "" || path.Clean("/"+relativePath) != "/"+relativePath {
		return nil, false
	}
	return target, true
}

func mediaInputURL(item map[string]any) string {
	itemType, _ := item["type"].(string)
	if itemType != "image_url" && itemType != "video_url" && itemType != "audio_url" {
		return ""
	}
	value, _ := item[itemType].(map[string]any)
	rawURL, _ := value["url"].(string)
	return rawURL
}

func historicalObjectInputID(index int, rawURL string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", index, rawURL)))
	return "object_" + hex.EncodeToString(digest[:16])
}
