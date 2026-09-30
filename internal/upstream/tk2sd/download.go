package tk2sd

import (
	"context"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
)

const maxVideoBytes int64 = 2 << 30

func (c *Client) Download(ctx context.Context, id, destination string, maxBytes int64) (int64, error) {
	if !validID(id) || destination == "" {
		return 0, ErrInvalidRequest
	}
	if maxBytes <= 0 || maxBytes > maxVideoBytes {
		maxBytes = maxVideoBytes
	}
	r, err := c.newRequest(ctx, http.MethodGet, "/v1/tasks/"+id+"/video", nil, "")
	if err != nil {
		return 0, err
	}
	r.Header.Set("Accept", "video/mp4")
	response, err := c.do(r)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, c.decode(response, http.StatusOK, nil, false)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "video/mp4" || response.Header.Get("Content-Range") != "" {
		return 0, ErrInvalidResponse
	}
	if response.ContentLength > maxBytes {
		return 0, ErrBodyTooLarge
	}
	// 同目录临时文件仅在完整校验后替换目标；失败不破坏既有文件。
	file, err := os.CreateTemp(filepath.Dir(destination), ".tk2sd-video-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(file.Name())
	n, copyErr := io.Copy(file, io.LimitReader(response.Body, maxBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		return n, ErrInvalidResponse
	}
	if closeErr != nil {
		return n, closeErr
	}
	if n > maxBytes {
		return n, ErrBodyTooLarge
	}
	if n == 0 || (response.ContentLength >= 0 && n != response.ContentLength) {
		return n, ErrInvalidResponse
	}
	if err := ctx.Err(); err != nil {
		return n, err
	}
	if err := os.Rename(file.Name(), destination); err != nil {
		return n, err
	}
	return n, nil
}
