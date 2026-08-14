package logsafe

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var (
	httpURLPattern        = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
	dataURIPattern        = regexp.MustCompile(`(?i)data:[^\s"'<>]+`)
	networkAddressPattern = regexp.MustCompile(`(?i)\b((?:dial|lookup)(?:\s+(?:tcp|udp))?|connect(?:ing)? to)\s+[a-z0-9._-]+(?::\d+)?(?::)?`)
	ipv4Pattern           = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?(?::)?`)
)

// Error保留可操作的异常原因，同时移除日志中禁止出现的素材和私有地址。
func Error(err error) string {
	if err == nil {
		return "未知错误"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	message := err.Error()
	message = dataURIPattern.ReplaceAllString(message, "[redacted-data-uri]")
	message = httpURLPattern.ReplaceAllString(message, "[redacted-url]")
	message = networkAddressPattern.ReplaceAllString(message, "$1 [redacted-address]")
	message = ipv4Pattern.ReplaceAllString(message, "[redacted-address]")
	message = strings.TrimSpace(message)
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}
