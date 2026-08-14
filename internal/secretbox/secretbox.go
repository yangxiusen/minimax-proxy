package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

type Box struct {
	aead      cipher.AEAD
	masterKey []byte
}

const MasterKeyEnvironment = "MINIMAX_PROXY_MASTER_KEY"

func NewFromEnvironment() (*Box, error) {
	value := strings.TrimSpace(os.Getenv(MasterKeyEnvironment))
	if value == "" {
		return nil, fmt.Errorf("环境变量 %s 未配置", MasterKeyEnvironment)
	}
	key, err := decodeMasterKey(value)
	if err != nil {
		return nil, fmt.Errorf("环境变量 %s 无效: %w", MasterKeyEnvironment, err)
	}
	return New(key)
}

func decodeMasterKey(value string) ([]byte, error) {
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if len(value) == 32 {
		return []byte(value), nil
	}
	return nil, errors.New("必须是 32 字节原文、64 位十六进制或 32 字节 Base64")
}

func New(masterKey []byte) (*Box, error) {
	if len(masterKey) != 32 {
		return nil, errors.New("节点密钥主密钥必须是 32 字节")
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES-GCM: %w", err)
	}
	return &Box{aead: aead, masterKey: append([]byte(nil), masterKey...)}, nil
}

func (b *Box) DeriveCallbackSigningKey(ownerKeyID string) ([]byte, error) {
	if b == nil || len(b.masterKey) != 32 || strings.TrimSpace(ownerKeyID) == "" {
		return nil, errors.New("callback 签名密钥配置无效")
	}
	mac := hmac.New(sha256.New, b.masterKey)
	_, _ = mac.Write([]byte("minimax-h3-proxy/callback-signing/v1\x00" + ownerKeyID))
	return mac.Sum(nil), nil
}

func (b *Box) DeriveArtifactSigningKey() ([]byte, error) {
	if b == nil || len(b.masterKey) != 32 {
		return nil, errors.New("产物签名密钥配置无效")
	}
	mac := hmac.New(sha256.New, b.masterKey)
	_, _ = mac.Write([]byte("minimax-h3-proxy/artifact-signing/v1"))
	return mac.Sum(nil), nil
}

func (b *Box) Seal(plaintext string) (nonce, ciphertext []byte, fingerprint string, err error) {
	if b == nil || b.aead == nil {
		return nil, nil, "", errors.New("节点密钥加密器未配置")
	}
	if plaintext == "" {
		return nil, nil, "", errors.New("节点 API Key 不能为空")
	}
	nonce = make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, "", fmt.Errorf("生成 AES-GCM nonce: %w", err)
	}
	ciphertext = b.aead.Seal(nil, nonce, []byte(plaintext), nil)
	digest := sha256.Sum256([]byte(plaintext))
	return nonce, ciphertext, "sha256:" + hex.EncodeToString(digest[:4]), nil
}

func (b *Box) Open(nonce, ciphertext []byte) (string, error) {
	if b == nil || b.aead == nil {
		return "", errors.New("节点密钥解密器未配置")
	}
	if len(nonce) != b.aead.NonceSize() {
		return "", errors.New("节点 API Key nonce 无效")
	}
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", errors.New("节点 API Key 密文认证失败")
	}
	return string(plaintext), nil
}
