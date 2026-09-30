package tk2sd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func assetJSON(size int) string {
	return fmt.Sprintf(`{"id":%q,"url":"asset://%s","kind":"image","duration":0,"width":1280,"height":720,"size":%d,"filename":"input.png"}`, assetID, assetID, size)
}

type finalEOFReader struct{ done bool }

func (r *finalEOFReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	return copy(p, "png"), io.EOF
}

func TestUploadMultipartAndFinalDataEOF(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/assets" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("wrong upload request")
		}
		mr, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			return
		}
		part, err := mr.NextPart()
		if err != nil {
			t.Error(err)
			return
		}
		data, err := io.ReadAll(part)
		if err != nil || part.FormName() != "file" || part.FileName() != "input.png" || part.Header.Get("Content-Type") != "image/png" || string(data) != "png" {
			t.Errorf("bad multipart: %v", err)
		}
		if _, err = mr.NextPart(); err != io.EOF {
			t.Errorf("missing closing boundary: %v", err)
		}
		w.WriteHeader(201)
		jsonReply(w, assetJSON(3))
	})
	a, err := c.Upload(context.Background(), "input.png", "image/png", &finalEOFReader{})
	if err != nil || a.ID != assetID || a.URL != "asset://"+assetID || a.Kind != "image" || a.Size != 3 || a.Width != 1280 || a.Filename != "input.png" {
		t.Fatalf("asset: %+v %v", a, err)
	}
}

type brokenReader struct{}

func (brokenReader) Read(p []byte) (int, error) { return copy(p, "bad"), errors.New("source secret") }

func TestUploadSourceErrorsAndEmptyInput(t *testing.T) {
	for _, source := range []io.Reader{brokenReader{}, strings.NewReader("")} {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(201)
			jsonReply(w, assetJSON(3))
		})
		if _, err := c.Upload(context.Background(), "input.png", "image/png", source); err == nil || strings.Contains(err.Error(), "source secret") {
			t.Fatalf("source error: %v", err)
		}
	}
}

func TestUploadValidatesAssetResponse(t *testing.T) {
	for _, body := range []string{strings.Replace(assetJSON(3), "asset://"+assetID, "asset://wrong", 1), strings.Replace(assetJSON(3), `"kind":"image"`, `"kind":"video"`, 1), assetJSON(0), assetJSON(4), strings.Replace(assetJSON(3), `"width":1280`, `"width":0`, 1)} {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(201)
			jsonReply(w, body)
		})
		if _, err := c.Upload(context.Background(), "input.png", "image/png", strings.NewReader("png")); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestUploadRejectsUnsafeHeaders(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid upload reached server") })
	for _, name := range []string{"", "../input.png", `C:\private\input.png`, "bad\r\nname"} {
		if _, err := c.Upload(context.Background(), name, "image/png", strings.NewReader("png")); err == nil {
			t.Errorf("accepted filename %q", name)
		}
	}
	if _, err := c.Upload(context.Background(), "input.png", "image/png\r\nBad: x", strings.NewReader("png")); err == nil {
		t.Fatal("accepted header injection")
	}
	if _, err := c.Upload(context.Background(), "input.png", "image/png", nil); err == nil {
		t.Fatal("accepted nil source")
	}
}

type blockingSource struct {
	started  chan struct{}
	closed   chan struct{}
	finished chan struct{}
	once     sync.Once
}

func (s *blockingSource) Read([]byte) (int, error) {
	close(s.started)
	<-s.closed
	close(s.finished)
	return 0, errors.New("closed source secret")
}

func (s *blockingSource) Close() error { s.once.Do(func() { close(s.closed) }); return nil }

func TestUploadCancellationUnblocksSource(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body) })
	source := &blockingSource{started: make(chan struct{}), closed: make(chan struct{}), finished: make(chan struct{})}
	t.Cleanup(func() { _ = source.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Upload(ctx, "input.png", "image/png", source); done <- err }()
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("source was not read")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lost cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("upload did not return on cancellation")
	}
	select {
	case <-source.finished:
	case <-time.After(time.Second):
		t.Fatal("transport reader leaked")
	}
}

func TestUploadClientTimeoutUnblocksSource(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body) })
	c.http.Timeout = 50 * time.Millisecond
	source := &blockingSource{started: make(chan struct{}), closed: make(chan struct{}), finished: make(chan struct{})}
	t.Cleanup(func() { _ = source.Close() })
	done := make(chan error, 1)
	go func() { _, err := c.Upload(context.Background(), "input.png", "image/png", source); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lost timeout: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("upload did not return on client timeout")
	}
	select {
	case <-source.finished:
	case <-time.After(time.Second):
		t.Fatal("transport reader leaked")
	}
}

func TestUploadEarlyRejectionUnblocksSource(t *testing.T) {
	source := &blockingSource{started: make(chan struct{}), closed: make(chan struct{}), finished: make(chan struct{})}
	t.Cleanup(func() { _ = source.Close() })
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).EnableFullDuplex(); err != nil {
			t.Error(err)
			return
		}
		select {
		case <-source.started:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Connection", "close")
		w.WriteHeader(401)
		jsonReply(w, `{"error":{"code":"AuthenticationError","message":"unauthorized"}}`)
		w.(http.Flusher).Flush()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.Upload(ctx, "input.png", "image/png", source)
	var remote *HTTPError
	if !errors.As(err, &remote) || remote.StatusCode != 401 {
		t.Fatalf("early rejection: %v", err)
	}
	select {
	case <-source.finished:
	case <-time.After(time.Second):
		t.Fatal("transport reader leaked")
	}
}

type repeatedSource struct{}

func (repeatedSource) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestUploadSizeLimitStreamsWithoutBufferingWholeFile(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(201)
		jsonReply(w, assetJSON(3))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Upload(ctx, "input.png", "image/png", repeatedSource{}); err == nil {
		t.Fatal("unbounded upload accepted")
	}
}

func TestUploadVideoAndAudioMetadata(t *testing.T) {
	for _, tc := range []struct {
		kind          string
		duration      float64
		width, height int
		ok            bool
	}{
		{"video", 2, 1280, 720, true}, {"audio", 15, 0, 0, true}, {"video", 1.9, 1280, 720, false}, {"audio", 15.1, 0, 0, false}, {"video", 5, 0, 720, false},
	} {
		t.Run(fmt.Sprintf("%s/%g", tc.kind, tc.duration), func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(201)
				jsonReply(w, fmt.Sprintf(`{"id":%q,"url":"asset://%s","kind":%q,"duration":%g,"width":%d,"height":%d,"size":3,"filename":"input.bin"}`, assetID, assetID, tc.kind, tc.duration, tc.width, tc.height))
			})
			_, err := c.Upload(context.Background(), "input.bin", tc.kind+"/mp4", strings.NewReader("abc"))
			if (err == nil) != tc.ok {
				t.Fatalf("metadata: %v", err)
			}
		})
	}
}
