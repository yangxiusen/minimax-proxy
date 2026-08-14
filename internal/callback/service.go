package callback

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"minimax-h3-tc/internal/netguard"
)

var (
	ErrChallengeFailed = errors.New("callback challenge 验证失败")
	ErrSignatureConfig = errors.New("callback 签名配置无效")
	ErrInvalidDelivery = errors.New("callback 投递无效")
)

type Options struct {
	ChallengeTimeout time.Duration
	RequestTimeout   time.Duration
	MaxResponseBytes int64
	MaxRedirects     int
}

type Service struct {
	client           *http.Client
	challengeTimeout time.Duration
	requestTimeout   time.Duration
	maxResponseBytes int64
	now              func() time.Time
	challenge        func() (string, error)
}

type URLCipher interface {
	Seal(string) (nonce, ciphertext []byte, fingerprint string, err error)
}

type PreparedTarget struct {
	Nonce       []byte
	Ciphertext  []byte
	Fingerprint string
}

type Delivery struct {
	ID             string
	TaskID         string
	ExternalStatus string
	StateVersion   int64
	AttemptCount   int
	CallbackURL    string
	SigningSecret  []byte
	Body           []byte
	BodyHash       string
}

type DeliveryResult struct {
	Success         bool
	Retryable       bool
	HTTPStatus      int
	Error           error
	ResponseSnippet string
}

func NewService(guard *netguard.Guard, options Options) *Service {
	if guard == nil {
		guard = netguard.New(netguard.Options{})
	}
	if options.ChallengeTimeout <= 0 {
		options.ChallengeTimeout = 3 * time.Second
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = 5 * time.Second
	}
	if options.MaxResponseBytes <= 0 {
		options.MaxResponseBytes = 64 << 10
	}
	if options.MaxRedirects <= 0 {
		options.MaxRedirects = 3
	}
	return &Service{
		client:           guard.Client(options.RequestTimeout, options.MaxResponseBytes, options.MaxRedirects),
		challengeTimeout: options.ChallengeTimeout,
		requestTimeout:   options.RequestTimeout,
		maxResponseBytes: options.MaxResponseBytes,
		now:              time.Now,
		challenge:        randomChallenge,
	}
}

