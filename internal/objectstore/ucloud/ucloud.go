package ucloud

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	ufsdk "github.com/ufilesdk-dev/ufile-gosdk"

	"minimax-h3-tc/internal/objectstore"
)

type Config struct {
	BucketName, FileHost, PublicBaseURL string
	PublicKey, PrivateKey               string
	Client                              *http.Client
}

type Store struct{ config Config }

func New(config Config) (*Store, error) {
	endpoint, err := url.Parse(config.FileHost)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
		return nil, &objectstore.Error{Code: "ucloud_config_invalid", Message: "UCloud File Host 配置无效"}
	}
	publicBase, err := url.Parse(config.PublicBaseURL)
	if err != nil || publicBase.Scheme != "https" || publicBase.Host == "" {
		return nil, &objectstore.Error{Code: "ucloud_config_invalid", Message: "UCloud 公开访问地址配置无效"}
	}
	if config.BucketName == "" || config.PublicKey == "" || config.PrivateKey == "" {
		return nil, &objectstore.Error{Code: "ucloud_config_invalid", Message: "UCloud 配置不完整"}
	}
	if config.Client == nil {
		config.Client = http.DefaultClient
	}
	return &Store{config: config}, nil
}

func (s *Store) UploadFile(ctx context.Context, filePath, objectKey, mimeType string) (string, error) {
	request, err := ufsdk.NewFileRequest(&ufsdk.Config{
		PublicKey: s.config.PublicKey, PrivateKey: s.config.PrivateKey, BucketName: s.config.BucketName,
		Endpoint: s.config.FileHost, VerifyUploadMD5: true,
	}, s.config.Client)
	if err != nil {
		return "", &objectstore.Error{Code: "ucloud_config_invalid", Message: "UCloud 上传客户端创建失败"}
	}
	request.Context = ctx
	if err := request.AsyncUpload(filePath, objectKey, mimeType, 4); err != nil {
		return "", mapError(err, request.LastResponseStatus)
	}
	verify, err := ufsdk.NewFileRequest(&ufsdk.Config{
		PublicKey: s.config.PublicKey, PrivateKey: s.config.PrivateKey, BucketName: s.config.BucketName,
		Endpoint: s.config.FileHost,
	}, s.config.Client)
	if err != nil {
		return "", &objectstore.Error{Code: "ucloud_verify_failed", Message: "UCloud 对象校验初始化失败", Retryable: true}
	}
	verify.Context = ctx
	if err := verify.HeadFile(objectKey); err != nil {
		s.delete(ctx, objectKey)
		return "", mapError(err, verify.LastResponseStatus)
	}
	publicURL := s.publicURL(objectKey)
	publicRequest, err := http.NewRequestWithContext(ctx, http.MethodHead, publicURL, nil)
	if err != nil {
		return "", &objectstore.Error{Code: "ucloud_public_read_failed", Message: "UCloud 公开访问地址无效"}
	}
	publicResponse, err := s.config.Client.Do(publicRequest)
	if err != nil {
		s.delete(ctx, objectKey)
		return "", &objectstore.Error{Code: "ucloud_public_read_failed", Message: "UCloud 对象无法公开读取", Retryable: true}
	}
	publicResponse.Body.Close()
	if publicResponse.StatusCode < 200 || publicResponse.StatusCode >= 300 {
		s.delete(ctx, objectKey)
		return "", &objectstore.Error{Code: "ucloud_public_read_failed", Message: "UCloud 对象无法公开读取", Retryable: publicResponse.StatusCode == http.StatusTooManyRequests || publicResponse.StatusCode >= 500}
	}
	return publicURL, nil
}

func (s *Store) publicURL(objectKey string) string {
	base, _ := url.Parse(strings.TrimRight(s.config.PublicBaseURL, "/") + "/")
	return base.ResolveReference(&url.URL{Path: objectKey}).String()
}

func (s *Store) Probe(ctx context.Context) error {
	file, err := os.CreateTemp("", "ucloud-probe-*.txt")
	if err != nil {
		return err
	}
	filePath := file.Name()
	defer os.Remove(filePath)
	if _, err := file.WriteString("ok"); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	key := "MiniMax-H3/.health/probe-" + time.Now().UTC().Format("20060102T150405.000000000") + ".txt"
	publicURL, err := s.UploadFile(ctx, filePath, key, "text/plain")
	if err != nil {
		return err
	}
	defer s.delete(ctx, key)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, publicURL, nil)
	if err != nil {
		return err
	}
	response, err := s.config.Client.Do(request)
	if err != nil {
		return &objectstore.Error{Code: "ucloud_public_read_failed", Message: "UCloud 对象无法公开读取", Retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &objectstore.Error{Code: "ucloud_public_read_failed", Message: "UCloud 对象无法公开读取", Retryable: response.StatusCode >= 500}
	}
	return nil
}

func (s *Store) delete(ctx context.Context, objectKey string) {
	request, err := ufsdk.NewFileRequest(&ufsdk.Config{PublicKey: s.config.PublicKey, PrivateKey: s.config.PrivateKey, BucketName: s.config.BucketName, Endpoint: s.config.FileHost}, s.config.Client)
	if err == nil {
		request.Context = ctx
		_ = request.DeleteFile(objectKey)
	}
}

func mapError(err error, status int) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return &objectstore.Error{Code: "ucloud_auth_failed", Message: "UCloud 返回鉴权失败，请检查对象存储配置"}
	}
	retryable := status == 0 || status == http.StatusTooManyRequests || status >= 500
	return &objectstore.Error{Code: "ucloud_upload_failed", Message: "UCloud 上传失败", Retryable: retryable}
}
