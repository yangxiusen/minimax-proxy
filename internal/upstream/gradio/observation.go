package gradio

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

type ObservationStatus string

const (
	ObservationUnknown ObservationStatus = "unknown"
	ObservationIdle    ObservationStatus = "idle"
	ObservationRunning ObservationStatus = "running"
	ObservationFailed  ObservationStatus = "failed"
)

type Observation struct {
	Status        ObservationStatus
	PrivateQueue  *int
	CPUPercent    *float64
	MemoryPercent *float64
	GPUPercent    *float64
	VRAMPercent   *float64
}

var (
	queuePattern         = regexp.MustCompile(`(?i)(?:队列|排队|等待|queue|queued|waiting|pending)[^0-9-]*([+-]?\d+(?:\.\d+)?)|([+-]?\d+(?:\.\d+)?)[^\pL]*(?:队列|排队|等待|queue|queued|waiting|pending)`)
	percentNumberPattern = regexp.MustCompile(`^\s*([+-]?\d+(?:\.\d+)?)\s*%?\s*$`)
	resourcePatterns     = map[string]*regexp.Regexp{
		"cpu":    regexp.MustCompile(`(?i)(?:cpu|处理器)(?:利用率|使用率|usage|utilization)?\s*[:=：]?\s*([+-]?\d+(?:\.\d+)?)\s*%`),
		"memory": regexp.MustCompile(`(?i)(?:内存|系统内存|ram|memory)(?:利用率|使用率|usage|utilization)?\s*[:=：]?\s*([+-]?\d+(?:\.\d+)?)\s*%`),
		"gpu":    regexp.MustCompile(`(?i)(?:gpu|显卡)(?:利用率|使用率|usage|utilization)?\s*[:=：]?\s*([+-]?\d+(?:\.\d+)?)\s*%`),
		"vram":   regexp.MustCompile(`(?i)(?:显存|vram|gpu[ _-]*(?:memory|mem))(?:利用率|使用率|usage|utilization)?\s*[:=：]?\s*([+-]?\d+(?:\.\d+)?)\s*%`),
	}
	resourceUsagePatterns = map[string]*regexp.Regexp{
		"memory": regexp.MustCompile(`(?i)(?:内存|系统内存|ram|memory)(?:占用|usage|used)?\s*[:=：]?\s*[+-]?\d+(?:\.\d+)?\s*/\s*[+-]?\d+(?:\.\d+)?\s*(?:[kmgt]i?b)?\s*\(\s*([+-]?\d+(?:\.\d+)?)\s*%\s*\)`),
		"vram":   regexp.MustCompile(`(?i)(?:显存|vram|gpu[ _-]*(?:memory|mem))(?:占用|usage|used)?\s*[:=：]?\s*[+-]?\d+(?:\.\d+)?\s*/\s*[+-]?\d+(?:\.\d+)?\s*(?:[kmgt]i?b)?\s*\(\s*([+-]?\d+(?:\.\d+)?)\s*%\s*\)`),
	}
)

func ParseObservation(result []any) Observation {
	observation := Observation{Status: ObservationUnknown}
	if len(result) > 1 {
		observation.Status = parseObservationStatus(result[1])
	}
	if len(result) > 2 {
		observation.PrivateQueue = parseQueue(result[2])
	}
	if len(result) > 3 {
		parseResources(result[3], &observation)
	}
	if len(result) > 4 && containsFailureKeyword(result[4]) {
		observation.Status = ObservationFailed
	}
	return observation
}

func parseObservationStatus(value any) ObservationStatus {
	text, ok := value.(string)
	if !ok {
		return ObservationUnknown
	}
	text = strings.ToLower(text)
	if containsAny(text, "失败", "错误", "异常", "failed", "failure", "error") {
		return ObservationFailed
	}
	if containsAny(text, "运行", "生成中", "处理中", "工作中", "running", "processing", "working", "busy") {
		return ObservationRunning
	}
	if containsAny(text, "空闲", "闲置", "无任务", "等待任务", "等待提交", "完成", "idle", "ready", "complete", "finished") {
		return ObservationIdle
	}
	return ObservationUnknown
}

