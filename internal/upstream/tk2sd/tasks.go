package tk2sd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type TaskError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Task struct {
	ID         string
	Model      string
	Status     string
	VideoURL   string
	Error      *TaskError
	Ratio      string
	Resolution string
}

type Metadata struct {
	Duration float64 `json:"duration"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
}

type submission struct {
	Model    string              `json:"model"`
	Content  []submissionContent `json:"content"`
	Duration int                 `json:"duration"`
}

type assetReference struct {
	URL string `json:"url"`
}
type submissionContent struct {
	Type     string          `json:"type"`
	Text     *string         `json:"text,omitempty"`
	ImageURL *assetReference `json:"image_url,omitempty"`
	VideoURL *assetReference `json:"video_url,omitempty"`
	AudioURL *assetReference `json:"audio_url,omitempty"`
	Role     string          `json:"role,omitempty"`
}

func (c *Client) Submit(ctx context.Context, body []byte, idempotencyKey string) (string, error) {
	if int64(len(body)) > c.maxBody {
		return "", ErrBodyTooLarge
	}
	if !headerValue(idempotencyKey, 200) || !validSubmission(body) {
		return "", ErrInvalidRequest
	}
	r, err := c.newRequest(ctx, http.MethodPost, tasksPath, bytes.NewReader(body), "application/json")
	if err != nil {
		return "", err
	}
	r.Header.Set("Idempotency-Key", idempotencyKey)
	response, err := c.do(r)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var result struct {
		ID string `json:"id"`
	}
	if err := c.decode(response, http.StatusOK, &result, false); err != nil {
		return "", err
	}
	if !validID(result.ID) {
		c.logTaskID(ctx, result.ID, false)
		return "", ErrInvalidResponse
	}
	c.logTaskID(ctx, result.ID, true)
	return result.ID, nil
}

func validSubmission(body []byte) bool {
	var input submission
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || !validName(input.Model) || input.Duration < 4 || input.Duration > 15 || len(input.Content) == 0 || len(input.Content) > 13 {
		return false
	}
	texts := 0
	for _, item := range input.Content {
		if item.Type == "text" {
			if item.Text == nil || strings.TrimSpace(*item.Text) == "" || item.Role != "" || item.ImageURL != nil || item.VideoURL != nil || item.AudioURL != nil {
				return false
			}
			texts++
			continue
		}
		if item.Text != nil {
			return false
		}
		var ref *assetReference
		switch item.Type {
		case "image_url":
			if item.VideoURL != nil || item.AudioURL != nil || (item.Role != "" && item.Role != "first_frame" && item.Role != "reference_image") {
				return false
			}
			ref = item.ImageURL
		case "video_url":
			if item.ImageURL != nil || item.AudioURL != nil || (item.Role != "" && item.Role != "reference_video") {
				return false
			}
			ref = item.VideoURL
		case "audio_url":
			if item.ImageURL != nil || item.VideoURL != nil || (item.Role != "" && item.Role != "reference_audio") {
				return false
			}
			ref = item.AudioURL
		default:
			return false
		}
		if ref == nil || !strings.HasPrefix(ref.URL, "asset://") || !validAssetID(strings.TrimPrefix(ref.URL, "asset://")) {
			return false
		}
	}
	return texts == 1
}

func (c *Client) Query(ctx context.Context, id string) (Task, error) {
	if !validID(id) {
		return Task{}, ErrInvalidRequest
	}
	var result struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Status  string `json:"status"`
		Content struct {
			VideoURL string `json:"video_url"`
		} `json:"content"`
		Error      *TaskError `json:"error"`
		Ratio      string     `json:"ratio"`
		Resolution string     `json:"resolution"`
	}
	if err := c.request(ctx, http.MethodGet, tasksPath+"/"+id, &result, true); err != nil {
		return Task{}, err
	}
	if result.ID != id || !validName(result.Model) {
		return Task{}, ErrInvalidResponse
	}
	switch result.Status {
	case "queued", "running", "succeeded", "failed", "cancelled":
	default:
		return Task{}, ErrInvalidResponse
	}
	if result.Status == "failed" {
		if result.Error == nil || result.Error.Code == "" || result.Error.Message == "" {
			return Task{}, ErrInvalidResponse
		}
		result.Error.Code = c.safeText(result.Error.Code, 128)
		result.Error.Message = c.safeText(result.Error.Message, 512)
	}
	videoURL := ""
	if result.Status == "succeeded" {
		if err := c.validateVideoURL(ctx, id, result.Content.VideoURL); err != nil {
			return Task{}, err
		}
		videoURL = result.Content.VideoURL
	}
	return Task{ID: result.ID, Model: result.Model, Status: result.Status, VideoURL: videoURL, Error: result.Error, Ratio: result.Ratio, Resolution: result.Resolution}, nil
}

func (c *Client) Metadata(ctx context.Context, id string) (Metadata, error) {
	if !validID(id) {
		return Metadata{}, ErrInvalidRequest
	}
	var result struct {
		ID      string   `json:"id"`
		Status  string   `json:"status"`
		Content Metadata `json:"content"`
	}
	if err := c.request(ctx, http.MethodGet, "/v1/tasks/"+id, &result, false); err != nil {
		return Metadata{}, err
	}
	if result.ID != id || result.Status != "succeeded" || !positiveFinite(result.Content.Duration) || result.Content.Width <= 0 || result.Content.Height <= 0 {
		return Metadata{}, ErrInvalidResponse
	}
	return result.Content, nil
}

func (c *Client) Cancel(ctx context.Context, id string) error {
	if !validID(id) {
		return ErrInvalidRequest
	}
	r, err := c.newRequest(ctx, http.MethodDelete, tasksPath+"/"+id, nil, "")
	if err != nil {
		return err
	}
	response, err := c.do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return c.decode(response, http.StatusNoContent, nil, false)
}

func positiveFinite(v float64) bool { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }

func (c *Client) validateVideoURL(ctx context.Context, id, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawFragment != "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ErrUnsafeURL
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["expires"]) != 1 || len(query["signature"]) != 1 || !headerValue(query.Get("signature"), 512) {
		return ErrUnsafeURL
	}
	expires, err := strconv.ParseInt(query.Get("expires"), 10, 64)
	if err != nil || expires <= time.Now().Unix() {
		return ErrUnsafeURL
	}
	if sameOrigin(c.baseURL, u) {
		if u.Path != c.baseURL.Path+"/media/tasks/"+id || u.RawPath != "" {
			return ErrUnsafeURL
		}
		return nil
	}
	if u.Scheme != "https" {
		return ErrUnsafeURL
	}
	// 公网链接仅校验；下载始终走原节点固定端点，不请求此 URL。
	if _, err := c.guard.Validate(ctx, raw); err != nil {
		return ErrUnsafeURL
	}
	return nil
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}