// Challenge 不访问存储；调用方必须在开启创建任务事务前完成此步骤。
func (s *Service) Challenge(ctx context.Context, callbackURL *string) error {
	if callbackURL == nil || strings.TrimSpace(*callbackURL) == "" {
		return nil
	}
	challenge, err := s.challenge()
	if err != nil {
		return fmt.Errorf("生成 callback challenge: %w", err)
	}
	body, err := json.Marshal(struct {
		Challenge string `json:"challenge"`
	}{Challenge: challenge})
	if err != nil {
		return err
	}
	timeout := s.challengeTimeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimSpace(*callbackURL), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: 请求无效", ErrChallengeFailed)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: 网络请求失败", ErrChallengeFailed)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%w: HTTP %d", ErrChallengeFailed, response.StatusCode)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("%w: 响应体无效", ErrChallengeFailed)
	}
	var echoed struct {
		Challenge string `json:"challenge"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&echoed); err != nil || echoed.Challenge != challenge {
		return ErrChallengeFailed
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrChallengeFailed
	}
	return nil
}

// PrepareTarget 先完成网络 challenge，再产生供创建任务事务保存的 URL 密文。
func (s *Service) PrepareTarget(ctx context.Context, callbackURL *string, cipher URLCipher) (*PreparedTarget, error) {
	if callbackURL == nil || strings.TrimSpace(*callbackURL) == "" {
		return nil, nil
	}
	if err := s.Challenge(ctx, callbackURL); err != nil {
		return nil, err
	}
	if cipher == nil {
		return nil, errors.New("callback URL 加密器未配置")
	}
	nonce, ciphertext, fingerprint, err := cipher.Seal(strings.TrimSpace(*callbackURL))
	if err != nil {
		return nil, fmt.Errorf("加密 callback URL: %w", err)
	}
	return &PreparedTarget{Nonce: nonce, Ciphertext: ciphertext, Fingerprint: fingerprint}, nil
}

func NewDelivery(eventID, taskID, status string, stateVersion int64, content json.RawMessage) (Delivery, error) {
	if strings.TrimSpace(eventID) == "" || strings.TrimSpace(taskID) == "" || stateVersion < 1 || !isExternalStatus(status) {
		return Delivery{}, ErrInvalidDelivery
	}
	type payload struct {
		TaskID  string          `json:"task_id"`
		Status  string          `json:"status"`
		Content json.RawMessage `json:"content,omitempty"`
	}
	body, err := json.Marshal(payload{TaskID: taskID, Status: status, Content: content})
	if err != nil {
		return Delivery{}, fmt.Errorf("编码 callback Body: %w", err)
	}
	digest := sha256.Sum256(body)
	return Delivery{
		ID: eventID, TaskID: taskID, ExternalStatus: status, StateVersion: stateVersion,
		Body: body, BodyHash: hex.EncodeToString(digest[:]),
	}, nil
}

func (s *Service) Deliver(ctx context.Context, delivery Delivery) DeliveryResult {
	if err := validateDelivery(delivery); err != nil {
		return DeliveryResult{Error: err}
	}
	timestamp := strconvFormatUnix(s.now().UTC().Unix())
	signature := sign(delivery.SigningSecret, timestamp, delivery.Body)
	timeout := s.requestTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, delivery.CallbackURL, bytes.NewReader(delivery.Body))
	if err != nil {
		return DeliveryResult{Error: ErrInvalidDelivery}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Minimax-Event-Id", delivery.ID)
	request.Header.Set("X-Minimax-Timestamp", timestamp)
	request.Header.Set("X-Minimax-Signature", signature)
	response, err := s.client.Do(request)
	if err != nil {
		if errors.Is(err, netguard.ErrUnsafeURL) {
			return DeliveryResult{Error: errors.New("callback URL 安全校验失败")}
		}
		return DeliveryResult{Retryable: true, Error: errors.New("callback 网络请求失败")}
	}
	defer response.Body.Close()
	snippet, readErr := readSnippet(response.Body, responseLimit(s.maxResponseBytes))
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		if readErr != nil && !errors.Is(readErr, netguard.ErrResponseTooLarge) {
			return DeliveryResult{Retryable: true, HTTPStatus: response.StatusCode, Error: errors.New("读取 callback 响应失败")}
		}
		return DeliveryResult{Success: true, HTTPStatus: response.StatusCode}
	}
	retryable := response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
	return DeliveryResult{
		Retryable: retryable, HTTPStatus: response.StatusCode,
		Error: fmt.Errorf("callback 返回 HTTP %d", response.StatusCode), ResponseSnippet: snippet,
	}
}

func DeriveSigningKey(masterKey []byte, ownerKeyID string) ([]byte, error) {
	if len(masterKey) < 32 || strings.TrimSpace(ownerKeyID) == "" {
		return nil, ErrSignatureConfig
	}
	mac := hmac.New(sha256.New, masterKey)
	_, _ = mac.Write([]byte("minimax-h3-proxy/callback-signing/v1\x00" + ownerKeyID))
	return mac.Sum(nil), nil
}

func validateDelivery(delivery Delivery) error {
	if delivery.ID == "" || delivery.TaskID == "" || delivery.StateVersion < 1 || !isExternalStatus(delivery.ExternalStatus) || strings.TrimSpace(delivery.CallbackURL) == "" || len(delivery.Body) == 0 {
		return ErrInvalidDelivery
	}
	if len(delivery.SigningSecret) < 32 {
		return ErrSignatureConfig
	}
	digest := sha256.Sum256(delivery.Body)
	if delivery.BodyHash != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("%w: Body 哈希不匹配", ErrInvalidDelivery)
	}
	return nil
}

func sign(secret []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func randomChallenge() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func isExternalStatus(status string) bool {
	switch status {
	case "queued", "running", "succeeded", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func responseLimit(value int64) int64 {
	if value <= 0 {
		return 64 << 10
	}
	return value
}

func readSnippet(reader io.Reader, limit int64) (string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if int64(len(data)) > limit {
		return string(data[:limit]), netguard.ErrResponseTooLarge
	}
	return string(data), err
}

func strconvFormatUnix(value int64) string {
	// 避免时间格式的本地化差异，签名协议固定使用十进制 Unix 秒。
	return fmt.Sprintf("%d", value)
}
