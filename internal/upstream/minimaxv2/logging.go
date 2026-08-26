package minimaxv2

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"unicode/utf8"

	"minimax-h3-tc/internal/logsafe"
)

const (
	maxLoggedResponseBytes = 32 << 10
	maxLoggedErrorBytes    = 1024
)

type proxyTaskIDContextKey struct{}

// WithProxyTaskID associates the proxy task with the outbound official request logs.
func WithProxyTaskID(ctx context.Context, taskID string) context.Context {
	if strings.TrimSpace(taskID) == "" {
		return ctx
	}
	return context.WithValue(ctx, proxyTaskIDContextKey{}, taskID)
}

func proxyTaskID(ctx context.Context) string {
	taskID, _ := ctx.Value(proxyTaskIDContextKey{}).(string)
	return taskID
}

func (client *Client) logSubmitRequest(ctx context.Context, input createRequest) {
	client.log().InfoContext(ctx, "官方 V2 创建任务请求",
		"stage", "official_submit",
		"event", "request",
		"task_id", proxyTaskID(ctx),
		"request", summarizeCreateRequest(input),
	)
}

func (client *Client) logSubmitResponse(ctx context.Context, statusCode int, data []byte, operationErr error) {
	summary, truncated, details := client.summarizeResponse(data)
	attributes := []any{
		"stage", "official_submit",
		"event", "response",
		"task_id", proxyTaskID(ctx),
		"status_code", statusCode,
		"response_bytes", len(data),
		"response_truncated", truncated,
	}
	if summary != "" {
		attributes = append(attributes, "response", summary)
	}
	if details.RequestID != "" {
		attributes = append(attributes, "request_id", details.RequestID)
	}
	if details.ErrorType != "" {
		attributes = append(attributes, "error_type", details.ErrorType)
	}
	if details.ErrorMessage != "" {
		attributes = append(attributes, "error_message", details.ErrorMessage)
	}
	if operationErr != nil {
		attributes = append(attributes, "error_reason", client.safeError(operationErr))
		client.log().ErrorContext(ctx, "官方 V2 创建任务响应失败", attributes...)
		return
	}
	client.log().InfoContext(ctx, "官方 V2 创建任务响应", attributes...)
}

func (client *Client) log() *slog.Logger {
	if client.logger != nil {
		return client.logger
	}
	return slog.Default()
}

func summarizeCreateRequest(input createRequest) map[string]any {
	summary := map[string]any{
		"model":      input.Model,
		"resolution": input.Resolution,
		"duration":   input.Duration,
	}
	if input.Ratio != "" {
		summary["ratio"] = input.Ratio
	}
	if input.AIGCWatermark != nil {
		summary["aigc_watermark"] = *input.AIGCWatermark
	}
	var content any
	if err := json.Unmarshal(input.Content, &content); err != nil {
		summary["content"] = map[string]any{"summary_error": "invalid_content_json"}
		return summary
	}
	summary["content"] = sanitizeValue(content, "")
	return summary
}

func sanitizeValue(value any, key string) any {
	if isSensitiveKey(key) {
		return "[redacted]"
	}
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			result[childKey] = sanitizeValue(childValue, childKey)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, childValue := range typed {
			result[index] = sanitizeValue(childValue, "")
		}
		return result
	case string:
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(typed)), "data:") {
			return summarizeDataURI(typed)
		}
		if containsBase64DataURI(typed) {
			return "[redacted-embedded-data-uri]"
		}
		return typed
	default:
		return value
	}
}

