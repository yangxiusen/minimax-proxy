package tk2sd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const taskID = "fedcba9876543210fedcba9876543210"
const assetID = "0123456789abcdef0123456789abcdef"
const submitBody = `{"model":"video-1.5-pro","content":[{"type":"text","text":"test"}],"duration":5}`

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	u, _ := url.Parse(s.URL)
	return NewClient(u, "test-secret", s.Client(), 0), s
}

func jsonReply(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

func TestModelsAndHealthContract(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("Accept") != "application/json" {
			t.Error("incorrect request headers or method")
		}
		switch r.URL.Path {
		case "/v1/models":
			jsonReply(w, `{"data":[{"id":"video-1.5-pro","mode":"text_to_video","durations":[5],"credits_per_second":1},{"id":"video-1.5-pro","mode":"image_to_video","durations":[5]}]}`)
		case tasksPath:
			if r.URL.Query().Get("page_size") != "1" || r.URL.Query().Get("page_num") != "1" {
				t.Error("health must read one task")
			}
			jsonReply(w, `{"total":0,"items":[]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	models, err := c.Models(context.Background())
	if err != nil || len(models) != 2 || models[0].ID != "video-1.5-pro" || models[0].Mode != "text_to_video" || models[0].Durations[0] != 5 {
		t.Fatalf("models: %+v, %v", models, err)
	}
	if err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestModelResponseValidation(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":null}`, `{"data":[{"id":"x","mode":"unknown","durations":[5]}]}`, `{"data":[{"id":"x","mode":"text_to_video","durations":[]}]}`, `{"data":[{"id":"x","mode":"text_to_video","durations":[0]}]}`, `{"data":[]} {}`, `{"data":[{"id":"x","mode":"text_to_video","durations":[5]},{"id":"x","mode":"text_to_video","durations":[6]}]}`} {
		t.Run(body, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, body) })
			if _, err := c.Models(context.Background()); err == nil {
				t.Fatal("accepted malformed catalog")
			}
		})
	}
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, `{"data":[]}`) })
	if models, err := c.Models(context.Background()); err != nil || len(models) != 0 {
		t.Fatalf("empty catalog: %v", err)
	}
}

func TestHTTPErrorClassificationAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		status int
		class  ErrorClass
	}{
		{400, ErrorRejected}, {401, ErrorAuthentication}, {403, ErrorAuthentication}, {404, ErrorNotFound}, {409, ErrorConflict}, {413, ErrorRejected}, {422, ErrorRejected}, {429, ErrorTemporary}, {503, ErrorTemporary}, {200, ErrorResponse},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				jsonReply(w, `{"error":{"code":"ServiceError","message":"test-secret https://private.invalid/?signature=secret-signature"},"detail":"ignored"}`)
			})
			_, err := c.Models(context.Background())
			var remote *HTTPError
			if !errors.As(err, &remote) || remote.StatusCode != tc.status || remote.Class != tc.class || remote.Code != "ServiceError" {
				t.Fatalf("wrong classification: %#v %v", remote, err)
			}
			if strings.Contains(fmt.Sprintf("%+v %s", remote, remote.Message), "test-secret") || strings.Contains(remote.Message, "signature") {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestBoundedResponsesAndDetailErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{200, strings.Repeat("x", 129)}, {401, strings.Repeat("x", 129)}, {422, `{"detail":"invalid media"}`}, {503, `<html>test-secret</html>`}} {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		})
		c.maxBody = 128
		_, err := c.Models(context.Background())
		if err == nil {
			t.Fatal("expected error")
		}
		if tc.status != 200 {
			var remote *HTTPError
			if !errors.As(err, &remote) || remote.StatusCode != tc.status {
				t.Fatalf("lost HTTP status: %v", err)
			}
			if tc.status == 422 && remote.Message != "invalid media" {
				t.Fatalf("detail not decoded: %#v", remote)
			}
		}
		if strings.Contains(err.Error(), "test-secret") {
			t.Fatal("body leaked")
		}
	}
}

