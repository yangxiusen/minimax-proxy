package ucloud

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"minimax-h3-tc/internal/objectstore"
)

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