func summarizeDataURI(value string) map[string]any {
	summary := map[string]any{"source": "data_uri"}
	metadata, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(strings.ToLower(metadata), "data:") {
		summary["invalid"] = true
		return summary
	}
	metadata = metadata[len("data:"):]
	parts := strings.Split(metadata, ";")
	if len(parts) > 0 && parts[0] != "" {
		summary["media_type"] = strings.ToLower(parts[0])
	}
	isBase64 := false
	for _, part := range parts[1:] {
		if strings.EqualFold(part, "base64") {
			isBase64 = true
			break
		}
	}
	if !isBase64 {
		summary["invalid"] = true
		return summary
	}
	hash := sha256.New()
	decodedBytes, err := io.Copy(hash, base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded)))
	if err != nil {
		summary["invalid"] = true
		return summary
	}
	summary["decoded_bytes"] = decodedBytes
	summary["sha256"] = hex.EncodeToString(hash.Sum(nil))
	return summary
}

type responseDetails struct {
	RequestID    string
	ErrorCode    string
	ErrorType    string
	ErrorMessage string
	ResourceType string
}

func (client *Client) summarizeResponse(data []byte) (string, bool, responseDetails) {
	if len(data) == 0 {
		return "", false, responseDetails{}
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return "<non-json response omitted>", false, responseDetails{}
	}
	details := extractResponseDetails(value)
	details.RequestID = client.safeText(details.RequestID, maxLoggedErrorBytes)
	details.ErrorCode = client.safeText(details.ErrorCode, maxLoggedErrorBytes)
	details.ErrorType = client.safeText(details.ErrorType, maxLoggedErrorBytes)
	details.ErrorMessage = client.safeText(details.ErrorMessage, maxLoggedErrorBytes)
	details.ResourceType = client.safeText(details.ResourceType, maxLoggedErrorBytes)
	sanitized := client.sanitizeResponseValue(value, "")
	encoded, err := json.Marshal(sanitized)
	if err != nil {
		return "<response summary unavailable>", false, details
	}
	if len(encoded) <= maxLoggedResponseBytes {
		return string(encoded), false, details
	}
	return truncateUTF8(string(encoded), maxLoggedResponseBytes), true, details
}

func (client *Client) sanitizeResponseValue(value any, key string) any {
	if isSensitiveKey(key) {
		return "[redacted]"
	}
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			result[childKey] = client.sanitizeResponseValue(childValue, childKey)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, childValue := range typed {
			result[index] = client.sanitizeResponseValue(childValue, "")
		}
		return result
	case string:
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(typed)), "data:") {
			return summarizeDataURI(typed)
		}
		if containsBase64DataURI(typed) {
			return "[redacted-embedded-data-uri]"
		}
		return client.safeText(typed, maxLoggedResponseBytes)
	default:
		return value
	}
}

func extractResponseDetails(value any) responseDetails {
	root, _ := value.(map[string]any)
	details := responseDetails{RequestID: stringField(root, "request_id")}
	if failure, ok := root["error"].(map[string]any); ok {
		details.ErrorCode = stringField(failure, "code")
		details.ErrorType = stringField(failure, "type")
		details.ErrorMessage = stringField(failure, "message")
		details.ResourceType = stringField(failure, "resource_type")
	}
	return details
}

func stringField(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func isSensitiveKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "authorization", "api_key", "apikey", "access_token", "token", "private_key":
		return true
	default:
		return false
	}
}

func (client *Client) safeError(err error) string {
	return client.safeText(logsafe.Error(err), maxLoggedErrorBytes)
}

func (client *Client) safeText(value string, limit int) string {
	if client.apiKey != "" {
		value = strings.ReplaceAll(value, client.apiKey, "[redacted]")
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "data:") {
		return "[redacted-data-uri]"
	}
	if containsBase64DataURI(value) {
		return "[redacted-embedded-data-uri]"
	}
	return truncateUTF8(value, limit)
}

func containsBase64DataURI(value string) bool {
	lower := strings.ToLower(value)
	for offset := 0; offset < len(lower); {
		index := strings.Index(lower[offset:], "data:")
		if index < 0 {
			return false
		}
		index += offset
		comma := strings.IndexByte(lower[index:], ',')
		if comma < 0 {
			return false
		}
		if strings.Contains(lower[index:index+comma], ";base64") {
			return true
		}
		offset = index + len("data:")
	}
	return false
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
