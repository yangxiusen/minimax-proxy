package nodeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

type Client struct {
	baseURL *url.URL
	apiKey  string
	http    *http.Client
	maxBody int64
}

type HTTPError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *HTTPError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("节点 API HTTP %d: %s: %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("节点 API HTTP %d", e.StatusCode)
}

func NewClient(baseURL *url.URL, apiKey string, client *http.Client, maxBody int64) *Client {
	copyURL := *baseURL
	copyURL.RawQuery, copyURL.Fragment = "", ""
	if client == nil {
		client = http.DefaultClient
	}
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if maxBody <= 0 {
		maxBody = 1 << 20
	}
	return &Client{baseURL: &copyURL, apiKey: apiKey, http: &safeClient, maxBody: maxBody}
}

func (c *Client) Health(ctx context.Context, requestID string) (Health, error) {
	var health Health
	err := c.request(ctx, http.MethodGet, []string{"internal", "v1", "health"}, requestID, "", nil, &health)
	if err == nil && health.ProtocolVersion != ProtocolVersion {
		return Health{}, fmt.Errorf("节点协议版本 %q 不兼容", health.ProtocolVersion)
	}
	return health, err
}

func (c *Client) Capabilities(ctx context.Context, requestID string) (Capabilities, error) {
	var raw map[string]any
	if err := c.request(ctx, http.MethodGet, []string{"internal", "v1", "capabilities"}, requestID, "", nil, &raw); err != nil {
		return Capabilities{}, err
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return Capabilities{}, err
	}
	var result Capabilities
	if err := json.Unmarshal(data, &result); err != nil {
		return Capabilities{}, err
	}
	if result.ProtocolVersion != ProtocolVersion {
		return Capabilities{}, fmt.Errorf("节点能力协议版本 %q 不兼容", result.ProtocolVersion)
	}
	result.Raw = raw
	return result, nil
}

func (c *Client) CreateExecution(ctx context.Context, requestID string, input ExecutionRequest) (ExecutionReference, error) {
	var result ExecutionReference
	err := c.request(ctx, http.MethodPost, []string{"internal", "v1", "executions"}, requestID, input.OperationID, input, &result)
	return result, err
}

func (c *Client) GetExecution(ctx context.Context, requestID, executionID string) (Execution, error) {
	var result Execution
	err := c.request(ctx, http.MethodGet, []string{"internal", "v1", "executions", executionID}, requestID, "", nil, &result)
	return result, err
}

func (c *Client) CancelExecution(ctx context.Context, requestID, executionID, operationID string) (ExecutionReference, error) {
	var result ExecutionReference
	err := c.request(ctx, http.MethodPost, []string{"internal", "v1", "executions", executionID, "cancel"}, requestID, operationID, nil, &result)
	return result, err
}

func (c *Client) GetArtifact(ctx context.Context, requestID, artifactID string) (Artifact, error) {
	var result Artifact
	err := c.request(ctx, http.MethodGet, []string{"internal", "v1", "artifacts", artifactID}, requestID, "", nil, &result)
	return result, err
}

func (c *Client) GetArtifactContent(ctx context.Context, requestID, artifactID, rangeHeader string) (*ArtifactContent, error) {
	request, err := c.newRequest(ctx, http.MethodGet, []string{"internal", "v1", "artifacts", artifactID, "content"}, requestID, "", nil, "")
	if err != nil {
		return nil, err
	}
	if rangeHeader != "" {
		request.Header.Set("Range", rangeHeader)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("节点 API 请求失败: %w", err)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		defer response.Body.Close()
		return nil, c.responseError(response)
	}
	return &ArtifactContent{
		Body: response.Body, StatusCode: response.StatusCode, ContentLength: response.ContentLength,
		ContentRange: response.Header.Get("Content-Range"), ContentType: response.Header.Get("Content-Type"),
		ETag: response.Header.Get("ETag"),
	}, nil
}

