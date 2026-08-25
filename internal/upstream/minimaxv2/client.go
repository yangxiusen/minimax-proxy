package minimaxv2

import (
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

const ProtocolVersion = "minimax-v2"

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

type Content struct {
	URL string `json:"url"`
}

type Task struct {
	ID         string     `json:"id"`
	Model      string     `json:"model"`
	Status     Status     `json:"status"`
	Content    Content    `json:"content"`
	Resolution string     `json:"resolution"`
	Duration   int        `json:"duration"`
	Ratio      string     `json:"ratio"`
	CreatedAt  int64      `json:"created_at"`
	UpdatedAt  int64      `json:"updated_at"`
	Error      *TaskError `json:"error,omitempty"`
}

type TaskError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type HTTPError struct {
	StatusCode int
	Code       string
	RequestID  string
}

func (err *HTTPError) Error() string {
	return fmt.Sprintf("MiniMax V2 HTTP %d (%s)", err.StatusCode, err.Code)
}

type Client struct {
	baseURL *url.URL
	apiKey  string
	model   string
	http    *http.Client
	maxBody int64
}

func NewClient(baseURL *url.URL, apiKey, model string, client *http.Client, maxBody int64) *Client {
	copyURL := *baseURL
	copyURL.RawQuery, copyURL.Fragment = "", ""
	copyURL.Path = strings.TrimRight(copyURL.Path, "/")
	if client == nil {
		client = http.DefaultClient
	}
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if maxBody <= 0 {
		maxBody = 1 << 20
	}
	return &Client{baseURL: &copyURL, apiKey: apiKey, model: model, http: &safeClient, maxBody: maxBody}
}

type createRequest struct {
	Model         string          `json:"model"`
	Content       json.RawMessage `json:"content"`
	Resolution    string          `json:"resolution"`
	Duration      int             `json:"duration"`
	Ratio         string          `json:"ratio,omitempty"`
	AIGCWatermark *bool           `json:"aigc_watermark,omitempty"`
}

func (client *Client) Submit(ctx context.Context, requestJSON []byte) (string, error) {
	var input createRequest
	if err := json.Unmarshal(requestJSON, &input); err != nil {
		return "", fmt.Errorf("解析 V2 任务快照: %w", err)
	}
	if len(input.Content) == 0 || input.Resolution == "" || input.Duration == 0 {
		return "", errors.New("V2 任务快照缺少必填字段")
	}
	input.Model = client.model
	body, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	var response struct {
		TaskID string `json:"task_id"`
	}
	if err := client.request(ctx, http.MethodPost, []string{"v2", "video_generation"}, body, &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.TaskID) == "" {
		return "", errors.New("MiniMax V2 创建响应缺少 task_id")
	}
	return response.TaskID, nil
}

func (client *Client) Query(ctx context.Context, taskID string) (Task, error) {
	if !validTaskID(taskID) {
		return Task{}, errors.New("MiniMax V2 task_id 无效")
	}
	var response struct {
		Task Task `json:"task"`
	}
	if err := client.request(ctx, http.MethodGet, []string{"v2", "query", "video_generation", taskID}, nil, &response); err != nil {
		return Task{}, err
	}
	if response.Task.ID == "" || response.Task.Status == "" {
		return Task{}, errors.New("MiniMax V2 查询响应缺少关键字段")
	}
	if response.Task.Status == StatusSucceeded {
		resultURL, err := url.Parse(response.Task.Content.URL)
		if err != nil || resultURL.Scheme != "https" || resultURL.Host == "" {
			return Task{}, errors.New("MiniMax V2 成功响应缺少有效 HTTPS 结果地址")
		}
	}
	return response.Task, nil
}

func (client *Client) List(ctx context.Context) ([]Task, error) {
	var response struct {
		Tasks json.RawMessage `json:"tasks"`
		Items json.RawMessage `json:"items"`
	}
	if err := client.request(ctx, http.MethodGet, []string{"v2", "query", "video_generation"}, nil, &response); err != nil {
		return nil, err
	}
	wrapped := response.Tasks
	field := "tasks"
	if len(wrapped) == 0 {
		wrapped = response.Items
		field = "items"
	}
	if len(wrapped) == 0 {
		return nil, errors.New("MiniMax V2 列表响应缺少 tasks 或 items")
	}
	var tasks []Task
	if err := json.Unmarshal(wrapped, &tasks); err != nil || tasks == nil {
		return nil, fmt.Errorf("MiniMax V2 列表响应字段 %s 不是数组", field)
	}
	return tasks, nil
}

func (client *Client) Delete(ctx context.Context, taskID string) error {
	if !validTaskID(taskID) {
		return errors.New("MiniMax V2 task_id 无效")
	}
	return client.request(ctx, http.MethodDelete, []string{"v2", "video_generation", taskID}, nil, nil)
}

func (client *Client) request(ctx context.Context, method string, segments []string, body []byte, output any) error {
	target := *client.baseURL
	parts := append([]string{target.Path}, segments...)
	target.Path = path.Join(parts...)
	if !strings.HasPrefix(target.Path, "/") {
		target.Path = "/" + target.Path
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.apiKey)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, client.maxBody+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > client.maxBody {
		return errors.New("MiniMax V2 响应超过大小限制")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
			RequestID string `json:"request_id"`
		}
		_ = json.Unmarshal(data, &failure)
		return &HTTPError{StatusCode: response.StatusCode, Code: failure.Error.Type, RequestID: failure.RequestID}
	}
	if output == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("解析 MiniMax V2 响应: %w", err)
	}
	return nil
}

func validTaskID(value string) bool {
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, "/\\?#")
}
