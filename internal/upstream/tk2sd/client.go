package tk2sd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"

	"minimax-h3-tc/internal/netguard"
)

const tasksPath = "/api/v3/contents/generations/tasks"
const maxJSONBytes int64 = 1 << 20

type ErrorClass string

const (
	ErrorAuthentication ErrorClass = "authentication"
	ErrorConflict       ErrorClass = "conflict"
	ErrorNotFound       ErrorClass = "not_found"
	ErrorRejected       ErrorClass = "rejected"
	ErrorTemporary      ErrorClass = "temporary"
	ErrorRedirect       ErrorClass = "redirect"
	ErrorResponse       ErrorClass = "invalid_response"
	ErrorTransport      ErrorClass = "transport"
)

var (
	ErrInvalidRequest  = errors.New("tk2sd: invalid request")
	ErrInvalidResponse = errors.New("tk2sd: invalid response")
	ErrIncompleteVideo = errors.New("tk2sd: incomplete video response")
	ErrBodyTooLarge    = errors.New("tk2sd: body exceeds limit")
	ErrUnsafeURL       = errors.New("tk2sd: unsafe media URL")
	ErrUploadSource    = errors.New("tk2sd: upload source failed or empty")
)

type HTTPError struct {
	StatusCode int
	Code       string
	Message    string
	Class      ErrorClass
	cause      error
}

// 错误字符串不包含上游文本、地址或凭据，供日志安全使用。
func (e *HTTPError) Error() string { return fmt.Sprintf("tk2sd HTTP %d (%s)", e.StatusCode, e.Class) }
func (e *HTTPError) Unwrap() error { return e.cause }

type Client struct {
	baseURL   *url.URL
	apiKey    string
	http      *http.Client
	maxBody   int64
	guard     *netguard.Guard
	configErr error
	logger    *slog.Logger
}

