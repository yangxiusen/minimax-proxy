package tk2sd

import (
	"context"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"minimax-h3-tc/internal/logsafe"
)

const maxVideoBytes int64 = 2 << 30

func (c *Client) Download(ctx context.Context, id, destination string, maxBytes int64) (int64, error) {
	if !validID(id) || destination == "" {
		return 0, ErrInvalidRequest
	}
	if maxBytes <= 0 || maxBytes > maxVideoBytes {
		maxBytes = maxVideoBytes
	}
	taskID, nodeID := logsafe.TaskTrace(ctx)
	started := time.Now()
	c.log().InfoContext(ctx, "节点视频下载开始", "stage", "tk2sd_video_download", "task_id", taskID, "node_id", nodeID, "upstream_task_id", id, "max_bytes", maxBytes, "request_timeout", c.http.Timeout.String())
	fail := func(reason string, status int, received, expected int64, err error) (int64, error) {
		c.log().WarnContext(ctx, "节点视频响应校验失败", "stage", "tk2sd_video_download", "task_id", taskID, "node_id", nodeID, "upstream_task_id", id, "reason", reason, "status_code", status, "bytes_received", received, "expected_bytes", expected, "elapsed", time.Since(started).String(), "error_reason", logsafe.Error(err))
		return received, err
	}
	r, err := c.newRequest(ctx, http.MethodGet, "/v1/tasks/"+id+"/video", nil, "")
	if err != nil {
		return fail("request_failed", 0, 0, -1, err)
	}
	r.Header.Set("Accept", "video/mp4")
	response, err := c.do(r)
	if err != nil {
		return fail("transport_failed", 0, 0, -1, err)
	}
	defer response.Body.Close()
	c.log().InfoContext(ctx, "节点视频响应已到达", "stage", "tk2sd_video_download", "task_id", taskID, "node_id", nodeID, "upstream_task_id", id, "status_code", response.StatusCode, "content_length", response.ContentLength, "elapsed", time.Since(started).String())
	if response.StatusCode != http.StatusOK {
		return fail("http_status", response.StatusCode, 0, response.ContentLength, c.decode(response, http.StatusOK, nil, false))
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "video/mp4" {
		return fail("invalid_content_type", response.StatusCode, 0, response.ContentLength, ErrInvalidResponse)
	}
	if response.Header.Get("Content-Range") != "" {
		return fail("unexpected_content_range", response.StatusCode, 0, response.ContentLength, ErrInvalidResponse)
	}
	if response.ContentLength > maxBytes {
		return fail("video_too_large", response.StatusCode, 0, response.ContentLength, ErrBodyTooLarge)
	}
	// 同目录临时文件仅在完整校验后替换目标；失败不破坏既有文件。
	file, err := os.CreateTemp(filepath.Dir(destination), ".tk2sd-video-*")
	if err != nil {
		return fail("temporary_file_failed", response.StatusCode, 0, response.ContentLength, err)
	}
	defer os.Remove(file.Name())
	n, copyErr := io.Copy(file, io.LimitReader(response.Body, maxBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		if ctx.Err() != nil {
			return fail("download_timeout", response.StatusCode, n, response.ContentLength, ctx.Err())
		}
		return fail("stream_interrupted", response.StatusCode, n, response.ContentLength, ErrIncompleteVideo)
	}
	if closeErr != nil {
		return fail("temporary_file_failed", response.StatusCode, n, response.ContentLength, closeErr)
	}
	if n > maxBytes {
		return fail("video_too_large", response.StatusCode, n, response.ContentLength, ErrBodyTooLarge)
	}
	if n == 0 {
		return fail("empty_video", response.StatusCode, n, response.ContentLength, ErrIncompleteVideo)
	}
	if response.ContentLength >= 0 && n != response.ContentLength {
		return fail("content_length_mismatch", response.StatusCode, n, response.ContentLength, ErrIncompleteVideo)
	}
	if err := ctx.Err(); err != nil {
		return fail("download_timeout", response.StatusCode, n, response.ContentLength, err)
	}
	if err := os.Rename(file.Name(), destination); err != nil {
		return fail("temporary_file_failed", response.StatusCode, n, response.ContentLength, err)
	}
	c.log().InfoContext(ctx, "节点视频下载完成", "stage", "tk2sd_video_download", "task_id", taskID, "node_id", nodeID, "upstream_task_id", id, "bytes_downloaded", n, "elapsed", time.Since(started).String())
	return n, nil
}
