package tk2sd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/netguard"
)

func TestTaskLifecycleContract(t *testing.T) {
	var submitted []string
	c, s := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("missing bearer")
		}
		switch {
		case r.Method == "POST" && r.URL.Path == tasksPath:
			data, _ := io.ReadAll(r.Body)
			submitted = append(submitted, string(data))
			if r.Header.Get("Idempotency-Key") != "proxy:instance:task" || r.Header.Get("Content-Type") != "application/json" {
				t.Error("wrong submit headers")
			}
			jsonReply(w, fmt.Sprintf(`{"id":%q}`, taskID))
		case r.Method == "GET" && r.URL.Path == tasksPath+"/"+taskID:
			jsonReply(w, fmt.Sprintf(`{"id":%q,"model":"video-1.5-pro","status":"succeeded","error":null,"content":{"video_url":"http://%s/media/tasks/%s?expires=%d&signature=abc"},"duration":5,"ratio":"16:9","resolution":"720p"}`, taskID, r.Host, taskID, time.Now().Add(time.Hour).Unix()))
		case r.Method == "GET" && r.URL.Path == "/v1/tasks/"+taskID:
			jsonReply(w, fmt.Sprintf(`{"id":%q,"status":"succeeded","content":{"duration":4.72,"width":1280,"height":720,"video_url":"http://private.invalid"},"input":{"duration":5}}`, taskID))
		case r.Method == "DELETE" && r.URL.Path == tasksPath+"/"+taskID:
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})
	ctx := context.Background()
	for range 2 {
		id, err := c.Submit(ctx, []byte(submitBody), "proxy:instance:task")
		if err != nil || id != taskID {
			t.Fatalf("submit: %q %v", id, err)
		}
	}
	if len(submitted) != 2 || submitted[0] != submitBody || submitted[1] != submitBody {
		t.Fatal("submit bytes changed")
	}
	task, err := c.Query(ctx, taskID)
	if err != nil || task.ID != taskID || task.Model != "video-1.5-pro" || task.Ratio != "16:9" || task.Resolution != "720p" || !strings.HasPrefix(task.VideoURL, s.URL) {
		t.Fatalf("query: %+v %v", task, err)
	}
	meta, err := c.Metadata(ctx, taskID)
	if err != nil || meta.Duration != 4.72 || meta.Width != 1280 || meta.Height != 720 {
		t.Fatalf("metadata: %+v %v", meta, err)
	}
	if err := c.Cancel(ctx, taskID); err != nil {
		t.Fatal(err)
	}
}

func TestTaskResponseValidation(t *testing.T) {
	for _, body := range []string{`{"id":"other","model":"x","status":"running"}`, fmt.Sprintf(`{"id":%q,"model":"x","status":"uncertain"}`, taskID), fmt.Sprintf(`{"id":%q,"model":"x","status":"succeeded"}`, taskID), fmt.Sprintf(`{"id":%q,"status":"running"}`, taskID), fmt.Sprintf(`{"id":%q,"model":"x","status":"running","error":{"code":"Oops","message":"bad"}}`, taskID)} {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, body) })
		if _, err := c.Query(context.Background(), taskID); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, fmt.Sprintf(`{"id":%q,"model":"x","status":"failed","error":{"code":"GenerationFailed","message":"test-secret https://secret.invalid/?signature=abc"}}`, taskID))
	})
	task, err := c.Query(context.Background(), taskID)
	if err != nil || task.Error == nil || task.Error.Code != "GenerationFailed" || strings.Contains(task.Error.Message, "test-secret") || strings.Contains(task.Error.Message, "signature") {
		t.Fatalf("failed task: %+v %v", task, err)
	}
}

func TestSignedVideoURLValidation(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		ok        bool
	}{
		{"same origin", "{origin}/media/tasks/{id}?expires={expires}&signature=abc", true},
		{"public HTTPS literal", "https://8.8.8.8/media/tasks/{id}?expires={expires}&signature=abc", true},
		{"private other origin", "https://127.0.0.1/media/tasks/{id}?expires={expires}&signature=abc", false},
		{"public HTTP", "http://8.8.8.8/media/tasks/{id}?expires={expires}&signature=abc", false},
		{"wrong path", "{origin}/admin?expires={expires}&signature=abc", false},
		{"other task", "{origin}/media/tasks/other?expires={expires}&signature=abc", false},
		{"expired", "{origin}/media/tasks/{id}?expires=1&signature=abc", false},
		{"missing signature", "{origin}/media/tasks/{id}?expires={expires}", false},
		{"duplicate expiry", "{origin}/media/tasks/{id}?expires={expires}&expires=1&signature=abc", false},
		{"fragment", "{origin}/media/tasks/{id}?expires={expires}&signature=abc#secret", false},
		{"userinfo", "https://user:pass@8.8.8.8/media/tasks/{id}?expires={expires}&signature=abc", false},
		{"relative", "/media/tasks/{id}?expires={expires}&signature=abc", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				u := strings.NewReplacer("{origin}", "http://"+r.Host, "{id}", taskID, "{expires}", fmt.Sprint(time.Now().Add(time.Hour).Unix())).Replace(tc.url)
				jsonReply(w, fmt.Sprintf(`{"id":%q,"model":"x","status":"succeeded","content":{"video_url":%q}}`, taskID, u))
			})
			_, err := c.Query(context.Background(), taskID)
			if (err == nil) != tc.ok {
				t.Fatalf("validation: %v", err)
			}
		})
	}
}

