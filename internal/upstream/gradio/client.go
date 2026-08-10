package gradio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

var (
	ErrRequestRejected = errors.New("Gradio 明确拒绝请求")
	ErrResultAmbiguous = errors.New("无法识别唯一生成视频")
)

type Client struct {
	baseURL     *url.URL
	jobsBaseURL *url.URL
	http        *http.Client
	maxBody     int64
}

func NewClient(baseURL *url.URL, client *http.Client, maxBody int64) *Client {
	return NewClientWithJobs(baseURL, baseURL, client, maxBody)
}

func NewClientWithJobs(baseURL, jobsBaseURL *url.URL, client *http.Client, maxBody int64) *Client {
	copyURL := *baseURL
	jobsCopy := *jobsBaseURL
	if client == nil {
		client = http.DefaultClient
	}
	if maxBody <= 0 {
		maxBody = 8 << 20
	}
	return &Client{baseURL: &copyURL, jobsBaseURL: &jobsCopy, http: client, maxBody: maxBody}
}

func (c *Client) Call(ctx context.Context, apiName string, data []any) ([]any, error) {
	apiName = strings.Trim(apiName, "/")
	if apiName == "" || strings.Contains(apiName, "/") {
		return nil, errors.New("Gradio api_name 无效")
	}
	endpoint := c.endpoint("gradio_api", "call", apiName)
	payload, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Gradio POST: %w", err)
	}
	var event struct {
		EventID string `json:"event_id"`
	}
	if err := decodeLimited(response, c.maxBody, &event); err != nil {
		return nil, fmt.Errorf("Gradio POST 响应: %w", err)
	}
	if event.EventID == "" {
		return nil, errors.New("Gradio 未返回 event_id")
	}
	resultURL := c.endpoint("gradio_api", "call", apiName, event.EventID)
	get, err := http.NewRequestWithContext(ctx, http.MethodGet, resultURL.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err = c.http.Do(get)
	if err != nil {
		return nil, fmt.Errorf("Gradio SSE: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Gradio SSE HTTP %d", response.StatusCode)
	}
	return decodeSSE(io.LimitReader(response.Body, c.maxBody+1), c.maxBody)
}

func (c *Client) Healthy(ctx context.Context, healthPath string) error {
	endpoint := *c.baseURL
	endpoint.Path = path.Join(endpoint.Path, healthPath)
	endpoint.RawQuery, endpoint.Fragment = "", ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return fmt.Errorf("健康检查 HTTP %d", response.StatusCode)
	}
	return nil
}

func (c *Client) endpoint(parts ...string) *url.URL {
	return endpoint(c.baseURL, parts...)
}

func (c *Client) jobsEndpoint(parts ...string) *url.URL {
	return endpoint(c.jobsBaseURL, parts...)
}

func endpoint(baseURL *url.URL, parts ...string) *url.URL {
	result := *baseURL
	segments := append([]string{result.Path}, parts...)
	result.Path = path.Join(segments...)
	result.RawPath, result.RawQuery, result.Fragment = "", "", ""
	return &result
}

func decodeLimited(response *http.Response, limit int64, target any) error {
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	reader := io.LimitReader(response.Body, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("响应体超过限制")
	}
	if err := json.Unmarshal(data, target); err != nil {
		return err
	}
	return nil
}

func decodeSSE(reader io.Reader, limit int64) ([]any, error) {
	scanner := bufio.NewScanner(reader)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, int(limit)+1)
	event := ""
	dataLines := make([]string, 0)
	var terminalOutput []any
	var terminalErr error
	terminalSeen := false
	flush := func() ([]any, error, bool) {
		if event == "error" {
			return nil, fmt.Errorf("%w: %s", ErrRequestRejected, strings.Join(dataLines, "\n")), true
		}
		if event != "complete" {
			return nil, nil, false
		}
		var output []any
		if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &output); err != nil {
			return nil, fmt.Errorf("解析 Gradio complete: %w", err), true
		}
		return output, nil, true
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if output, err, done := flush(); done {
				// 终止事件后继续读取到 EOF，避免 Windows 服务端将提前关闭记录为连接重置。
				if !terminalSeen {
					terminalOutput, terminalErr, terminalSeen = output, err, true
				}
			}
			event, dataLines = "", dataLines[:0]
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if output, err, done := flush(); done {
		if !terminalSeen {
			terminalOutput, terminalErr, terminalSeen = output, err, true
		}
	}
	if terminalSeen {
		return terminalOutput, terminalErr
	}
	return nil, errors.New("Gradio SSE 缺少 complete/error 终止事件")
}
