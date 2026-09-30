package logsafe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"minimax-h3-tc/internal/domain"
	"regexp"
	"strings"
	"unicode/utf8"
)

const diagnosticLimit = 16 << 10

var credentialDiagnosticText = regexp.MustCompile(`(?i)\bbearer\b|\b(?:authorization|api[_-]?key|token|cookie|password|secret|signature|csrf)\b\s*[:=]`)

type taskTraceKey struct{}
type taskTrace struct{ TaskID, NodeID string }

func WithTask(ctx context.Context, taskID, nodeID string) context.Context {
	return context.WithValue(ctx, taskTraceKey{}, taskTrace{taskID, nodeID})
}
func TaskTrace(ctx context.Context) (string, string) {
	trace, _ := ctx.Value(taskTraceKey{}).(taskTrace)
	return trace.TaskID, trace.NodeID
}

func Generation(r domain.GenerationRequest, secrets ...string) map[string]any {
	result := map[string]any{"model": diagnosticText(r.Model, secrets)}
	if r.DurationPresent || r.Duration != 0 {
		result["duration"] = r.Duration
	}
	if r.Resolution != "" {
		result["resolution"] = diagnosticText(r.Resolution, secrets)
	}
	if r.Ratio != "" {
		result["ratio"] = diagnosticText(r.Ratio, secrets)
	}
	if r.AIGCWatermark != nil {
		result["aigc_watermark"] = *r.AIGCWatermark
	}
	if r.CallbackURL != nil {
		result["callback_url"] = "[redacted-url]"
	}
	content := make([]any, 0, min(len(r.Content), 32))
	for index, item := range r.Content {
		if index >= 32 {
			break
		}
		entry := map[string]any{"type": diagnosticText(item.Type, secrets)}
		if item.Type == "text" {
			entry["text"] = "[redacted]"
			entry["text_chars"] = utf8.RuneCountInString(item.Text)
		}
		if item.Role != "" {
			entry["role"] = diagnosticText(item.Role, secrets)
		}
		if media := item.Media(); media != nil {
			entry[item.Type] = map[string]any{"url": mediaDiagnostic(media.URL)}
		}
		content = append(content, entry)
	}
	result["content"] = content
	return result
}

func DiagnosticJSON(data []byte, secrets ...string) any {
	if len(data) > 1<<20 {
		return map[string]any{"omitted": "oversized_json", "bytes": len(data)}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if !json.Valid(data) || decoder.Decode(&value) != nil {
		return map[string]any{"omitted": "non_json", "bytes": len(data)}
	}
	summary := diagnosticValue(value, "", secrets, 0)
	encoded, err := json.Marshal(summary)
	if err != nil {
		return map[string]any{"omitted": "invalid_summary"}
	}
	if len(encoded) > diagnosticLimit {
		return map[string]any{"truncated": true, "summary": truncateDiagnostic(string(encoded), diagnosticLimit/2), "bytes": len(data)}
	}
	return summary
}
func diagnosticValue(value any, key string, secrets []string, depth int) any {
	name := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
	for _, sensitive := range []string{"authorization", "apikey", "token", "secret", "password", "signature", "cookie", "credential", "headers", "idempotency"} {
		if strings.Contains(name, sensitive) {
			return "[redacted]"
		}
	}
	if name == "text" || name == "prompt" || name == "key" || name == "path" {
		return "[redacted]"
	}
	if depth > 8 {
		return "[depth-limit]"
	}
	switch v := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for k, child := range v {
			if len(result) >= 64 {
				result["_truncated"] = true
				break
			}
			result[diagnosticText(k, secrets)] = diagnosticValue(child, k, secrets, depth+1)
		}
		return result
	case []any:
		result := make([]any, 0, min(len(v), 32))
		for i, child := range v {
			if i >= 32 {
				result = append(result, map[string]any{"remaining_items": len(v) - i})
				break
			}
			result = append(result, diagnosticValue(child, "", secrets, depth+1))
		}
		return result
	case string:
		if name == "url" || strings.HasSuffix(name, "url") || strings.HasPrefix(strings.ToLower(v), "data:") {
			return mediaDiagnostic(v)
		}
		// Error responses may echo prompts or raw binary in arbitrary fields.
		// Only stable identity and parameter fields retain string values.
		switch name {
		case "id", "taskid", "requestid", "model", "status", "code", "type", "role", "resolution", "ratio", "format", "mimetype":
			return diagnosticText(v, secrets)
		default:
			return "[redacted]"
		}
	default:
		return value
	}
}
func mediaDiagnostic(value string) any {
	source := "opaque"
	prefix, _, _ := strings.Cut(value, ":")
	prefix = strings.ToLower(prefix)
	switch prefix {
	case "http", "https", "asset", "proxy-input", "mm_file":
		source = prefix
	case "data":
		return map[string]any{"source": "data_uri", "encoded_chars": len(value)}
	}
	sum := sha256.Sum256([]byte(value))
	return map[string]any{"source": source, "sha256": hex.EncodeToString(sum[:]), "redacted": true}
}
func diagnosticText(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	if credentialDiagnosticText.MatchString(value) {
		return "[redacted-sensitive-text]"
	}
	value = Error(errors.New(value))
	return strings.ToValidUTF8(truncateDiagnostic(value, 512), "?")
}
func truncateDiagnostic(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "[truncated]"
}
