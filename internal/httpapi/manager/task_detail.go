package manager

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"minimax-h3-tc/internal/domain"
)

func sanitizedTaskRequest(requestJSON string, files []domain.InputSpoolFile) (any, bool) {
	var request map[string]any
	if err := json.Unmarshal([]byte(requestJSON), &request); err != nil {
		return map[string]any{"invalid": true}, false
	}
	byIndex := make(map[int]domain.InputSpoolFile, len(files))
	for _, file := range files {
		byIndex[file.ContentIndex] = file
	}
	legacy := false
	content, _ := request["content"].([]any)
	for index, rawItem := range content {
		item, _ := rawItem.(map[string]any)
		itemType, _ := item["type"].(string)
		var urlObject map[string]any
		switch itemType {
		case "image_url":
			urlObject, _ = item["image_url"].(map[string]any)
		case "audio_url":
			urlObject, _ = item["audio_url"].(map[string]any)
		default:
			continue
		}
		if urlObject == nil {
			continue
		}
		rawURL, _ := urlObject["url"].(string)
		if strings.HasPrefix(rawURL, "data:") {
			legacy = true
			urlObject["url"] = "[legacy-base64-hidden]"
			item["legacy_base64_present"] = true
		}
		if file, ok := byIndex[index]; ok {
			item["source_kind"] = file.SourceKind
			item["media_type"] = file.MediaType
			item["extension"] = file.Extension
			item["file_name"] = filepath.Base(filepath.FromSlash(file.RelativePath))
			item["input_id"] = file.ID
			item["input_ref"] = "proxy-input://" + file.TaskID + "/" + file.ID
			item["size_bytes"] = file.SizeBytes
			item["sha256"] = file.SHA256
			item["file_state"] = "available"
		}
	}
	return request, legacy
}

func taskPhaseFromTask(task domain.Task) string {
	return taskPhase(domain.AdminTaskSummary{InternalStatus: task.Status, Status: task.Status.V2(), RetryCount: task.RetryCount, UpstreamID: task.UpstreamID})
}
