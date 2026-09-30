package remote

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/inputspool"
	"minimax-h3-tc/internal/netguard"
	"minimax-h3-tc/internal/upstream/tk2sd"
)

var ErrInvalidInput = errors.New("远程素材校验失败")

type AssetStore interface {
	ListInputSpoolFiles(context.Context, string) ([]domain.InputSpoolFile, error)
	GetRemoteAsset(context.Context, string, string, int) (domain.NodeAsset, error)
	SaveRemoteAsset(context.Context, domain.RemoteRun, domain.NodeAsset) error
}

type AssetClient interface {
	Upload(context.Context, string, string, io.Reader) (tk2sd.Asset, error)
}

type InputMaterializer struct {
	Store   AssetStore
	Root    string
	Guard   *netguard.Guard
	Timeout time.Duration
}

func (m *InputMaterializer) Prepare(ctx context.Context, task domain.Task, run domain.RemoteRun, client AssetClient) ([]byte, error) {
	var request domain.GenerationRequest
	if len(task.RequestJSON) > 64<<20 || json.Unmarshal([]byte(task.RequestJSON), &request) != nil || request.Model != task.Model || !domain.ValidModelID(request.Model) || request.Duration < 4 || request.Duration > 15 || len(request.Content) < 1 || len(request.Content) > 13 {
		return nil, ErrInvalidInput
	}
	counts := map[string]int{}
	firstFrames := 0
	for _, item := range request.Content {
		counts[item.Type]++
		if item.Type == "text" {
			if strings.TrimSpace(item.Text) == "" || utf8.RuneCountInString(item.Text) > 14000 || item.Role != "" || item.ImageURL != nil || item.VideoURL != nil || item.AudioURL != nil {
				return nil, ErrInvalidInput
			}
		} else {
			refs := 0
			for _, ref := range []*domain.MediaURL{item.ImageURL, item.VideoURL, item.AudioURL} {
				if ref != nil {
					refs++
				}
			}
			if refs != 1 || item.Media() == nil || item.Text != "" || !validRole(item.Type, item.Role) {
				return nil, ErrInvalidInput
			}
			if item.Role == "first_frame" {
				firstFrames++
			}
		}
	}
	mediaCount := counts["image_url"] + counts["video_url"] + counts["audio_url"]
	if counts["text"] != 1 || (firstFrames > 0 && (firstFrames != 1 || mediaCount != 1)) || counts["image_url"] > 9 || counts["video_url"] > 3 || counts["audio_url"] > 3 || mediaCount > 12 {
		return nil, ErrInvalidInput
	}
	var files []domain.InputSpoolFile
	if m != nil && m.Store != nil {
		var err error
		files, err = m.Store.ListInputSpoolFiles(ctx, task.TaskID)
		if err != nil {
			return nil, err
		}
	}
	var totalDuration float64
	for i, item := range request.Content {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if item.Type == "text" {
			continue
		}
		if m == nil || m.Store == nil || item.Media() == nil {
			return nil, ErrInvalidInput
		}
		if !validRole(item.Type, item.Role) {
			return nil, ErrInvalidInput
		}
		record, err := m.Store.GetRemoteAsset(ctx, task.TaskID, run.NodeID, i)
		var asset tk2sd.Asset
		if err == nil {
			if record.TaskID != task.TaskID || record.NodeID != run.NodeID || record.ContentIndex != i || record.ContentType != item.Type || record.Role != item.Role || json.Unmarshal([]byte(record.MetadataJSON), &asset) != nil || record.AssetID != asset.ID || record.SizeBytes != asset.Size {
				return nil, ErrInvalidInput
			}
		} else if errors.Is(err, domain.ErrTaskNotFound) {
			asset, record, err = m.upload(ctx, task.TaskID, run.NodeID, i, item, files, client)
			if err != nil {
				return nil, err
			}
			if err = m.Store.SaveRemoteAsset(ctx, run, record); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
		if err = validateAsset(asset, item.Type); err != nil {
			return nil, err
		}
		if asset.Kind != "image" {
			totalDuration += asset.Duration
			if totalDuration > 15 {
				return nil, ErrInvalidInput
			}
		}
		request.Content[i].Media().URL = "asset://" + asset.ID
	}
	// 只发送协议允许的字段，回调、默认分辨率和水印均由 Proxy 保留。
	body, err := json.Marshal(struct {
		Model    string                     `json:"model"`
		Content  []domain.GenerationContent `json:"content"`
		Duration int                        `json:"duration"`
	}{request.Model, request.Content, request.Duration})
	if err != nil || len(body) > 1<<20 {
		return nil, ErrInvalidInput
	}
	return body, nil
}

func validRole(kind, role string) bool {
	switch kind {
	case "image_url":
		return role == "first_frame" || role == "reference_image"
	case "video_url":
		return role == "reference_video"
	case "audio_url":
		return role == "reference_audio"
	}
	return false
}

func validateAsset(a tk2sd.Asset, kind string) error {
	id, err := hex.DecodeString(a.ID)
	if err != nil || len(id) != 16 || strings.ToLower(a.ID) != a.ID || a.URL != "asset://"+a.ID || a.Kind != strings.TrimSuffix(kind, "_url") || a.Size <= 0 || a.Size > inputLimit(kind) {
		return ErrInvalidInput
	}
	if a.Kind != "audio" && (a.Width <= 0 || a.Height <= 0) {
		return ErrInvalidInput
	}
	if a.Kind == "image" && a.Duration != 0 {
		return ErrInvalidInput
	}
	if a.Kind != "image" && (math.IsNaN(a.Duration) || math.IsInf(a.Duration, 0) || a.Duration < 2 || a.Duration > 15) {
		return ErrInvalidInput
	}
	return nil
}

func (m *InputMaterializer) upload(ctx context.Context, taskID, nodeID string, index int, item domain.GenerationContent, files []domain.InputSpoolFile, client AssetClient) (tk2sd.Asset, domain.NodeAsset, error) {
	var zero tk2sd.Asset
	var record domain.NodeAsset
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var meta *domain.InputSpoolFile
	raw := item.Media().URL
	for i := range files {
		f := &files[i]
		if f.ContentIndex != index {
			continue
		}
		if meta != nil || f.TaskID != taskID || f.ContentType != item.Type || f.Role != item.Role {
			return zero, record, ErrInvalidInput
		}
		if raw != "proxy-input://"+taskID+"/"+f.ID && (f.ObjectURL == "" || raw != f.ObjectURL) {
			return zero, record, ErrInvalidInput
		}
		meta = f
	}
	reader, err := m.open(ctx, raw, item.Type, meta)
	if err != nil {
		return zero, record, err
	}
	defer reader.Close()
	buffer := bufio.NewReader(reader)
	header, err := buffer.Peek(512)
	if err != nil && err != io.EOF {
		return zero, record, ErrInvalidInput
	}
	mediaType, extension := detectMedia(header)
	if !strings.HasPrefix(mediaType, strings.TrimSuffix(item.Type, "_url")+"/") {
		return zero, record, ErrInvalidInput
	}
	if meta != nil && (normalizeMIME(meta.MediaType) != mediaType || (meta.DetectedMIME != "" && normalizeMIME(meta.DetectedMIME) != mediaType)) {
		return zero, record, ErrInvalidInput
	}
	stream := &inputStream{reader: buffer, closer: reader, hash: sha256.New(), limit: inputLimit(item.Type)}
	asset, err := client.Upload(ctx, fmt.Sprintf("input-%d%s", index, extension), mediaType, stream)
	if err != nil {
		return zero, record, err
	}
	var extra [1]byte
	n, readErr := stream.Read(extra[:])
	if n != 0 || readErr != io.EOF || stream.failed || stream.size <= 0 || asset.Size != stream.size {
		return zero, record, ErrInvalidInput
	}
	digest := hex.EncodeToString(stream.hash.Sum(nil))
	if meta != nil && (meta.SizeBytes != stream.size || !strings.EqualFold(meta.SHA256, digest)) {
		return zero, record, ErrInvalidInput
	}
	if err = validateAsset(asset, item.Type); err != nil {
		return zero, record, err
	}
	// 不持久化上游返回的文件系统路径或无关地址。
	asset.Filename = fmt.Sprintf("input-%d%s", index, extension)
	data, err := json.Marshal(asset)
	if err != nil {
		return zero, record, ErrInvalidInput
	}
	record = domain.NodeAsset{TaskID: taskID, NodeID: nodeID, ContentIndex: index, AssetID: asset.ID, ContentType: item.Type, Role: item.Role, SourceSHA256: digest, SizeBytes: stream.size, MetadataJSON: string(data)}
	return asset, record, nil
}

func (m *InputMaterializer) open(ctx context.Context, raw, kind string, meta *domain.InputSpoolFile) (io.ReadCloser, error) {
	if meta != nil {
		if meta.SizeBytes <= 0 || meta.SizeBytes > inputLimit(kind) || len(meta.SHA256) != 64 {
			return nil, ErrInvalidInput
		}
		if meta.ObjectURL != "" {
			raw = meta.ObjectURL
		} else {
			if meta.SourceKind != "data_uri" || m.Root == "" || !filepath.IsLocal(filepath.FromSlash(meta.RelativePath)) || strings.Contains(meta.RelativePath, "\\") || filepath.Ext(meta.RelativePath) != meta.Extension {
				return nil, ErrInvalidInput
			}
			root, err := os.OpenRoot(m.Root)
			if err != nil {
				return nil, ErrInvalidInput
			}
			defer root.Close()
			f, err := root.Open(filepath.FromSlash(meta.RelativePath))
			if err != nil {
				return nil, ErrInvalidInput
			}
			info, err := f.Stat()
			if err != nil || !info.Mode().IsRegular() || info.Size() != meta.SizeBytes {
				f.Close()
				return nil, ErrInvalidInput
			}
			digest := sha256.New()
			size, err := io.Copy(digest, io.LimitReader(f, inputLimit(kind)+1))
			if err != nil || size != meta.SizeBytes || !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), meta.SHA256) {
				f.Close()
				return nil, ErrInvalidInput
			}
			if _, err = f.Seek(0, io.SeekStart); err != nil {
				f.Close()
				return nil, ErrInvalidInput
			}
			return f, nil
		}
	}
	if strings.HasPrefix(raw, "data:") {
		decoded, err := inputspool.DecodeDataURI(kind, raw)
		if err != nil || int64(len(decoded.Payload)) > inputLimit(kind) {
			return nil, ErrInvalidInput
		}
		return io.NopCloser(bytes.NewReader(decoded.Payload)), nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, ErrInvalidInput
	}
	guard := m.Guard
	if guard == nil {
		guard = netguard.New(netguard.Options{})
	}
	if _, err = guard.Validate(ctx, raw); err != nil {
		return nil, ErrInvalidInput
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, ErrInvalidInput
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	resp, err := guard.Client(timeout, inputLimit(kind), 3).Do(req)
	if err != nil {
		return nil, errors.New("远程素材读取失败")
	}
	if resp.StatusCode != http.StatusOK || resp.ContentLength > inputLimit(kind) {
		resp.Body.Close()
		return nil, ErrInvalidInput
	}
	return resp.Body, nil
}

