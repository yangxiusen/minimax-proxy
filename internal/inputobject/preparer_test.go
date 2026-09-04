package inputobject

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/objectstore"
)

type configStoreFake struct{ config domain.ObjectStorageConfig }

func (s configStoreFake) GetObjectStorageConfig(context.Context) (domain.ObjectStorageConfig, error) {
	return s.config, nil
}

type secretFake struct{}

func (secretFake) Open(_, ciphertext []byte) (string, error) { return string(ciphertext), nil }

type uploadFake struct {
	calls     int
	key, mime string
	keys      []string
	payload   []byte
	err       error
}

func (u *uploadFake) Upload(_ context.Context, payload []byte, key, mime string) (string, error) {
	u.calls, u.key, u.mime = u.calls+1, key, mime
	u.keys = append(u.keys, key)
	u.payload = append([]byte(nil), payload...)
	if u.err != nil {
		return "", u.err
	}
	return "https://cdn.example.com/" + key, nil
}

func TestPrepareUploadsDataURIAndLeavesURLInputsUnchanged(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\ncontent")
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	request := []byte(`{"content":[{"type":"image_url","role":"first_frame","image_url":{"url":"` + dataURI + `"}},{"type":"video_url","video_url":{"url":"https://source.example/video.mp4"}}]}`)
	upload := &uploadFake{}
	preparer := New(configStoreFake{domain.ObjectStorageConfig{UploadBase64Inputs: true, LastTestStatus: "passed", PublicKeyCiphertext: []byte("public"), PrivateKeyCiphertext: []byte("private"), RequestTimeout: time.Minute}}, secretFake{}, func(domain.ObjectStorageConfig, string, string) (objectstore.DataStore, error) { return upload, nil })
	result, err := preparer.Prepare(context.Background(), "request-namespace", request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Enabled || upload.calls != 1 || upload.mime != "image/png" || string(upload.payload) != string(png) {
		t.Fatalf("result=%+v upload=%+v", result, upload)
	}
	if len(result.Files) != 1 {
		t.Fatalf("metadata files=%+v, want one object-backed input", result.Files)
	}
	file := result.Files[0]
	if file.ContentIndex != 0 || file.ContentType != "image_url" || file.Role != "first_frame" ||
		file.SourceKind != "data_uri" || file.DeclaredMIME != "image/png" || file.DetectedMIME != "image/png" ||
		file.MediaType != "image/png" || file.Extension != ".png" || file.RelativePath != upload.key ||
		file.ObjectURL != "https://cdn.example.com/"+upload.key || file.SizeBytes != int64(len(png)) || len(file.SHA256) != 64 {
		t.Fatalf("object input metadata=%+v", file)
	}
	if !strings.Contains(upload.key, "/request-namespace/") {
		t.Fatalf("unstable object key=%q", upload.key)
	}
	var decoded map[string]any
	if err := json.Unmarshal(result.JSON, &decoded); err != nil {
		t.Fatal(err)
	}
	content := decoded["content"].([]any)
	first := content[0].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	second := content[1].(map[string]any)["video_url"].(map[string]any)["url"].(string)
	if first == dataURI || second != "https://source.example/video.mp4" {
		t.Fatalf("rewritten=%s unchanged=%s", first, second)
	}
}

func TestPrepareRejectsUnknownOrMismatchedMediaBeforeUpload(t *testing.T) {
	for _, test := range []struct{ name, dataURI string }{
		{name: "unknown bytes", dataURI: "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not-an-image"))},
		{name: "mismatched declaration", dataURI: "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xffjpeg"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			upload := &uploadFake{}
			request := []byte(`{"content":[{"type":"image_url","image_url":{"url":"` + test.dataURI + `"}}]}`)
			preparer := New(configStoreFake{domain.ObjectStorageConfig{UploadBase64Inputs: true, LastTestStatus: "passed", PublicKeyCiphertext: []byte("public"), PrivateKeyCiphertext: []byte("private")}}, secretFake{}, func(domain.ObjectStorageConfig, string, string) (objectstore.DataStore, error) { return upload, nil })
			if _, err := preparer.Prepare(context.Background(), "request-namespace", request); err == nil || upload.calls != 0 {
				t.Fatalf("calls=%d err=%v", upload.calls, err)
			}
		})
	}
}

func TestPrepareDisabledDoesNotUpload(t *testing.T) {
	upload := &uploadFake{}
	request := []byte(`{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}`)
	preparer := New(configStoreFake{domain.ObjectStorageConfig{}}, secretFake{}, func(domain.ObjectStorageConfig, string, string) (objectstore.DataStore, error) { return upload, nil })
	result, err := preparer.Prepare(context.Background(), "task-1", request)
	if err != nil || result.Enabled || upload.calls != 0 || string(result.JSON) != string(request) {
		t.Fatalf("result=%+v calls=%d err=%v", result, upload.calls, err)
	}
}

func TestPrepareURLOnlyRequestSkipsUnavailableObjectStorage(t *testing.T) {
	upload := &uploadFake{}
	request := []byte(`{"content":[{"type":"video_url","video_url":{"url":"https://source.example/video.mp4"}}]}`)
	preparer := New(configStoreFake{domain.ObjectStorageConfig{UploadBase64Inputs: true, LastTestStatus: "failed"}}, secretFake{}, func(domain.ObjectStorageConfig, string, string) (objectstore.DataStore, error) {
		t.Fatal("factory must not be called")
		return upload, nil
	})
	result, err := preparer.Prepare(context.Background(), "task-1", request)
	if err != nil || !result.Enabled || upload.calls != 0 || string(result.JSON) != string(request) {
		t.Fatalf("result=%+v calls=%d err=%v", result, upload.calls, err)
	}
}

func TestPrepareReturnsUploadFailure(t *testing.T) {
	upload := &uploadFake{err: errors.New("upload failed")}
	png := []byte("\x89PNG\r\n\x1a\ncontent")
	request := []byte(`{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(png) + `"}}]}`)
	preparer := New(configStoreFake{domain.ObjectStorageConfig{UploadBase64Inputs: true, LastTestStatus: "passed", PublicKeyCiphertext: []byte("public"), PrivateKeyCiphertext: []byte("private")}}, secretFake{}, func(domain.ObjectStorageConfig, string, string) (objectstore.DataStore, error) { return upload, nil })
	if _, err := preparer.Prepare(context.Background(), "task-1", request); err == nil {
		t.Fatal("expected upload error")
	}
}
