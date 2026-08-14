package callback

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

var ErrNoDelivery = errors.New("没有待投递 callback")

type Store interface {
	ClaimCallback(context.Context, time.Time, time.Time) (Delivery, error)
	MarkCallbackSucceeded(context.Context, string, int, time.Time) error
	ScheduleCallbackRetry(context.Context, string, int, int, string, time.Time) error
	MarkCallbackFailed(context.Context, string, int, int, string) error
}

type Worker struct {
	Store          Store
	Service        *Service
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	LeaseDuration  time.Duration
	Now            func() time.Time
	IsEmpty        func(error) bool
	Logger         *slog.Logger
}

func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	if w.Store == nil || w.Service == nil {
		return false, errors.New("callback worker 依赖未配置")
	}
	now := w.now()
	delivery, err := w.Store.ClaimCallback(ctx, now, now.Add(w.leaseDuration()))
	if err != nil {
		if w.empty(err) {
			return false, nil
		}
		return false, err
	}
	attempt := delivery.AttemptCount + 1
	result := w.Service.Deliver(ctx, delivery)
	logger := w.logger().With("event_id", delivery.ID, "task_id", delivery.TaskID, "attempt", attempt)
	if result.Success {
		if err := w.Store.MarkCallbackSucceeded(ctx, delivery.ID, result.HTTPStatus, now); err != nil {
			return true, err
		}
		logger.Info("callback 投递成功", "http_status", result.HTTPStatus)
		return true, nil
	}
	message := deliveryError(result.Error)
	if result.Retryable && attempt < w.maxAttempts() {
		nextAttempt := now.Add(w.backoff(attempt))
		if err := w.Store.ScheduleCallbackRetry(ctx, delivery.ID, attempt, result.HTTPStatus, message, nextAttempt); err != nil {
			return true, err
		}
		logger.Warn("callback 投递失败，已安排重试", "http_status", result.HTTPStatus, "next_attempt_at", nextAttempt)
		return true, nil
	}
	if err := w.Store.MarkCallbackFailed(ctx, delivery.ID, attempt, result.HTTPStatus, message); err != nil {
		return true, err
	}
	logger.Error("callback 投递最终失败", "http_status", result.HTTPStatus)
	return true, nil
}

func (w *Worker) Run(ctx context.Context, idleInterval time.Duration) error {
	if idleInterval <= 0 {
		idleInterval = time.Second
	}
	ticker := time.NewTicker(idleInterval)
	defer ticker.Stop()
	for {
		worked, err := w.ProcessOne(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			w.logger().Error("callback worker 执行失败", "error", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Worker) backoff(attempt int) time.Duration {
	initial := w.InitialBackoff
	if initial <= 0 {
		initial = time.Second
	}
	maximum := w.MaxBackoff
	if maximum <= 0 {
		maximum = 5 * time.Minute
	}
	power := attempt - 1
	if power > 30 {
		power = 30
	}
	factor := time.Duration(1) << power
	if initial >= maximum || initial > maximum/factor {
		return maximum
	}
	return initial * factor
}

func (w *Worker) maxAttempts() int {
	if w.MaxAttempts <= 0 {
		return 5
	}
	return w.MaxAttempts
}

func (w *Worker) leaseDuration() time.Duration {
	if w.LeaseDuration <= 0 {
		return 30 * time.Second
	}
	return w.LeaseDuration
}

func (w *Worker) now() time.Time {
	if w.Now == nil {
		return time.Now().UTC()
	}
	return w.Now().UTC()
}

func (w *Worker) empty(err error) bool {
	if w.IsEmpty != nil {
		return w.IsEmpty(err)
	}
	return errors.Is(err, ErrNoDelivery)
}

func (w *Worker) logger() *slog.Logger {
	if w.Logger == nil {
		return slog.Default()
	}
	return w.Logger
}

func deliveryError(err error) string {
	if err == nil {
		return "callback 投递失败"
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}
