package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"minimax-h3-tc/internal/logsafe"
)

func TestReadableLogShowsTimestampLevelMessageAndFlatFields(t *testing.T) {
	var output bytes.Buffer
	handler := newReadableLogHandler(&output)
	record := slog.NewRecord(time.Date(2026, 10, 23, 10, 19, 9, 362000000, time.FixedZone("CST", 8*3600)), slog.LevelWarn, "远程任务素材准备失败", 0)
	record.AddAttrs(slog.String("task_id", "task-1"), slog.String("source", "object_storage"), slog.Int("http_status", 403), slog.Bool("retryable", false))
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	want := "2026-10-23 10:19:09.362 +0800 WARN  远程任务素材准备失败 | task_id=task-1 source=object_storage http_status=403 retryable=false\n"
	if output.String() != want {
		t.Fatalf("readable log:\n%s\nwant:\n%s", output.String(), want)
	}
}

func TestReadableLogFlattensSanitizedResponseWithoutJSON(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(newReadableLogHandler(&output))
	logger.Info("tk2sd 返回结果", "response", map[string]any{"status": "running", "error": nil, "details": []any{map[string]any{"type": "value_error"}, "[redacted]"}}, "request", map[string]any{})
	line := output.String()
	for _, field := range []string{"response.status=running", "response.error=<nil>", "response.details[0].type=value_error", "response.details[1]=[redacted]", "request=<empty>"} {
		if !strings.Contains(line, field) {
			t.Errorf("missing %s in %s", field, line)
		}
	}
	if strings.ContainsAny(line, "{}") || strings.Contains(line, "map[") {
		t.Fatal("JSON or map object leaked into readable output", line)
	}
}

func TestReadableLogPreservesExistingDiagnosticRedaction(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(newReadableLogHandler(&output))
	summary := logsafe.DiagnosticJSON([]byte(`{"status":"running","duration":10,"token":"private-token","prompt":"private-prompt","video_url":"https://private.example/result?signature=private-signature","message":"private-message"}`), "private-token")
	logger.Info("上游响应", "response", summary)
	for _, sensitive := range []string{"private-token", "private-prompt", "private.example", "private-signature", "private-message"} {
		if strings.Contains(output.String(), sensitive) {
			t.Error("sensitive diagnostic became visible", sensitive)
		}
	}
	if !strings.Contains(output.String(), "response.status=running") || !strings.Contains(output.String(), "response.duration=10") || !strings.Contains(output.String(), "response.prompt=[redacted]") {
		t.Fatal("safe diagnostic fields lost", output.String())
	}
}

func TestReadableLogExpandsOfficialResponseJSONSummary(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(newReadableLogHandler(&output))
	// 官方 V2 以已脱敏的 JSON 字符串传递摘要，而非 map。
	logger.Info("官方 V2 创建任务响应", "response", `{"task_id":"upstream-1","id":9007199254740993,"token":"[redacted]"}`, "request", `[{"type":"text","text":"[redacted]"}]`, "error_reason", `{"opaque":"unchanged"}`)
	for _, field := range []string{"response.task_id=upstream-1", "response.id=9007199254740993", "response.token=[redacted]", "request[0].type=text", "request[0].text=[redacted]"} {
		if !strings.Contains(output.String(), field) {
			t.Error("official response still contains encoded JSON or lost precision", field, output.String())
		}
	}
	if !strings.Contains(output.String(), `error_reason="{\"opaque\":\"unchanged\"}"`) {
		t.Fatal("non-summary string was reinterpreted", output.String())
	}
}

func TestReadableLogDerivedContextsDoNotLeakAcrossLoggers(t *testing.T) {
	var output bytes.Buffer
	base := slog.New(newReadableLogHandler(&output)).With("node_id", "node-1")
	scoped := base.WithGroup("task").With("id", "one")
	scoped.WithGroup("upstream").Info("派生任务", "http_status", 422)
	base.Info("基础任务", "task_id", "two")
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "node_id=node-1 task.id=one task.upstream.http_status=422") {
		t.Fatal("missing scoped attributes", output.String())
	}
	if strings.Contains(lines[1], "task.id=") || strings.Contains(lines[1], "upstream") || !strings.Contains(lines[1], "task_id=two") {
		t.Fatal("derived context leaked to parent", output.String())
	}
}

func TestReadableLogEscapesNewlinesAndPreservesHeaderNamedFields(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(newReadableLogHandler(&output))
	logger.Error("读取失败\n伪造记录\r\n", "error_reason", "失败\n原因", "level", "business-level", "msg", "business-message", "time", "business-time")
	line := output.String()
	if strings.Count(line, "\n") != 1 || strings.ContainsRune(line, '\r') || !strings.Contains(line, `读取失败\n伪造记录\r\n`) || !strings.Contains(line, `error_reason="失败\n原因"`) {
		t.Fatal("message or fields became multiple log records", line)
	}
	for _, field := range []string{"level=business-level", "msg=business-message", "time=business-time"} {
		if !strings.Contains(line, field) {
			t.Error("business field was removed with header", field)
		}
	}
}

func TestReadableLogConcurrentDerivedLoggersWriteWholeLines(t *testing.T) {
	var output bytes.Buffer
	base := slog.New(newReadableLogHandler(&output))
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			logger := base.With("worker", i)
			for j := 0; j < 10; j++ {
				logger.Info("任务日志", "entry", j)
			}
		}(i)
	}
	group.Wait()
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 200 {
		t.Fatal("lost or partial log records", len(lines))
	}
	for _, line := range lines {
		if strings.Count(line, "INFO  任务日志 | worker=") != 1 || !strings.Contains(line, " entry=") {
			t.Fatal("concurrent log records interleaved", line)
		}
	}
}

func TestReadableLogFiltersDebugAndBoundsRecursiveMaps(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(newReadableLogHandler(&output))
	logger.Debug("不应输出")
	if output.Len() != 0 {
		t.Fatal("debug logs enabled unexpectedly")
	}
	value := map[string]any{}
	value["self"] = value
	logger.Info("循环摘要", "response", value)
	if !strings.Contains(output.String(), "<depth-limit>") {
		t.Fatal("recursive summary was not bounded")
	}
}

type failedLogWriter struct{ err error }

func (w failedLogWriter) Write([]byte) (int, error) { return 0, w.err }

type shortLogWriter struct{}

func (shortLogWriter) Write(data []byte) (int, error) { return len(data) / 2, nil }

func TestReadableLogReportsOutputFailures(t *testing.T) {
	want := errors.New("writer unavailable")
	for _, test := range []struct {
		writer io.Writer
		want   error
	}{{failedLogWriter{err: want}, want}, {shortLogWriter{}, io.ErrShortWrite}} {
		handler := newReadableLogHandler(test.writer)
		if err := handler.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "测试日志", 0)); !errors.Is(err, test.want) {
			t.Fatal("output error lost", err)
		}
	}
}