func parseQueue(value any) *int {
	if number, ok := nonNegativeInteger(value); ok {
		return &number
	}
	text, ok := value.(string)
	if !ok {
		return nil
	}
	match := queuePattern.FindStringSubmatch(text)
	if len(match) == 0 {
		return nil
	}
	numberText := match[1]
	if numberText == "" {
		numberText = match[2]
	}
	valueNumber, err := strconv.ParseFloat(numberText, 64)
	if err != nil {
		return nil
	}
	return integerFloat(valueNumber)
}

func nonNegativeInteger(value any) (int, bool) {
	var number float64
	switch typed := value.(type) {
	case int:
		number = float64(typed)
	case int8:
		number = float64(typed)
	case int16:
		number = float64(typed)
	case int32:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case uint:
		number = float64(typed)
	case uint8:
		number = float64(typed)
	case uint16:
		number = float64(typed)
	case uint32:
		number = float64(typed)
	case uint64:
		number = float64(typed)
	case float32:
		number = float64(typed)
	case float64:
		number = typed
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	integer := integerFloat(number)
	if integer == nil {
		return 0, false
	}
	return *integer, true
}

func integerFloat(number float64) *int {
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number != math.Trunc(number) || number > float64(^uint(0)>>1) {
		return nil
	}
	result := int(number)
	return &result
}

func parseResources(value any, observation *Observation) {
	switch typed := value.(type) {
	case string:
		for kind, pattern := range resourceUsagePatterns {
			match := pattern.FindStringSubmatch(typed)
			if len(match) == 2 {
				setResource(observation, kind, parsePercent(match[1]))
			}
		}
		for kind, pattern := range resourcePatterns {
			match := pattern.FindStringSubmatch(typed)
			if len(match) == 2 {
				setResource(observation, kind, parsePercent(match[1]))
			}
		}
	case map[string]any:
		parseResourceMap(typed, "", observation)
	}
}

func parseResourceMap(values map[string]any, parent string, observation *Observation) {
	for key, value := range values {
		normalized := normalizeResourceKey(key)
		path := normalized
		if parent != "" {
			path = parent + "." + normalized
		}
		if nested, ok := value.(map[string]any); ok {
			parseResourceMap(nested, path, observation)
			continue
		}
		kind := classifyResourceKey(path)
		if kind != "" {
			setResource(observation, kind, percentValue(value))
		}
	}
}

func normalizeResourceKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.NewReplacer("-", "_", " ", "_").Replace(key)
}

func classifyResourceKey(key string) string {
	if containsAny(key, "vram", "显存", "gpu_memory", "gpu_mem") || (strings.Contains(key, "gpu") && containsAny(key, ".memory", ".mem")) {
		return "vram"
	}
	if containsAny(key, "cpu", "处理器") {
		return "cpu"
	}
	if containsAny(key, "gpu", "显卡") && containsAny(key, "usage", "util", "load", "rate", "percent") {
		return "gpu"
	}
	if key == "gpu" || key == "显卡" {
		return "gpu"
	}
	if containsAny(key, "memory", "ram", "内存") {
		return "memory"
	}
	return ""
}

func percentValue(value any) *float64 {
	if text, ok := value.(string); ok {
		match := percentNumberPattern.FindStringSubmatch(text)
		if len(match) != 2 {
			return nil
		}
		return parsePercent(match[1])
	}
	var number float64
	switch typed := value.(type) {
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case float32:
		number = float64(typed)
	case float64:
		number = typed
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return nil
		}
		number = parsed
	default:
		return nil
	}
	return validPercent(number)
}

func parsePercent(value string) *float64 {
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return nil
	}
	return validPercent(number)
}

func validPercent(number float64) *float64 {
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number > 100 {
		return nil
	}
	return &number
}

func setResource(observation *Observation, kind string, value *float64) {
	if value == nil {
		return
	}
	switch kind {
	case "cpu":
		observation.CPUPercent = value
	case "memory":
		observation.MemoryPercent = value
	case "gpu":
		observation.GPUPercent = value
	case "vram":
		observation.VRAMPercent = value
	}
}

func containsFailureKeyword(value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	text = strings.ToLower(text)
	return containsAny(text, "失败", "错误", "异常", "failed", "failure", "error")
}

func containsAny(value string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
