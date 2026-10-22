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
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	Logger  *slog.Logger
}

func (m *InputMaterializer) Prepare(ctx context.Context, task domain.Task, run domain.RemoteRun, client AssetClient) ([]byte, error) {
	var request domain.GenerationRequest
	if len(task.RequestJSON) > 64<<20 || json.Unmarshal([]byte(task.RequestJSON), &request) != nil {
		return nil, invalidInput("request_validate", "invalid_json", "生成请求无法解析或超过 64 MiB")
	}
	if request.Model != task.Model || !domain.ValidModelID(request.Model) {
		return nil, invalidInput("request_validate", "invalid_model", "生成请求模型无效或与任务不一致")
	}
	if request.Duration < 4 || request.Duration > 15 {
		return nil, invalidInput("request_validate", "invalid_duration", "生成时长必须为 4–15 秒")
	}
	if len(request.Content) < 1 || len(request.Content) > 13 {
		return nil, invalidInput("request_validate", "invalid_content_count", "content 数量必须为 1–13 项")
	}
	counts := map[string]int{}
	firstFrames := 0
	for i, item := range request.Content {
		counts[item.Type]++
		if item.Type == "text" {
			if strings.TrimSpace(item.Text) == "" || utf8.RuneCountInString(item.Text) > 14000 || item.Role != "" || item.ImageURL != nil || item.VideoURL != nil || item.AudioURL != nil {
				return nil, withInput(invalidInput("request_validate", "invalid_text", "文本必须非空、最多 14000 字且不能包含媒体字段或角色"), i, item.Type, item.Role, "proxy")
			}
		} else {
			refs := 0
			for _, ref := range []*domain.MediaURL{item.ImageURL, item.VideoURL, item.AudioURL} {
				if ref != nil {
					refs++
				}
			}
			if refs != 1 || item.Media() == nil || item.Text != "" || !validRole(item.Type, item.Role) {
				return nil, withInput(invalidInput("request_validate", "invalid_media_reference", "媒体字段、类型或角色不符合协议"), i, item.Type, item.Role, "proxy")
			}
			if item.Role == "first_frame" {
				firstFrames++
			}
		}
	}
	mediaCount := counts["image_url"] + counts["video_url"] + counts["audio_url"]
	if counts["text"] != 1 || (firstFrames > 0 && (firstFrames != 1 || mediaCount != 1)) || counts["image_url"] > 9 || counts["video_url"] > 3 || counts["audio_url"] > 3 || mediaCount > 12 {
		return nil, invalidInput("request_validate", "invalid_content_combination", "文本数量、首帧组合或参考素材数量不符合协议")
	}
	var files []domain.InputSpoolFile
	if m != nil && m.Store != nil {
		var err error
		files, err = m.Store.ListInputSpoolFiles(ctx, task.TaskID)
		if err != nil {
			return nil, inputFailure("tk2sd_input_persistence_failed", "proxy_store", "spool_lookup", "lookup_failed", "素材暂存记录读取失败", err)
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
			return nil, withInput(invalidInput("request_validate", "materializer_missing", "素材处理器或素材记录存储未配置"), i, item.Type, item.Role, "proxy")
		}
		if !validRole(item.Type, item.Role) {
			return nil, withInput(invalidInput("request_validate", "invalid_role", "素材角色不符合协议"), i, item.Type, item.Role, "proxy")
		}
		record, err := m.Store.GetRemoteAsset(ctx, task.TaskID, run.NodeID, i)
		var asset tk2sd.Asset
		if err == nil {
			if record.TaskID != task.TaskID || record.NodeID != run.NodeID || record.ContentIndex != i || record.ContentType != item.Type || record.Role != item.Role || json.Unmarshal([]byte(record.MetadataJSON), &asset) != nil || record.AssetID != asset.ID || record.SizeBytes != asset.Size {
				return nil, withInput(invalidInput("asset_validate", "cached_asset_mismatch", "已缓存的上游素材记录与任务不一致"), i, item.Type, item.Role, "proxy_store")
			}
			m.logInput(ctx, "asset_reused", "复用上游素材", "stage", "asset_lookup", "source", "proxy_store", "content_index", i, "content_type", item.Type)
		} else if errors.Is(err, domain.ErrTaskNotFound) {
			asset, record, err = m.upload(ctx, task.TaskID, run.NodeID, i, item, files, client)
			if err != nil {
				return nil, err
			}
			if err = m.Store.SaveRemoteAsset(ctx, run, record); err != nil {
				return nil, withInput(inputFailure("tk2sd_input_persistence_failed", "proxy_store", "asset_save", "save_failed", "上游素材记录保存失败", err), i, item.Type, item.Role, "proxy_store")
			}
		} else {
			return nil, withInput(inputFailure("tk2sd_input_persistence_failed", "proxy_store", "asset_lookup", "lookup_failed", "上游素材记录读取失败", err), i, item.Type, item.Role, "proxy_store")
		}
		if err = validateAsset(asset, item.Type); err != nil {
			return nil, withInput(err, i, item.Type, item.Role, "upstream")
		}
		if asset.Kind != "image" {
			totalDuration += asset.Duration
			if totalDuration > 15 {
				return nil, withInput(invalidInput("media_validate", "total_duration_exceeded", "参考视频和音频总时长不能超过 15 秒"), i, item.Type, item.Role, "proxy")
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
		return nil, invalidInput("request_validate", "request_body_invalid", "上游生成请求无法编码或超过 1 MiB")
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
		return invalidInput("asset_validate", "invalid_asset_metadata", "上游素材标识、类型或大小不符合协议")
	}
	if a.Kind != "audio" && (a.Width <= 0 || a.Height <= 0) {
		return invalidInput("asset_validate", "invalid_dimensions", "上游素材宽高必须为正数")
	}
	if a.Kind == "image" && a.Duration != 0 {
		return invalidInput("asset_validate", "invalid_image_duration", "上游图片素材时长必须为 0")
	}
	if a.Kind != "image" && (math.IsNaN(a.Duration) || math.IsInf(a.Duration, 0) || a.Duration < 2 || a.Duration > 15) {
		return invalidInput("asset_validate", "invalid_media_duration", "上游视频或音频素材时长必须为 2–15 秒")
	}
	return nil
}

func (m *InputMaterializer) upload(ctx context.Context, taskID, nodeID string, index int, item domain.GenerationContent, files []domain.InputSpoolFile, client AssetClient) (_ tk2sd.Asset, _ domain.NodeAsset, resultErr error) {
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
	source := "media_url"
	if strings.HasPrefix(raw, "data:") {
		source = "data_uri"
	} else if strings.HasPrefix(raw, "proxy-input://") {
		source = "local_spool"
	}
	defer func() {
		if resultErr != nil {
			resultErr = withInput(resultErr, index, item.Type, item.Role, source)
		}
	}()
	for i := range files {
		f := &files[i]
		if f.ContentIndex != index {
			continue
		}
		if meta != nil || f.TaskID != taskID || f.ContentType != item.Type || f.Role != item.Role {
			return zero, record, invalidInput("source_validate", "spool_record_mismatch", "素材暂存记录重复或与任务不一致")
		}
		if raw != "proxy-input://"+taskID+"/"+f.ID && (f.ObjectURL == "" || raw != f.ObjectURL) {
			return zero, record, invalidInput("source_validate", "source_reference_mismatch", "素材引用与暂存记录不一致")
		}
		meta = f
		if f.ObjectURL != "" {
			source = "object_storage"
		} else {
			source = "local_spool"
		}
	}
	fields := []any{"content_index", index, "content_type", item.Type, "role", item.Role, "source", source}
	m.logInput(ctx, "source_open_started", "开始读取远程任务素材", append(fields, "stage", "source_open")...)
	reader, err := m.open(ctx, raw, item.Type, meta)
	if err != nil {
		return zero, record, err
	}
	defer reader.Close()
	buffer := bufio.NewReader(reader)
	header, err := buffer.Peek(512)
	if err != nil && err != io.EOF {
		failure := invalidInput("source_read", "header_read_failed", "素材文件头读取失败")
		failure.Code, failure.cause = "tk2sd_input_source_read_failed", errors.Join(ErrInvalidInput, err)
		return zero, record, failure
	}
	mediaType, extension := detectMedia(header)
	if !strings.HasPrefix(mediaType, strings.TrimSuffix(item.Type, "_url")+"/") {
		return zero, record, invalidInput("media_detect", "unsupported_media", "素材格式不支持或与声明类型不一致（支持 PNG/JPEG/WebP、MP4、MP3/WAV）")
	}
	if meta != nil && (normalizeMIME(meta.MediaType) != mediaType || (meta.DetectedMIME != "" && normalizeMIME(meta.DetectedMIME) != mediaType)) {
		return zero, record, invalidInput("media_detect", "mime_mismatch", "素材实际格式与暂存记录的 MIME 类型不一致")
	}
	m.logInput(ctx, "source_opened", "素材读取与格式识别完成", append(fields, "stage", "media_detect", "detected_mime", mediaType, "limit_bytes", inputLimit(item.Type))...)
	stream := &inputStream{reader: buffer, closer: reader, hash: sha256.New(), limit: inputLimit(item.Type)}
	m.logInput(ctx, "asset_upload_started", "开始上传上游素材", append(fields, "stage", "asset_upload")...)
	asset, err := client.Upload(ctx, fmt.Sprintf("input-%d%s", index, extension), mediaType, stream)
	if err != nil {
		// 上游提前响应会关闭素材流，已收到的 HTTP 响应优先于关闭流产生的读取错误。
		var upstream *tk2sd.HTTPError
		if errors.As(err, &upstream) && upstream.StatusCode > 0 {
			return zero, record, uploadFailure(err)
		}
		if readFailure := stream.readFailure.Load(); readFailure != nil {
			failure := inputFailure("tk2sd_input_source_read_failed", source, "source_read", "stream_read_failed", "上传过程中素材源读取失败", err)
			if errors.Is(readFailure.err, ErrInvalidInput) || errors.Is(readFailure.err, netguard.ErrResponseTooLarge) {
				failure.Reason, failure.Message = "size_exceeded", fmt.Sprintf("素材读取超过 %d 字节限制", inputLimit(item.Type))
			}
			// 大小超限 sentinel 不加入错误链，避免把原上传客户端的 pending 改为终态。
			if !errors.Is(readFailure.err, ErrInvalidInput) {
				failure.cause = errors.Join(err, readFailure.err)
			}
			return zero, record, failure
		}
		return zero, record, uploadFailure(err)
	}
	var extra [1]byte
	n, readErr := stream.Read(extra[:])
	if n != 0 || readErr != io.EOF || stream.failed || stream.size <= 0 {
		return zero, record, invalidInput("integrity_check", "stream_incomplete", "素材流读取不完整、为空或超过大小限制")
	}
	if asset.Size != stream.size {
		failure := invalidInput("integrity_check", "upload_size_mismatch", "上游返回的素材大小与上传字节数不一致")
		failure.Source = "upstream"
		return zero, record, failure
	}
	digest := hex.EncodeToString(stream.hash.Sum(nil))
	if meta != nil && meta.SizeBytes != stream.size {
		return zero, record, invalidInput("integrity_check", "size_mismatch", "素材实际大小与暂存记录不一致")
	}
	if meta != nil && !strings.EqualFold(meta.SHA256, digest) {
		return zero, record, invalidInput("integrity_check", "checksum_mismatch", "素材 SHA256 与暂存记录不一致")
	}
	if err = validateAsset(asset, item.Type); err != nil {
		return zero, record, withInput(err, index, item.Type, item.Role, "upstream")
	}
	// 不持久化上游返回的文件系统路径或无关地址。
	asset.Filename = fmt.Sprintf("input-%d%s", index, extension)
	data, err := json.Marshal(asset)
	if err != nil {
		return zero, record, invalidInput("asset_validate", "metadata_encode_failed", "上游素材元数据无法编码")
	}
	record = domain.NodeAsset{TaskID: taskID, NodeID: nodeID, ContentIndex: index, AssetID: asset.ID, ContentType: item.Type, Role: item.Role, SourceSHA256: digest, SizeBytes: stream.size, MetadataJSON: string(data)}
	m.logInput(ctx, "asset_uploaded", "上游素材上传与完整性校验完成", append(fields, "stage", "integrity_check", "size_bytes", stream.size, "asset_id", asset.ID)...)
	return asset, record, nil
}

func (m *InputMaterializer) open(ctx context.Context, raw, kind string, meta *domain.InputSpoolFile) (io.ReadCloser, error) {
	if meta != nil {
		if meta.SizeBytes <= 0 || meta.SizeBytes > inputLimit(kind) || len(meta.SHA256) != 64 {
			return nil, invalidInput("source_validate", "invalid_spool_metadata", "素材暂存记录缺少有效大小或 SHA256，或大小超过限制")
		}
		if meta.ObjectURL != "" {
			raw = meta.ObjectURL
		} else {
			if meta.SourceKind != "data_uri" || m.Root == "" || !filepath.IsLocal(filepath.FromSlash(meta.RelativePath)) || strings.Contains(meta.RelativePath, "\\") || filepath.Ext(meta.RelativePath) != meta.Extension {
				return nil, invalidInput("local_file", "invalid_spool_path", "本地暂存目录或文件引用无效")
			}
			root, err := os.OpenRoot(m.Root)
			if err != nil {
				failure := invalidInput("local_file", "root_open_failed", "本地暂存目录无法打开")
				failure.cause = errors.Join(ErrInvalidInput, err)
				return nil, failure
			}
			defer root.Close()
			f, err := root.Open(filepath.FromSlash(meta.RelativePath))
			if err != nil {
				failure := invalidInput("local_file", "open_failed", "本地暂存素材文件无法打开")
				failure.cause = errors.Join(ErrInvalidInput, err)
				return nil, failure
			}
			info, err := f.Stat()
			if err != nil || !info.Mode().IsRegular() || info.Size() != meta.SizeBytes {
				f.Close()
				return nil, invalidInput("local_file", "invalid_file_size", "本地暂存素材不是普通文件、无法读取属性或大小与记录不一致")
			}
			digest := sha256.New()
			size, err := io.Copy(digest, io.LimitReader(f, inputLimit(kind)+1))
			if err != nil || size != meta.SizeBytes {
				f.Close()
				return nil, invalidInput("local_file", "file_read_failed", "本地暂存素材读取失败或读取大小与记录不一致")
			}
			if !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), meta.SHA256) {
				f.Close()
				return nil, invalidInput("integrity_check", "checksum_mismatch", "本地暂存素材 SHA256 与记录不一致")
			}
			if _, err = f.Seek(0, io.SeekStart); err != nil {
				f.Close()
				return nil, invalidInput("local_file", "seek_failed", "本地暂存素材无法重置读取位置")
			}
			return f, nil
		}
	}
	if strings.HasPrefix(raw, "data:") {
		decoded, err := inputspool.DecodeDataURI(kind, raw)
		if err != nil || int64(len(decoded.Payload)) > inputLimit(kind) {
			return nil, invalidInput("source_open", "invalid_data_uri", "内联素材解码失败、格式不支持或大小超过限制")
		}
		return io.NopCloser(bytes.NewReader(decoded.Payload)), nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, invalidInput("source_validate", "invalid_url", "素材引用不是有效的 HTTP/HTTPS URL")
	}
	guard := m.Guard
	if guard == nil {
		guard = netguard.New(netguard.Options{})
	}
	if _, err = guard.Validate(ctx, raw); err != nil {
		return nil, invalidInput("source_validate", "url_guard_rejected", "素材 URL 安全校验或 DNS 解析失败")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, invalidInput("source_validate", "invalid_http_request", "无法构造素材下载请求")
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	resp, err := guard.Client(timeout, inputLimit(kind), 3).Do(req)
	if err != nil {
		return nil, inputFailure("tk2sd_input_source_read_failed", "", "source_download", "request_failed", "素材下载未取得有效响应", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		failure := inputFailure("tk2sd_input_source_http_error", "", "source_download", "http_status", "素材下载失败", ErrInvalidInput)
		failure.HTTPStatus = resp.StatusCode
		return nil, failure
	}
	if resp.ContentLength > inputLimit(kind) {
		resp.Body.Close()
		return nil, invalidInput("source_open", "size_exceeded", fmt.Sprintf("素材 Content-Length 超过 %d 字节限制", inputLimit(kind)))
	}
	return resp.Body, nil
}

type inputStream struct {
	reader      io.Reader
	closer      io.Closer
	hash        hash.Hash
	size, limit int64
	failed, eof bool
	readFailure atomic.Pointer[inputReadFailure]
}

type inputReadFailure struct{ err error }

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
		s.readFailure.CompareAndSwap(nil, &inputReadFailure{err: ErrInvalidInput})
		return 0, ErrInvalidInput
	}
	_, _ = s.hash.Write(p[:n])
	if err != nil && err != io.EOF {
		s.failed = true
		s.readFailure.CompareAndSwap(nil, &inputReadFailure{err: err})
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
