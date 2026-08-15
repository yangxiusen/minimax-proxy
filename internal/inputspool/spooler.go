package inputspool

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"minimax-h3-tc/internal/domain"
)

type Spooler struct {
	root string
}

type PreparedRequest struct {
	JSON    []byte
	Files   []domain.InputSpoolFile
	taskDir string
}

func New(root string) *Spooler {
	return &Spooler{root: root}
}

func RootFromDatabasePath(path string) string {
	return filepath.Join(filepath.Dir(path), "temp-inputs")
}

func (s *Spooler) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

func (p PreparedRequest) Cleanup() error {
	if p.taskDir == "" {
		return nil
	}
	return os.RemoveAll(p.taskDir)
}

func (s *Spooler) CleanupOrphans(ctx context.Context, liveTaskIDs map[string]bool, minAge time.Duration) error {
	if s == nil || s.root == "" {
		return nil
	}
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-minAge)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		path := filepath.Join(s.root, name)
		if !safeUnder(s.root, path) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if entry.IsDir() {
			if liveTaskIDs[name] {
				continue
			}
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			continue
		}
		if strings.HasSuffix(name, ".part") {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func (s *Spooler) PrepareRequest(ctx context.Context, taskID string, requestJSON []byte) (PreparedRequest, error) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return PreparedRequest{}, errors.New("输入临时目录未配置")
	}
	if taskID == "" || strings.ContainsAny(taskID, `/\`) || taskID == "." || taskID == ".." {
		return PreparedRequest{}, errors.New("task_id 无效")
	}
	var request map[string]any
	if err := json.Unmarshal(requestJSON, &request); err != nil {
		return PreparedRequest{}, err
	}
	content, _ := request["content"].([]any)
	taskDir := filepath.Join(s.root, taskID)
	prepared := PreparedRequest{taskDir: taskDir}
	for index, rawItem := range content {
		if err := ctx.Err(); err != nil {
			_ = prepared.Cleanup()
			return PreparedRequest{}, err
		}
		item, _ := rawItem.(map[string]any)
		itemType, _ := item["type"].(string)
		if itemType != "image_url" && itemType != "audio_url" {
			continue
		}
		urlKey := itemType
		urlObject, _ := item[urlKey].(map[string]any)
		rawURL, _ := urlObject["url"].(string)
		if !strings.HasPrefix(rawURL, "data:") {
			continue
		}
		file, err := s.writeDataURI(taskID, index, itemType, roleOf(item), rawURL)
		if err != nil {
			_ = prepared.Cleanup()
			return PreparedRequest{}, err
		}
		urlObject["url"] = "proxy-input://" + taskID + "/" + file.ID
		prepared.Files = append(prepared.Files, file)
	}
	rewritten, err := json.Marshal(request)
	if err != nil {
		_ = prepared.Cleanup()
		return PreparedRequest{}, err
	}
	prepared.JSON = rewritten
	return prepared, nil
}

func roleOf(item map[string]any) string {
	role, _ := item["role"].(string)
	if role == "" {
		return "input"
	}
	return role
}

func (s *Spooler) writeDataURI(taskID string, index int, contentType, role, rawURL string) (domain.InputSpoolFile, error) {
	header, encoded, ok := strings.Cut(rawURL, ",")
	if !ok || encoded == "" || !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return domain.InputSpoolFile{}, errors.New("素材 Data URI 无效")
	}
	declared := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64"))
	payload, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return domain.InputSpoolFile{}, err
	}
	if len(payload) == 0 {
		return domain.InputSpoolFile{}, errors.New("输入素材为空")
	}
	detected := detectMediaType(payload)
	mediaType := chooseMediaType(contentType, declared, detected)
	if mediaType == "" {
		return domain.InputSpoolFile{}, errors.New("媒体文件格式与声明类型不匹配")
	}
	extension := mediaExtension(mediaType)
	inputID := stableInputID(taskID, index, rawURL)
	relativePath := filepath.ToSlash(filepath.Join(taskID, inputID+extension))
	finalPath := filepath.Join(s.root, filepath.FromSlash(relativePath))
	if !safeUnder(filepath.Join(s.root, taskID), finalPath) {
		return domain.InputSpoolFile{}, errors.New("输入临时文件路径无效")
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return domain.InputSpoolFile{}, err
	}
	partPath := finalPath + ".part"
	if err := os.WriteFile(partPath, payload, 0o600); err != nil {
		return domain.InputSpoolFile{}, err
	}
	if err := os.Rename(partPath, finalPath); err != nil {
		_ = os.Remove(partPath)
		return domain.InputSpoolFile{}, err
	}
	digest := sha256.Sum256(payload)
	return domain.InputSpoolFile{
		ID: inputID, TaskID: taskID, ContentIndex: index, ContentType: contentType, Role: role,
		SourceKind: "data_uri", DeclaredMIME: declared, DetectedMIME: detected,
		MediaType: mediaType, Extension: extension, RelativePath: relativePath,
		SizeBytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func stableInputID(taskID string, index int, source string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", taskID, index, source)))
	return "input_" + hex.EncodeToString(digest[:16])
}

func detectMediaType(payload []byte) string {
	switch {
	case len(payload) >= 8 && string(payload[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(payload) >= 3 && payload[0] == 0xff && payload[1] == 0xd8 && payload[2] == 0xff:
		return "image/jpeg"
	case len(payload) >= 12 && string(payload[:4]) == "RIFF" && string(payload[8:12]) == "WEBP":
		return "image/webp"
	case len(payload) >= 12 && string(payload[:4]) == "RIFF" && string(payload[8:12]) == "WAVE":
		return "audio/wav"
	case len(payload) >= 3 && string(payload[:3]) == "ID3":
		return "audio/mpeg"
	case len(payload) >= 2 && payload[0] == 0xff && payload[1]&0xe0 == 0xe0:
		return "audio/mpeg"
	default:
		return ""
	}
}

func chooseMediaType(contentType, declared, detected string) string {
	wantPrefix := strings.TrimSuffix(contentType, "_url") + "/"
	if detected != "" {
		if !strings.HasPrefix(detected, wantPrefix) {
			return ""
		}
		return normalizeMediaType(detected)
	}
	if strings.HasPrefix(declared, wantPrefix) {
		return normalizeMediaType(declared)
	}
	return ""
}

func normalizeMediaType(mediaType string) string {
	switch mediaType {
	case "image/jpg":
		return "image/jpeg"
	case "audio/mp3":
		return "audio/mpeg"
	case "audio/x-wav":
		return "audio/wav"
	default:
		return mediaType
	}
}

func mediaExtension(mediaType string) string {
	switch normalizeMediaType(mediaType) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "audio/wav":
		return ".wav"
	case "audio/mpeg":
		return ".mp3"
	default:
		return ".bin"
	}
}

func safeUnder(root, path string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}
