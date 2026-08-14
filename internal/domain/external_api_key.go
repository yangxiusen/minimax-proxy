package domain

import (
	"errors"
	"time"
)

var (
	ErrAPIKeyNotFound        = errors.New("API Key 不存在")
	ErrAPIKeyNameConflict    = errors.New("API Key 名称已存在")
	ErrAPIKeyDigestConflict  = errors.New("API Key 已存在")
	ErrAPIKeyVersionConflict = errors.New("API Key 版本冲突")
	ErrAPIKeyInUse           = errors.New("API Key 正在被引用")
)

type ExternalAPIKey struct {
	ID        string
	Name      string
	Key       string
	KeyDigest [32]byte
	KeyPrefix string
	KeySuffix string
	Enabled   bool
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (key ExternalAPIKey) MaskedKey() string { return key.KeyPrefix + "..." + key.KeySuffix }

type ExternalAPIKeyInput struct {
	ID        string
	Name      string
	Key       string
	KeyDigest [32]byte
	KeyPrefix string
	KeySuffix string
	Enabled   bool
}

type ExternalAPIKeyUpdate struct {
	Name    string
	Enabled bool
}

type ExternalAPIKeyCredential struct {
	ID        string
	KeyDigest [32]byte
	Enabled   bool
}