func NewClient(baseURL *url.URL, apiKey string, httpClient *http.Client, maxBody int64) *Client {
	c := &Client{apiKey: apiKey, guard: netguard.New(netguard.Options{})}
	if baseURL == nil {
		c.configErr = ErrInvalidRequest
	} else {
		copyURL := *baseURL
		c.baseURL = &copyURL
		if (copyURL.Scheme != "http" && copyURL.Scheme != "https") || copyURL.Hostname() == "" || copyURL.User != nil || copyURL.Opaque != "" || copyURL.RawQuery != "" || copyURL.ForceQuery || copyURL.Fragment != "" || copyURL.RawFragment != "" || copyURL.RawPath != "" || strings.ContainsAny(copyURL.Path, "\\%") || (copyURL.Path != "" && path.Clean(copyURL.Path) != strings.TrimRight(copyURL.Path, "/") && copyURL.Path != "/") {
			c.configErr = ErrInvalidRequest
		}
		c.baseURL.Path = strings.TrimRight(copyURL.Path, "/")
	}
	if !headerValue(apiKey, 4096) {
		c.configErr = ErrInvalidRequest
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	safeClient := *httpClient
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	safeClient.Jar = nil
	if safeClient.Timeout <= 0 {
		safeClient.Timeout = 30 * time.Second
	}
	c.http = &safeClient
	if maxBody <= 0 || maxBody > maxJSONBytes {
		maxBody = maxJSONBytes
	}
	c.maxBody = maxBody
	return c
}

func (c *Client) newRequest(ctx context.Context, method, endpoint string, body io.Reader, contentType string) (*http.Request, error) {
	if c.configErr != nil {
		return nil, c.configErr
	}
	target := *c.baseURL
	target.Path += endpoint
	r, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	r.Header.Set("Authorization", "Bearer "+c.apiKey)
	r.Header.Set("Accept", "application/json")
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	return r, nil
}

func (c *Client) do(r *http.Request) (*http.Response, error) {
	c.logRequest(r)
	response, err := c.http.Do(r)
	if err == nil {
		if response.Request == nil {
			response.Request = r
		}
		return response, nil
	}
	var cause error
	if errors.Is(err, context.Canceled) {
		cause = context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		cause = context.DeadlineExceeded
	}
	if errors.Is(err, ErrUploadSource) {
		cause = ErrUploadSource
	}
	if errors.Is(err, ErrBodyTooLarge) {
		cause = ErrBodyTooLarge
	}
	if r.Context().Err() != nil {
		cause = r.Context().Err()
	}
	failure := &HTTPError{Class: ErrorTransport, Message: "request failed", cause: cause}
	c.logTransportError(r, failure)
	return nil, failure
}

func (c *Client) request(ctx context.Context, method, endpoint string, output any, allowTaskError bool) error {
	r, err := c.newRequest(ctx, method, endpoint, nil, "")
	if err != nil {
		return err
	}
	response, err := c.do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return c.decode(response, http.StatusOK, output, allowTaskError)
}

func classify(status int) ErrorClass {
	switch {
	case status == 401 || status == 403:
		return ErrorAuthentication
	case status == 404:
		return ErrorNotFound
	case status == 409:
		return ErrorConflict
	case status == 408 || status == 425 || status == 429 || status >= 500:
		return ErrorTemporary
	case status >= 300 && status < 400:
		return ErrorRedirect
	case status >= 400 && status < 500:
		return ErrorRejected
	default:
		return ErrorResponse
	}
}

func responseError(status int, cause error) *HTTPError {
	return &HTTPError{StatusCode: status, Class: classify(status), Message: "upstream response rejected", cause: cause}
}

func (c *Client) decode(response *http.Response, expected int, output any, allowTaskError bool) (resultErr error) {
	data, err := io.ReadAll(io.LimitReader(response.Body, c.maxBody+1))
	defer func() { c.logResponse(response, data, resultErr) }()
	if err != nil {
		cause := ErrInvalidResponse
		if errors.Is(err, context.Canceled) {
			cause = context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			cause = context.DeadlineExceeded
		}
		return responseError(response.StatusCode, cause)
	}
	if int64(len(data)) > c.maxBody {
		return responseError(response.StatusCode, ErrBodyTooLarge)
	}
	if response.StatusCode == expected && expected == http.StatusNoContent && len(data) == 0 {
		return nil
	}
	var envelope map[string]json.RawMessage
	decodeErr := json.Unmarshal(data, &envelope)
	if response.StatusCode != expected || decodeErr != nil || envelope == nil {
		return c.remoteError(response.StatusCode, envelope, ErrInvalidResponse)
	}
	if raw := envelope["error"]; len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var status string
		_ = json.Unmarshal(envelope["status"], &status)
		if !allowTaskError || status != "failed" {
			return c.remoteError(response.StatusCode, envelope, nil)
		}
	}
	if output == nil || json.Unmarshal(data, output) != nil {
		return responseError(response.StatusCode, ErrInvalidResponse)
	}
	return nil
}

func (c *Client) remoteError(status int, envelope map[string]json.RawMessage, cause error) error {
	e := responseError(status, cause)
	var detail TaskError
	if json.Unmarshal(envelope["error"], &detail) != nil || (detail.Code == "" && detail.Message == "") {
		if json.Unmarshal(envelope["detail"], &detail) != nil {
			_ = json.Unmarshal(envelope["detail"], &detail.Message)
		}
	}
	e.Code = c.safeText(detail.Code, 128)
	if detail.Message != "" {
		e.Message = c.safeText(detail.Message, 512)
	}
	return e
}

var sensitiveText = regexp.MustCompile(`(?i)(https?://|asset://|\bbearer\b|\bsignature\b|\bapi[_-]?key\b|\btoken\b|\bcookie\b)`)

func (c *Client) safeText(value string, limit int) string {
	return sanitizeText(value, limit, c.apiKey)
}

func SafeFeedbackMessage(value string) string {
	message := strings.TrimSpace(sanitizeText(value, 512, ""))
	if message == "[redacted]" {
		return ""
	}
	return message
}

func sanitizeText(value string, limit int, apiKey string) string {
	if (apiKey != "" && strings.Contains(value, apiKey)) || sensitiveText.MatchString(value) {
		return "[redacted]"
	}
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit])
	}
	return value
}

func headerValue(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, r := range s {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func validName(s string) bool {
	return strings.TrimSpace(s) == s && s != "" && len(s) <= 256 && !strings.ContainsFunc(s, unicode.IsControl)
}
