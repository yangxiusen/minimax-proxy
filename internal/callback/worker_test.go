package callback

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

var errNoDelivery = errors.New("no delivery")

type storeStub struct {
	delivery       Delivery
	claimErr       error
	succeeded      int
	retried        int
	failed         int
	nextAttempt    time.Time
	lastAttempt    int
	lastHTTPStatus int
	lastError      string
}

func (s *storeStub) ClaimCallback(context.Context, time.Time, time.Time) (Delivery, error) {
	return s.delivery, s.claimErr
}

func (s *storeStub) MarkCallbackSucceeded(_ context.Context, _ string, status int, _ time.Time) error {
	s.succeeded++
	s.lastHTTPStatus = status
	return nil
}

func (s *storeStub) ScheduleCallbackRetry(_ context.Context, _ string, attempt int, status int, message string, next time.Time) error {
	s.retried++
	s.lastAttempt, s.lastHTTPStatus, s.lastError, s.nextAttempt = attempt, status, message, next
	return nil
}

func (s *storeStub) MarkCallbackFailed(_ context.Context, _ string, attempt int, status int, message string) error {
	s.failed++
	s.lastAttempt, s.lastHTTPStatus, s.lastError = attempt, status, message
	return nil
}

func TestWorkerSchedulesRetryWithExponentialBackoff(t *testing.T) {
	service := newTestService(func(*http.Request) (*http.Response, error) {
		return response(http.StatusTooManyRequests, "later"), nil
	})
	delivery, _ := NewDelivery("event-1", "task-1", "running", 2, nil)
	delivery.CallbackURL = "https://callback.example/hook"
	delivery.SigningSecret = []byte(strings.Repeat("s", 32))
	delivery.AttemptCount = 2
	store := &storeStub{delivery: delivery}
	now := time.Unix(1_800_000_000, 0).UTC()
	worker := Worker{Store: store, Service: service, MaxAttempts: 5, InitialBackoff: time.Second, MaxBackoff: time.Minute, LeaseDuration: time.Minute, Now: func() time.Time { return now }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if worked, err := worker.ProcessOne(context.Background()); err != nil || !worked {
		t.Fatalf("ProcessOne() = %v, %v", worked, err)
	}
	if store.retried != 1 || store.lastAttempt != 3 || !store.nextAttempt.Equal(now.Add(4*time.Second)) {
		t.Fatalf("retry state = %#v", store)
	}
}

func TestWorkerMarksPermanent4xxWithoutChangingTask(t *testing.T) {
	service := newTestService(func(*http.Request) (*http.Response, error) {
		return response(http.StatusBadRequest, "bad request"), nil
	})
	delivery, _ := NewDelivery("event-2", "task-2", "succeeded", 4, nil)
	delivery.CallbackURL = "https://callback.example/hook"
	delivery.SigningSecret = []byte(strings.Repeat("s", 32))
	store := &storeStub{delivery: delivery}
	worker := Worker{Store: store, Service: service, MaxAttempts: 5, Now: time.Now, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if worked, err := worker.ProcessOne(context.Background()); err != nil || !worked {
		t.Fatalf("ProcessOne() = %v, %v", worked, err)
	}
	if store.failed != 1 || store.retried != 0 || store.succeeded != 0 {
		t.Fatalf("store state = %#v", store)
	}
}

func TestWorkerStopsAfterConfiguredAttempts(t *testing.T) {
	service := newTestService(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	})
	delivery, _ := NewDelivery("event-3", "task-3", "failed", 5, nil)
	delivery.CallbackURL = "https://callback.example/hook"
	delivery.SigningSecret = []byte(strings.Repeat("s", 32))
	delivery.AttemptCount = 2
	store := &storeStub{delivery: delivery}
	worker := Worker{Store: store, Service: service, MaxAttempts: 3, Now: time.Now, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if worked, err := worker.ProcessOne(context.Background()); err != nil || !worked {
		t.Fatalf("ProcessOne() = %v, %v", worked, err)
	}
	if store.failed != 1 || store.lastAttempt != 3 {
		t.Fatalf("store state = %#v", store)
	}
}

func TestWorkerTreatsEmptyQueueAsNoWork(t *testing.T) {
	worker := Worker{Store: &storeStub{claimErr: errNoDelivery}, Service: newTestService(nil), IsEmpty: func(err error) bool { return errors.Is(err, errNoDelivery) }}
	worked, err := worker.ProcessOne(context.Background())
	if err != nil || worked {
		t.Fatalf("ProcessOne() = %v, %v", worked, err)
	}
}
