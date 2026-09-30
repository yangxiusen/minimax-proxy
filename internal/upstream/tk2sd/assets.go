package tk2sd

import (
	"bytes"
	"context"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
)

const maxAssetBytes int64 = 100 << 20

type Asset struct {
	ID       string  `json:"id"`
	URL      string  `json:"url"`
	Kind     string  `json:"kind"`
	Filename string  `json:"filename"`
	Duration float64 `json:"duration"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Size     int64   `json:"size"`
}

func validAssetID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func validFilename(name string) bool {
	return validName(name) && name != "." && name != ".." && !strings.ContainsAny(name, "/\\:")
}

// source 的所有权在有效请求开始后转移；若实现 io.Closer，取消时会关闭它以中断读取。
// 阻塞型 source 必须自行响应取消，或保证 Close 能解除 Read 阻塞。
func (c *Client) Upload(ctx context.Context, filename, mediaType string, source io.Reader) (Asset, error) {
	mt, params, err := mime.ParseMediaType(mediaType)
	if err != nil || len(params) != 0 || strings.ContainsFunc(mediaType, unicode.IsControl) || !validFilename(filename) || source == nil {
		return Asset{}, ErrInvalidRequest
	}
	kind, _, _ := strings.Cut(mt, "/")
	if kind != "image" && kind != "video" && kind != "audio" {
		return Asset{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, c.http.Timeout)
	defer cancel()
	var framing bytes.Buffer
	mw := multipart.NewWriter(&framing)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filename}))
	header.Set("Content-Type", mt)
	if _, err := mw.CreatePart(header); err != nil {
		return Asset{}, ErrInvalidRequest
	}
	prefixLength := framing.Len()
	if err := mw.Close(); err != nil {
		return Asset{}, ErrInvalidRequest
	}
	counted := &uploadSource{ctx: ctx, source: source, remaining: maxAssetBytes}
	// 头部、素材、结束边界顺序读取，避免 io.Pipe 生产协程在提前响应或取消时阻塞。
	body := &uploadBody{reader: io.MultiReader(bytes.NewReader(framing.Bytes()[:prefixLength]), counted, bytes.NewReader(framing.Bytes()[prefixLength:])), source: source}
	r, err := c.newRequest(ctx, http.MethodPost, "/v1/assets", body, mw.FormDataContentType())
	if err != nil {
		return Asset{}, err
	}
	stopClose := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stopClose()
	defer body.Close()
	response, err := c.do(r)
	if err != nil {
		return Asset{}, err
	}
	defer response.Body.Close()
	var result Asset
	if err := c.decode(response, http.StatusCreated, &result, false); err != nil {
		return Asset{}, err
	}
	if !counted.complete.Load() {
		return Asset{}, ErrUploadSource
	}
	if !validAssetID(result.ID) || result.URL != "asset://"+result.ID || result.Kind != kind || !validFilename(result.Filename) || result.Size <= 0 || result.Size > maxAssetBytes || result.Size != counted.count.Load() || result.Width < 0 || result.Height < 0 || math.IsNaN(result.Duration) || math.IsInf(result.Duration, 0) {
		return Asset{}, ErrInvalidResponse
	}
	if kind == "image" {
		if result.Duration != 0 || result.Width <= 0 || result.Height <= 0 {
			return Asset{}, ErrInvalidResponse
		}
	} else {
		if result.Duration < 2 || result.Duration > 15 || (kind == "video" && (result.Width <= 0 || result.Height <= 0)) {
			return Asset{}, ErrInvalidResponse
		}
	}
	return result, nil
}

type uploadBody struct {
	reader io.Reader
	source io.Reader
	once   sync.Once
}

func (b *uploadBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
func (b *uploadBody) Close() error {
	b.once.Do(func() {
		if closer, ok := b.source.(io.Closer); ok {
			_ = closer.Close()
		}
	})
	return nil
}

type uploadSource struct {
	ctx        context.Context
	source     io.Reader
	remaining  int64
	count      atomic.Int64
	complete   atomic.Bool
	terminal   error
	emptyReads int
}

func (s *uploadSource) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if s.terminal != nil {
		return 0, s.terminal
	}
	if s.complete.Load() {
		return 0, io.EOF
	}
	if int64(len(p)) > s.remaining+1 {
		p = p[:s.remaining+1]
	}
	n, err := s.source.Read(p)
	if n < 0 || n > len(p) {
		s.terminal = ErrUploadSource
		return 0, s.terminal
	}
	s.remaining -= int64(n)
	s.count.Add(int64(n))
	if s.remaining < 0 {
		s.terminal = ErrBodyTooLarge
		return 0, s.terminal
	}
	if err != nil && err != io.EOF {
		s.terminal = ErrUploadSource
		return n, s.terminal
	}
	if err == io.EOF {
		if s.count.Load() == 0 {
			s.terminal = ErrUploadSource
			return 0, s.terminal
		}
		s.complete.Store(true)
	}
	if n == 0 && err == nil {
		s.emptyReads++
		if s.emptyReads >= 100 {
			s.terminal = ErrUploadSource
			return 0, s.terminal
		}
	} else {
		s.emptyReads = 0
	}
	return n, err
}
