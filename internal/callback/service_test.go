package callback

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"minimax-h3-tc/internal/netguard"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestChallengeWithoutURLMakesNoNetworkRequest(t *testing.T) {
	calls := 0
	service := newTestService(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected request")
	})
	if err := service.Challenge(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	empty := "  "
	if err := service.Challenge(context.Background(), &empty); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("network calls = %d", calls)
	}
}

func TestChallengeRequiresExactEcho(t *testing.T) {
	var got map[string]string
	service := newTestService(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return response(http.StatusOK, `{"challenge":"fixed-challenge"}`), nil
	})
	service.challenge = func() (string, error) { return "fixed-challenge", nil }
	target := "https://callback.example/challenge"
	if err := service.Challenge(context.Background(), &target); err != nil {
		t.Fatal(err)
	}
	if got["challenge"] != "fixed-challenge" {
		t.Fatalf("challenge body = %#v", got)
	}

	service.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"challenge":"wrong"}`), nil
	})
	if err := service.Challenge(context.Background(), &target); !errors.Is(err, ErrChallengeFailed) {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestChallengeRejectsOversizedResponse(t *testing.T) {
	service := newTestService(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"challenge":"fixed","padding":"`+strings.Repeat("x", 128)+`"}`), nil
	})
	service.challenge = func() (string, error) { return "fixed", nil }
	target := "https://callback.example/challenge"
	if err := service.Challenge(context.Background(), &target); !errors.Is(err, ErrChallengeFailed) {
		t.Fatalf("oversized response error = %v", err)
	}
}

type cipherStub struct {
	calls int
}

func (c *cipherStub) Seal(value string) ([]byte, []byte, string, error) {
	c.calls++
	return []byte("nonce"), []byte("encrypted:" + value), "host-fingerprint", nil
}

func TestPrepareTargetChallengesBeforeProducingCiphertext(t *testing.T) {
	cipher := &cipherStub{}
	challengeCalls := 0
	service := newTestService(func(*http.Request) (*http.Response, error) {
		challengeCalls++
		if cipher.calls != 0 {
			t.Fatal("URL was sealed before challenge completed")
		}
		return response(http.StatusOK, `{"challenge":"fixed"}`), nil
	})
	service.challenge = func() (string, error) { return "fixed", nil }
	target := "https://callback.example/hook?token=secret"
	prepared, err := service.PrepareTarget(context.Background(), &target, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if challengeCalls != 1 || cipher.calls != 1 || prepared == nil || len(prepared.Ciphertext) == 0 {
		t.Fatalf("prepared = %#v, challenge calls=%d, cipher calls=%d", prepared, challengeCalls, cipher.calls)
	}

	challengeCalls, cipher.calls = 0, 0
	if prepared, err := service.PrepareTarget(context.Background(), nil, cipher); err != nil || prepared != nil || challengeCalls != 0 || cipher.calls != 0 {
		t.Fatalf("nil URL produced side effects: prepared=%#v err=%v", prepared, err)
	}
}

func TestDeliverSignsStableEventAndBody(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	var eventIDs, signatures, timestamps []string
	service := newTestService(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		mu.Lock()
		bodies = append(bodies, body)
		eventIDs = append(eventIDs, request.Header.Get("X-Minimax-Event-Id"))
		signatures = append(signatures, request.Header.Get("X-Minimax-Signature"))
		timestamps = append(timestamps, request.Header.Get("X-Minimax-Timestamp"))
		mu.Unlock()
		return response(http.StatusInternalServerError, "temporary"), nil
	})
	service.now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	delivery, err := NewDelivery("evt-1", "task-1", "running", 2, json.RawMessage(`{"progress":20}`))
	if err != nil {
		t.Fatal(err)
	}
	delivery.CallbackURL = "https://callback.example/hook"
	delivery.SigningSecret = []byte(strings.Repeat("s", 32))
	first := service.Deliver(context.Background(), delivery)
	second := service.Deliver(context.Background(), delivery)
	if !first.Retryable || !second.Retryable {
		t.Fatalf("results = %#v %#v", first, second)
	}
	if string(bodies[0]) != string(bodies[1]) || eventIDs[0] != "evt-1" || eventIDs[1] != "evt-1" {
		t.Fatalf("retry identity changed: bodies=%q/%q events=%v", bodies[0], bodies[1], eventIDs)
	}
	mac := hmac.New(sha256.New, delivery.SigningSecret)
	_, _ = mac.Write([]byte(timestamps[0] + "." + string(bodies[0])))
	wantSignature := "v1=" + hex.EncodeToString(mac.Sum(nil))
	if signatures[0] != wantSignature {
		t.Fatalf("signature = %q, want %q", signatures[0], wantSignature)
	}
	if delivery.BodyHash == "" {
		t.Fatal("body hash missing")
	}
}

func TestDeliverClassifiesHTTPFailures(t *testing.T) {
	for _, tt := range []struct {
		status    int
		success   bool
		retryable bool
	}{
		{status: 204, success: true},
		{status: 400},
		{status: 408, retryable: true},
		{status: 429, retryable: true},
		{status: 500, retryable: true},
	} {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			service := newTestService(func(*http.Request) (*http.Response, error) {
				return response(tt.status, strings.Repeat("x", 128)), nil
			})
			delivery, _ := NewDelivery("event", "task", "failed", 3, nil)
			delivery.CallbackURL = "https://callback.example/hook"
			delivery.SigningSecret = []byte(strings.Repeat("k", 32))
			result := service.Deliver(context.Background(), delivery)
			if result.Success != tt.success || result.Retryable != tt.retryable || len(result.ResponseSnippet) > 64 {
				t.Fatalf("Deliver() = %#v", result)
			}
		})
	}
}

func TestDeliverTreatsUnsafeURLAsPermanentFailure(t *testing.T) {
	service := newTestService(func(*http.Request) (*http.Response, error) {
		return nil, netguard.ErrUnsafeURL
	})
	delivery, _ := NewDelivery("event", "task", "failed", 3, nil)
	delivery.CallbackURL = "https://callback.example/hook"
	delivery.SigningSecret = []byte(strings.Repeat("k", 32))
	result := service.Deliver(context.Background(), delivery)
	if result.Retryable || result.Error == nil {
		t.Fatalf("Deliver() = %#v", result)
	}
}

func TestDeriveSigningKeyUsesDomainSeparation(t *testing.T) {
	key, err := DeriveSigningKey([]byte(strings.Repeat("m", 32)), "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != sha256.Size || hmac.Equal(key, []byte(strings.Repeat("m", 32))) {
		t.Fatalf("derived key is invalid: %x", key)
	}
}

func newTestService(transport transportFunc) *Service {
	return &Service{
		client:           &http.Client{Transport: transport},
		challengeTimeout: time.Second,
		requestTimeout:   time.Second,
		maxResponseBytes: 64,
		now:              time.Now,
		challenge:        randomChallenge,
	}
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
