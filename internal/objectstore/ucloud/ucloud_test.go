package ucloud

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"minimax-h3-tc/internal/objectstore"
)

func TestUploadStreamsPayloadAndVerifiesPublicAccess(t *testing.T) {
	var uploaded []byte
	var contentType string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPut {
			uploaded, _ = io.ReadAll(request.Body)
			contentType = request.Header.Get("Content-Type")
		}
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	store, err := New(Config{BucketName: "bucket", FileHost: server.URL, PublicBaseURL: server.URL + "/public", PublicKey: "public-key", PrivateKey: "private-key", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("image-bytes")
	publicURL, err := store.Upload(t.Context(), payload, "MiniMax-H3/inputs/task-1/0.png", "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if string(uploaded) != string(payload) || contentType != "image/png" || publicURL != server.URL+"/public/MiniMax-H3/inputs/task-1/0.png" {
		t.Fatalf("payload=%q content_type=%q url=%q", uploaded, contentType, publicURL)
	}
}

func TestUploadFileUsesMultipartAndVerifiesPublicAccess(t *testing.T) {
	var mutex sync.Mutex
	requests := make([]string, 0, 5)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		requests = append(requests, request.Method+" "+request.URL.RequestURI()+" auth="+request.Header.Get("Authorization"))
		mutex.Unlock()
		switch {
		case request.Method == http.MethodPost && request.URL.RawQuery == "uploads":
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"BlkSize":4194304,"UploadId":"upload-1"}`))
		case request.Method == http.MethodPut && request.URL.Query().Get("partNumber") == "0":
			response.Header().Set("ETag", "part-etag")
		case request.Method == http.MethodHead:
			response.WriteHeader(http.StatusOK)
		default:
			response.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	filePath := t.TempDir() + "/video.mp4"
	if err := os.WriteFile(filePath, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(Config{BucketName: "bucket", FileHost: server.URL, PublicBaseURL: server.URL + "/public", PublicKey: "public-key", PrivateKey: "private-key", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	publicURL, err := store.UploadFile(t.Context(), filePath, "MiniMax-H3/2033-05-18/task-1.mp4", "video/mp4")
	if err != nil {
		t.Fatal(err)
	}
	if publicURL != server.URL+"/public/MiniMax-H3/2033-05-18/task-1.mp4" {
		t.Fatalf("public URL=%q", publicURL)
	}
	joined := strings.Join(requests, "\n")
	for _, expected := range []string{"POST /MiniMax-H3/2033-05-18/task-1.mp4?uploads", "PUT /MiniMax-H3/2033-05-18/task-1.mp4?partNumber=0&uploadId=upload-1", "HEAD /MiniMax-H3/2033-05-18/task-1.mp4", "HEAD /public/MiniMax-H3/2033-05-18/task-1.mp4 auth="} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %q in requests:\n%s", expected, joined)
		}
	}
}

func TestUploadFileRejectsObjectThatIsNotPublic(t *testing.T) {
	deleted := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.RawQuery == "uploads":
			_, _ = response.Write([]byte(`{"BlkSize":4194304,"UploadId":"upload-1"}`))
		case request.Method == http.MethodPut:
			response.Header().Set("ETag", "part-etag")
		case request.Method == http.MethodHead && strings.HasPrefix(request.URL.Path, "/public/"):
			response.WriteHeader(http.StatusForbidden)
		case request.Method == http.MethodDelete:
			deleted = true
			response.WriteHeader(http.StatusNoContent)
		default:
			response.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	filePath := t.TempDir() + "/video.mp4"
	if err := os.WriteFile(filePath, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(Config{BucketName: "bucket", FileHost: server.URL, PublicBaseURL: server.URL + "/public", PublicKey: "public-key", PrivateKey: "private-key", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UploadFile(t.Context(), filePath, "MiniMax-H3/2033-05-18/task-1.mp4", "video/mp4")
	var storageError *objectstore.Error
	if !strings.Contains(err.Error(), "公开") || !asObjectStoreError(err, &storageError) || storageError.Code != "ucloud_public_read_failed" {
		t.Fatalf("error=%v", err)
	}
	if !deleted {
		t.Fatal("object was not deleted after public access verification failed")
	}
}

func asObjectStoreError(err error, target **objectstore.Error) bool {
	value, ok := err.(*objectstore.Error)
	if ok {
		*target = value
	}
	return ok
}

func TestUploadPartFailureKeepsOriginalStatusAfterAbort(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.RawQuery == "uploads":
			_, _ = w.Write([]byte(`{"BlkSize":4194304,"UploadId":"upload-1"}`))
		case r.Method == http.MethodPut:
			w.Header().Set("X-Request-Id", "storage-request-1")
			w.WriteHeader(http.StatusServiceUnavailable)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	path := t.TempDir() + "/input.mp4"
	if err := os.WriteFile(path, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(Config{BucketName: "bucket", FileHost: server.URL, PublicBaseURL: server.URL + "/public", PublicKey: "private-public-key", PrivateKey: "private-secret-key", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UploadFile(t.Context(), path, "video.mp4", "video/mp4")
	var failure *objectstore.Error
	if !errors.As(err, &failure) || !failure.Retryable || !strings.Contains(failure.Message, "HTTP 503") || !strings.Contains(failure.Message, "上传分片") {
		t.Fatalf("part failure overwritten by cleanup: %+v", err)
	}
	if failure.Operation != "multipart_part" || failure.HTTPStatus != 503 || failure.RequestID != "storage-request-1" || failure.Reason != "http_status" {
		t.Fatalf("missing structured failure: %+v", failure)
	}
	for _, secret := range []string{server.URL, path, "private-secret-key", "private-public-key"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("sensitive storage error", err)
		}
	}
}

func TestObjectVerificationFailureHasStageAndStatus(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	store, err := New(Config{BucketName: "bucket", FileHost: server.URL, PublicBaseURL: server.URL + "/public", PublicKey: "public", PrivateKey: "private", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Upload(t.Context(), []byte("image"), "image.png", "image/png")
	if err == nil || !strings.Contains(err.Error(), "对象校验") || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("verification diagnostic missing: %v", err)
	}
}

func TestUploadRedirectKeepsFinalFailureStatus(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/video.mp4" {
			http.Redirect(w, r, "/final?uploads", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	path := t.TempDir() + "/video.mp4"
	if err := os.WriteFile(path, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(Config{BucketName: "bucket", FileHost: server.URL, PublicBaseURL: server.URL, PublicKey: "public", PrivateKey: "private", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UploadFile(t.Context(), path, "video.mp4", "video/mp4")
	var failure *objectstore.Error
	if !errors.As(err, &failure) || failure.HTTPStatus != 503 || !failure.Retryable {
		t.Fatalf("redirect hid final temporary failure: %+v", err)
	}
}