func TestRedirectsNeverFollowAndClientIsCopied(t *testing.T) {
	var contacted bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { contacted = true }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer s.Close()
	u, _ := url.Parse(s.URL)
	hc := s.Client()
	c := NewClient(u, "test-secret", hc, 0)
	u.Host = "changed.invalid"
	_, err := c.Models(context.Background())
	var remote *HTTPError
	if !errors.As(err, &remote) || remote.Class != ErrorRedirect || contacted || hc.CheckRedirect != nil {
		t.Fatalf("redirect isolation failed: %v", err)
	}
}

func TestInvalidInputsNeverReachNetwork(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid input reached server") })
	for _, id := range []string{"", ".", "..", "a/b", `a\b`, "a?x", "%2f", "a b", "a\n", "https://example.com", strings.Repeat("a", 129)} {
		if _, err := c.Query(context.Background(), id); err == nil {
			t.Errorf("query accepted %q", id)
		}
		if _, err := c.Metadata(context.Background(), id); err == nil {
			t.Errorf("metadata accepted %q", id)
		}
		if err := c.Cancel(context.Background(), id); err == nil {
			t.Errorf("cancel accepted %q", id)
		}
		if _, err := c.Download(context.Background(), id, "unused", 10); err == nil {
			t.Errorf("download accepted %q", id)
		}
	}
	for _, body := range []string{"", `null`, `{}`, submitBody + `{}`, `{"model":"x","content":[],"duration":5}`, `{"model":"x","content":[{"type":"image_url","image_url":{"url":"https://evil.invalid"}}],"duration":5}`, `{"model":"x","content":[{"type":"text","text":"ok"}],"callback_url":"https://evil.invalid","duration":5}`} {
		if _, err := c.Submit(context.Background(), []byte(body), "key"); err == nil {
			t.Errorf("submit accepted %q", body)
		}
	}
	for _, key := range []string{"", "bad\nkey", strings.Repeat("a", 201)} {
		if _, err := c.Submit(context.Background(), []byte(submitBody), key); err == nil {
			t.Error("invalid key accepted")
		}
	}
}

func TestContextCancellation(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.Models(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "http://") {
		t.Fatalf("unsafe or lost timeout: %v", err)
	}
}

func TestInvalidConfigurationAndBasePath(t *testing.T) {
	for _, raw := range []string{"", "file:///tmp", "https://user:password@host.invalid", "https://host.invalid/?key=secret", "https://host.invalid/#secret", "https://host.invalid/a/../b", "https://host.invalid/a%2fb"} {
		u, _ := url.Parse(raw)
		if _, err := NewClient(u, "test-secret", nil, 0).Models(context.Background()); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid base: %q %v", raw, err)
		}
	}
	if _, err := NewClient(nil, "test-secret", nil, 0).Models(context.Background()); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("nil base accepted")
	}
	c, s := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxy/v1/models" {
			t.Error("lost base path")
		}
		jsonReply(w, `{"data":[]}`)
	})
	u, _ := url.Parse(s.URL + "/proxy/")
	if _, err := NewClient(u, "test-secret", c.http, 0).Models(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHealthRejectsMalformedEnvelope(t *testing.T) {
	for _, body := range []string{`{}`, `{"total":0,"items":null}`, `{"total":-1,"items":[]}`, `{"total":0,"items":[{}]}`, `{"total":2,"items":[{},{}]}`} {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, body) })
		if err := c.Health(context.Background()); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestModelsRejectsCatalogOver256UniqueModels(t *testing.T) {
	var rows []string
	for i := range 257 {
		rows = append(rows, fmt.Sprintf(`{"id":"model-%d","mode":"text_to_video","durations":[5]}`, i))
	}
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, `{"data":[`+strings.Join(rows, ",")+`]}`) })
	if _, err := c.Models(context.Background()); err == nil {
		t.Fatal("accepted oversized catalog")
	}
}

func TestErrorMessagesRedactStructuredCredentials(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, `{"error":{"code":"InvalidSignature","message":"bad capability: {\"signature\":\"secret-value\"}"}}`)
	})
	_, err := c.Models(context.Background())
	var remote *HTTPError
	if !errors.As(err, &remote) || strings.Contains(remote.Message, "secret-value") {
		t.Fatalf("credential leaked: %#v", remote)
	}
}
