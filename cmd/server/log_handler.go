package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type readableLogOutput struct {
	mu     sync.Mutex
	writer io.Writer
}

type readableLogHandler struct {
	output *readableLogOutput
	attrs  []slog.Attr
	group  string
}

func newReadableLogHandler(writer io.Writer) slog.Handler {
	return &readableLogHandler{output: &readableLogOutput{writer: writer}}
}

func (h *readableLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}

func (h *readableLogHandler) Handle(ctx context.Context, record slog.Record) error {
	var fields bytes.Buffer
	levelWritten, messageWritten := false, false
	text := slog.NewTextHandler(&fields, &slog.HandlerOptions{ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
		// 仅移除文本处理器的内置头，保留业务中同名的 level、msg 和 time 字段。
		if attr.Key == slog.LevelKey && !levelWritten {
			levelWritten = true
			return slog.Attr{}
		}
		if attr.Key == slog.MessageKey && !messageWritten {
			messageWritten = true
			return slog.Attr{}
		}
		return attr
	}})
	attributes := slog.NewRecord(time.Time{}, record.Level, "", 0)
	attributes.AddAttrs(h.attrs...)
	record.Attrs(func(attr slog.Attr) bool {
		attributes.AddAttrs(flatLogAttrs(nil, h.group, attr, 0)...)
		return true
	})
	if err := text.Handle(ctx, attributes); err != nil {
		return err
	}
	var line bytes.Buffer
	// 转义消息中的控制字符，避免一条记录被换行拆成多条；字段转义交给标准库。
	message := strconv.Quote(record.Message)
	fmt.Fprintf(&line, "%s %-5s %s", record.Time.Format("2006-01-02 15:04:05.000 -0700"), record.Level.String(), message[1:len(message)-1])
	if values := strings.TrimSuffix(fields.String(), "\n"); values != "" {
		line.WriteString(" | ")
		line.WriteString(values)
	}
	line.WriteByte('\n')
	// 派生 logger 共享输出锁，使 stdout 与按日文件中的整行记录保持一致。
	h.output.mu.Lock()
	defer h.output.mu.Unlock()
	n, err := h.output.writer.Write(line.Bytes())
	if err == nil && n != line.Len() {
		err = io.ErrShortWrite
	}
	return err
}

func (h *readableLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	copy := *h
	copy.attrs = append([]slog.Attr(nil), h.attrs...)
	for _, attr := range attrs {
		copy.attrs = flatLogAttrs(copy.attrs, h.group, attr, 0)
	}
	return &copy
}

func (h *readableLogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	copy := *h
	copy.group = logFieldKey(h.group, name)
	return &copy
}

func flatLogAttrs(attrs []slog.Attr, prefix string, attr slog.Attr, depth int) []slog.Attr {
	if attr.Equal(slog.Attr{}) {
		return attrs
	}
	key := logFieldKey(prefix, attr.Key)
	value := attr.Value.Resolve()
	// 兼容官方 V2 已脱敏但以 JSON 字符串传入的摘要，只处理根层请求/响应字段。
	if depth == 0 && value.Kind() == slog.KindString && (attr.Key == "request" || attr.Key == "response") {
		raw := strings.TrimSpace(value.String())
		if len(raw) > 0 && len(raw) <= 64<<10 && (raw[0] == '{' || raw[0] == '[') && json.Valid([]byte(raw)) {
			decoder := json.NewDecoder(strings.NewReader(raw))
			decoder.UseNumber()
			var summary any
			if decoder.Decode(&summary) == nil {
				value = slog.AnyValue(summary)
			}
		}
	}
	if depth >= 8 {
		return append(attrs, slog.String(key, "<depth-limit>"))
	}
	if value.Kind() == slog.KindGroup {
		for _, child := range value.Group() {
			attrs = flatLogAttrs(attrs, key, child, depth+1)
		}
		return attrs
	}
	if value.Kind() == slog.KindAny {
		switch children := value.Any().(type) {
		case map[string]any:
			keys := make([]string, 0, len(children))
			for key := range children {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, name := range keys {
				attrs = flatLogAttrs(attrs, key, slog.Any(name, children[name]), depth+1)
			}
			if len(keys) == 0 {
				attrs = append(attrs, slog.String(key, "<empty>"))
			}
			return attrs
		case []any:
			for i, child := range children {
				attrs = flatLogAttrs(attrs, "", slog.Any(fmt.Sprintf("%s[%d]", key, i), child), depth+1)
			}
			if len(children) == 0 {
				attrs = append(attrs, slog.String(key, "<empty>"))
			}
			return attrs
		}
	}
	return append(attrs, slog.Attr{Key: key, Value: value})
}

func logFieldKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	if key == "" {
		return prefix
	}
	return prefix + "." + key
}