type inputStream struct {
	reader      io.Reader
	closer      io.Closer
	hash        hash.Hash
	size, limit int64
	failed, eof bool
}

func (s *inputStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if s.failed {
		return 0, ErrInvalidInput
	}
	if s.eof {
		return 0, io.EOF
	}
	if int64(len(p)) > s.limit-s.size+1 {
		p = p[:s.limit-s.size+1]
	}
	n, err := s.reader.Read(p)
	s.size += int64(n)
	if s.size > s.limit {
		s.failed = true
		return 0, ErrInvalidInput
	}
	_, _ = s.hash.Write(p[:n])
	if err != nil && err != io.EOF {
		s.failed = true
	}
	if err == io.EOF {
		s.eof = true
	}
	return n, err
}

func (s *inputStream) Close() error {
	if s.closer != nil {
		return s.closer.Close()
	}
	return nil
}

func inputLimit(kind string) int64 {
	switch kind {
	case "image_url":
		return 30 << 20
	case "video_url":
		return 50 << 20
	case "audio_url":
		return 15 << 20
	}
	return 0
}
func normalizeMIME(s string) string {
	switch s {
	case "image/jpg":
		return "image/jpeg"
	case "audio/mp3":
		return "audio/mpeg"
	case "audio/x-wav":
		return "audio/wav"
	}
	return s
}
func detectMedia(b []byte) (string, string) {
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png", ".png"
	case len(b) >= 3 && b[0] == 255 && b[1] == 216 && b[2] == 255:
		return "image/jpeg", ".jpg"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp", ".webp"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		return "audio/wav", ".wav"
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		return "video/mp4", ".mp4"
	case len(b) >= 3 && string(b[:3]) == "ID3":
		return "audio/mpeg", ".mp3"
	case len(b) >= 2 && b[0] == 255 && b[1]&224 == 224:
		return "audio/mpeg", ".mp3"
	}
	return "", ""
}