func TestMetadataRejectsMissingOrUntrustedValues(t *testing.T) {
	for _, body := range []string{`{"id":"other","status":"succeeded","content":{"duration":5,"width":1280,"height":720}}`, fmt.Sprintf(`{"id":%q,"status":"running","content":{"duration":5,"width":1280,"height":720}}`, taskID), fmt.Sprintf(`{"id":%q,"status":"succeeded","duration":5,"content":{"width":1280,"height":720}}`, taskID), fmt.Sprintf(`{"id":%q,"status":"succeeded","content":{"duration":-1,"width":1280,"height":720}}`, taskID), fmt.Sprintf(`{"id":%q,"status":"succeeded","content":{"duration":5,"width":0,"height":720}}`, taskID)} {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, body) })
		if _, err := c.Metadata(context.Background(), taskID); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestCancelRequires204(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, `{}`) })
	if err := c.Cancel(context.Background(), taskID); err == nil {
		t.Fatal("accepted 200 as cancellation")
	}
}

func TestDownloadBoundedVideoAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body, length string
		status                          int
		limit                           int64
		ok                              bool
	}{
		{"valid", "video/mp4", "video", "5", 200, 5, true},
		{"chunked", "video/mp4", "video", "", 200, 5, true},
		{"too large declared", "video/mp4", "video", "5", 200, 4, false},
		{"too large streamed", "video/mp4", "video", "", 200, 4, false},
		{"empty", "video/mp4", "", "0", 200, 5, false},
		{"truncated", "video/mp4", "vid", "5", 200, 5, false},
		{"wrong type", "text/html", "video", "5", 200, 5, false},
		{"partial", "video/mp4", "video", "5", 206, 5, false},
		{"not ready", "application/json", `{}`, "2", 409, 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/tasks/"+taskID+"/video" || r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("Range") != "" {
					t.Error("wrong download request")
				}
				w.Header().Set("Content-Type", tc.contentType)
				if tc.length != "" {
					w.Header().Set("Content-Length", tc.length)
				}
				w.WriteHeader(tc.status)
				if tc.length == "" {
					w.(http.Flusher).Flush()
				}
				_, _ = io.WriteString(w, tc.body)
			})
			dir := t.TempDir()
			dst := filepath.Join(dir, "video.mp4")
			n, err := c.Download(context.Background(), taskID, dst, tc.limit)
			if (err == nil) != tc.ok {
				t.Fatalf("download: %d %v", n, err)
			}
			entries, _ := os.ReadDir(dir)
			if tc.ok {
				data, _ := os.ReadFile(dst)
				if n != 5 || string(data) != "video" || len(entries) != 1 {
					t.Fatal("download contents differ")
				}
			} else if len(entries) != 0 {
				t.Fatal("partial file leaked")
			}
		})
	}
}

func TestDownloadFailurePreservesExistingDestination(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = io.WriteString(w, "too long")
	})
	dst := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(dst, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Download(context.Background(), taskID, dst, 2); err == nil {
		t.Fatal("accepted oversized video")
	}
	data, _ := os.ReadFile(dst)
	if string(data) != "original" {
		t.Fatal("existing destination lost")
	}
}

func TestDownloadLogsFailureReasonWithoutCredentials(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "private response")
	})
	var output bytes.Buffer
	c.logger = slog.New(slog.NewJSONHandler(&output, nil))
	if _, err := c.Download(context.Background(), taskID, filepath.Join(t.TempDir(), "video.mp4"), 1024); err == nil {
		t.Fatal("expected invalid media type")
	}
	log := output.String()
	for _, value := range []string{"节点视频下载开始", "节点视频响应校验失败", `"reason":"invalid_content_type"`, `"status_code":200`} {
		if !strings.Contains(log, value) {
			t.Fatalf("missing %s: %s", value, log)
		}
	}
	for _, secret := range []string{"test-secret", "private response"} {
		if strings.Contains(log, secret) {
			t.Fatalf("secret leaked: %s", log)
		}
	}
}

func TestDownloadTruncatedVideoIsRetryableWithoutSavingPartialFile(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "10")
		_, _ = io.WriteString(w, "short")
	})
	dir := t.TempDir()
	_, err := c.Download(context.Background(), taskID, filepath.Join(dir, "video.mp4"), 1024)
	if !errors.Is(err, ErrIncompleteVideo) {
		t.Fatalf("truncated download = %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("partial video remains: %v", entries)
	}
}

type staticResolver struct{ addresses []netip.Addr }

func (r staticResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.addresses, nil
}

func TestPublicVideoURLChecksAllDNSAddresses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		addresses []netip.Addr
		ok        bool
	}{
		{"public", []netip.Addr{netip.MustParseAddr("8.8.8.8")}, true},
		{"mixed", []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1")}, false},
		{"mapped loopback", []netip.Addr{netip.MustParseAddr("::ffff:127.0.0.1")}, false},
		{"empty", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				jsonReply(w, fmt.Sprintf(`{"id":%q,"model":"x","status":"succeeded","content":{"video_url":"https://media.invalid/media/tasks/%s?expires=%d&signature=abc"}}`, taskID, taskID, time.Now().Add(time.Hour).Unix()))
			})
			c.guard = netguard.New(netguard.Options{Resolver: staticResolver{tc.addresses}})
			_, err := c.Query(context.Background(), taskID)
			if (err == nil) != tc.ok {
				t.Fatalf("DNS check: %v", err)
			}
		})
	}
}
