package callback

import (
	"context"
	"errors"
	"time"

	"minimax-h3-tc/internal/store/sqlite"
)

type PersistentSecrets interface {
	Open(nonce, ciphertext []byte) (string, error)
	DeriveCallbackSigningKey(ownerKeyID string) ([]byte, error)
}

type PersistentStore struct {
	Repository *sqlite.Store
	Secrets    PersistentSecrets
}

func (store PersistentStore) ClaimCallback(ctx context.Context, now, leaseUntil time.Time) (Delivery, error) {
	if store.Repository == nil || store.Secrets == nil {
		return Delivery{}, errors.New("callback 持久化依赖未配置")
	}
	raw, err := store.Repository.ClaimCallbackDelivery(ctx, now, leaseUntil)
	if errors.Is(err, sqlite.ErrNoCallbackDelivery) {
		return Delivery{}, ErrNoDelivery
	}
	if err != nil {
		return Delivery{}, err
	}
	callbackURL, err := store.Secrets.Open(raw.CallbackURLNonce, raw.CallbackURLCiphertext)
	if err != nil {
		return Delivery{}, errors.New("callback URL 解密失败")
	}
	signingKey, err := store.Secrets.DeriveCallbackSigningKey(raw.APIKeyID)
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{
		ID: raw.ID, TaskID: raw.TaskID, ExternalStatus: raw.ExternalStatus, StateVersion: raw.StateVersion,
		AttemptCount: raw.AttemptCount, CallbackURL: callbackURL, SigningSecret: signingKey,
		Body: append([]byte(nil), raw.RequestBody...), BodyHash: raw.RequestBodyHash,
	}, nil
}

func (store PersistentStore) MarkCallbackSucceeded(ctx context.Context, id string, httpStatus int, deliveredAt time.Time) error {
	return store.Repository.MarkCallbackSucceeded(ctx, id, httpStatus, deliveredAt)
}

func (store PersistentStore) ScheduleCallbackRetry(ctx context.Context, id string, attempt, httpStatus int, message string, nextAttempt time.Time) error {
	return store.Repository.ScheduleCallbackRetry(ctx, id, attempt, httpStatus, message, nextAttempt)
}

func (store PersistentStore) MarkCallbackFailed(ctx context.Context, id string, attempt, httpStatus int, message string) error {
	return store.Repository.MarkCallbackFailed(ctx, id, attempt, httpStatus, message)
}
