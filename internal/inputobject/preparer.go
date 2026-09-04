package inputobject

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/inputspool"
	"minimax-h3-tc/internal/objectstore"
)

var (
	ErrNotReady     = errors.New("对象存储未就绪")
	ErrUploadFailed = errors.New("输入素材上传对象存储失败")
)

type ConfigStore interface {
	GetObjectStorageConfig(context.Context) (domain.ObjectStorageConfig, error)
}
type SecretOpener interface {
	Open([]byte, []byte) (string, error)
}
type StoreFactory func(domain.ObjectStorageConfig, string, string) (objectstore.DataStore, error)
type PreparedRequest struct {
	JSON    []byte
	Enabled bool
	Files   []domain.InputSpoolFile
}
type Preparer struct {
	configs ConfigStore
	secrets SecretOpener
	factory StoreFactory
}

func New(configs ConfigStore, secrets SecretOpener, factory StoreFactory) *Preparer {
	return &Preparer{configs: configs, secrets: secrets, factory: factory}
}

func (p *Preparer) Prepare(ctx context.Context, requestNamespace string, requestJSON []byte) (PreparedRequest, error) {
	config, err := p.configs.GetObjectStorageConfig(ctx)
	if errors.Is(err, domain.ErrObjectStorageNotFound) || (err == nil && !config.UploadBase64Inputs) {
		return PreparedRequest{JSON: append([]byte(nil), requestJSON...)}, nil
	}
	var request map[string]any
	if err := json.Unmarshal(requestJSON, &request); err != nil {
		return PreparedRequest{}, err
	}
	content, _ := request["content"].([]any)
	hasDataURI := false
	for _, rawItem := range content {
		item, _ := rawItem.(map[string]any)
		contentType, _ := item["type"].(string)
		urlObject, _ := item[contentType].(map[string]any)
		rawURL, _ := urlObject["url"].(string)
		if (contentType == "image_url" || contentType == "video_url" || contentType == "audio_url") && strings.HasPrefix(strings.ToLower(rawURL), "data:") {
			hasDataURI = true
			break
		}
	}
	if !hasDataURI {
		return PreparedRequest{JSON: append([]byte(nil), requestJSON...), Enabled: true}, nil
	}
	if err != nil || config.LastTestStatus != "passed" || p.secrets == nil || p.factory == nil {
		return PreparedRequest{}, ErrNotReady
	}
	publicKey, err := p.secrets.Open(config.PublicKeyNonce, config.PublicKeyCiphertext)
	if err != nil {
		return PreparedRequest{}, ErrNotReady
	}
	privateKey, err := p.secrets.Open(config.PrivateKeyNonce, config.PrivateKeyCiphertext)
	if err != nil {
		return PreparedRequest{}, ErrNotReady
	}
	store, err := p.factory(config, publicKey, privateKey)
	if err != nil {
		return PreparedRequest{}, ErrNotReady
	}
	result := PreparedRequest{Enabled: true}
	for index, rawItem := range content {
		if err := ctx.Err(); err != nil {
			return PreparedRequest{}, err
		}
		item, _ := rawItem.(map[string]any)
		contentType, _ := item["type"].(string)
		if contentType != "image_url" && contentType != "video_url" && contentType != "audio_url" {
			continue
		}
		urlObject, _ := item[contentType].(map[string]any)
		rawURL, _ := urlObject["url"].(string)
		if !strings.HasPrefix(strings.ToLower(rawURL), "data:") {
			continue
		}
		decoded, err := inputspool.DecodeDataURI(contentType, rawURL)
		if err != nil {
			return PreparedRequest{}, err
		}
		if decoded.DetectedMIME == "" || decoded.DeclaredMIME != decoded.DetectedMIME {
			return PreparedRequest{}, errors.New("媒体文件格式与声明类型不匹配")
		}
		key := fmt.Sprintf("MiniMax-H3/inputs/%s/%d-%s%s", requestNamespace, index, decoded.SHA256[:16], decoded.Extension)
		publicURL, err := store.Upload(ctx, decoded.Payload, key, decoded.MediaType)
		if err != nil {
			return PreparedRequest{}, fmt.Errorf("%w: %v", ErrUploadFailed, err)
		}
		urlObject["url"] = publicURL
		role, _ := item["role"].(string)
		if role == "" {
			role = "input"
		}
		result.Files = append(result.Files, domain.InputSpoolFile{
			ContentIndex: index, ContentType: contentType, Role: role,
			SourceKind: "data_uri", DeclaredMIME: decoded.DeclaredMIME, DetectedMIME: decoded.DetectedMIME,
			MediaType: decoded.MediaType, Extension: decoded.Extension, RelativePath: key, ObjectURL: publicURL,
			SizeBytes: int64(len(decoded.Payload)), SHA256: decoded.SHA256,
		})
	}
	rewritten, err := json.Marshal(request)
	if err != nil {
		return PreparedRequest{}, err
	}
	result.JSON = rewritten
	return result, nil
}