func (c *Client) ImportArtifact(ctx context.Context, requestID string, input ImportArtifactRequest) (Artifact, error) {
	var result Artifact
	if input.Content == nil {
		return result, errors.New("导入产物内容不能为空")
	}
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	contentType := multipartWriter.FormDataContentType()
	go func() {
		writeErr := writeImportMultipart(multipartWriter, input)
		if closeErr := multipartWriter.Close(); writeErr == nil {
			writeErr = closeErr
		}
		_ = writer.CloseWithError(writeErr)
	}()
	request, err := c.newRequest(ctx, http.MethodPost, []string{"internal", "v1", "artifacts", "import"}, requestID, input.OperationID, reader, contentType)
	if err != nil {
		_ = reader.Close()
		return result, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		_ = reader.Close()
		return result, fmt.Errorf("节点 API 请求失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, c.responseError(response)
	}
	if err := c.decodeResponse(response.Body, &result); err != nil {
		return Artifact{}, err
	}
	return result, nil
}

func (c *Client) DeleteArtifacts(ctx context.Context, requestID string, input DeleteArtifactsRequest) (DeleteArtifactsResult, error) {
	var result DeleteArtifactsResult
	err := c.request(ctx, http.MethodPost, []string{"internal", "v1", "artifacts", "delete"}, requestID, input.OperationID, input, &result)
	return result, err
}

func (c *Client) request(ctx context.Context, method string, parts []string, requestID, idempotencyKey string, input, output any) error {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("编码节点请求: %w", err)
		}
		body = bytes.NewReader(payload)
	}
	request, err := c.newRequest(ctx, method, parts, requestID, idempotencyKey, body, "application/json")
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("节点 API 请求失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return c.responseError(response)
	}
	if output == nil {
		_, err := io.Copy(io.Discard, io.LimitReader(response.Body, c.maxBody+1))
		return err
	}
	return c.decodeResponse(response.Body, output)
}

func (c *Client) newRequest(ctx context.Context, method string, parts []string, requestID, idempotencyKey string, body io.Reader, contentType string) (*http.Request, error) {
	endpoint := *c.baseURL
	segments := append([]string{endpoint.Path}, parts...)
	endpoint.Path = path.Join(segments...)
	endpoint.RawPath, endpoint.RawQuery, endpoint.Fragment = "", "", ""
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Accept", "application/json")
	if contentType != "" && body != nil {
		request.Header.Set("Content-Type", contentType)
	}
	if requestID != "" {
		request.Header.Set("X-Request-Id", requestID)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	return request, nil
}

func (c *Client) decodeResponse(body io.Reader, output any) error {
	data, err := io.ReadAll(io.LimitReader(body, c.maxBody+1))
	if err != nil {
		return fmt.Errorf("读取节点 API 响应: %w", err)
	}
	if int64(len(data)) > c.maxBody {
		return errors.New("节点 API 响应体超过限制")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return fmt.Errorf("节点 API 响应包含未知字段: %w", err)
		}
		return fmt.Errorf("解析节点 API 响应: %w", err)
	}
	return nil
}

func (c *Client) responseError(response *http.Response) error {
	data, err := io.ReadAll(io.LimitReader(response.Body, c.maxBody+1))
	if err != nil {
		return fmt.Errorf("读取节点 API 响应: %w", err)
	}
	if int64(len(data)) > c.maxBody {
		return errors.New("节点 API 响应体超过限制")
	}
	var envelope errorEnvelope
	if json.Unmarshal(data, &envelope) == nil && envelope.Error.Code != "" {
		return &HTTPError{StatusCode: response.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message}
	}
	return &HTTPError{StatusCode: response.StatusCode}
}

func writeImportMultipart(writer *multipart.Writer, input ImportArtifactRequest) error {
	fields := map[string]string{
		"operation_id": input.OperationID, "source_artifact_id": input.SourceArtifactID,
		"expected_size": strconv.FormatInt(input.ExpectedSize, 10), "expected_sha256": input.ExpectedSHA256,
		"kind": input.Kind,
	}
	if input.ExternalTaskID != "" {
		fields["external_task_id"] = input.ExternalTaskID
	}
	for _, key := range []string{"operation_id", "source_artifact_id", "expected_size", "expected_sha256", "kind", "external_task_id"} {
		if fields[key] == "" {
			continue
		}
		if err := writer.WriteField(key, fields[key]); err != nil {
			return err
		}
	}
	filename := path.Base(input.Filename)
	if filename == "." || filename == "/" || filename == "" {
		filename = "artifact.bin"
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return err
	}
	_, err = io.Copy(part, input.Content)
	return err
}
