package inputspool

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"minimax-h3-tc/internal/domain"
)

type restoreStore struct {
	file domain.InputSpoolFile
	err  error
}

func (s restoreStore) GetInputSpoolFile(context.Context, string, string) (domain.InputSpoolFile, error) {
	return s.file, s.err
}

func TestRestorerRebuildsVerifiedProxyInputAsDataURI(t *testing.T) {
	root := t.TempDir()
	payload := []byte{0, 0, 0, 20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	digest := sha256.Sum256(payload)
	relative := filepath.ToSlash(filepath.Join("task-1", "input-1.mp4"))
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	file := domain.InputSpoolFile{ID: "input-1", TaskID: "task-1", ContentIndex: 1, ContentType: "video_url", Role: "reference_video", SourceKind: "data_uri", MediaType: "video/mp4", Extension: ".mp4", RelativePath: relative, SizeBytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}
	request := []byte(`{"content":[{"type":"text","text":"x"},{"type":"video_url","role":"reference_video","video_url":{"url":"proxy-input://task-1/input-1"}}]}`)
	got, err := NewRestorer(root, restoreStore{file: file}).Restore(context.Background(), "task-1", request)
	if err != nil {
		t.Fatalf("Restore() error=%v", err)
	}
	var decoded struct {
		Content []struct {
			VideoURL *struct {
				URL string `json:"url"`
			} `json:"video_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	want := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(payload)
	if decoded.Content[1].VideoURL == nil || decoded.Content[1].VideoURL.URL != want {
		t.Fatalf("restored URL=%+v", decoded.Content[1].VideoURL)
	}
}

func TestRestorerPreservesRequestsWithoutProxyInputs(t *testing.T) {
	request := []byte(`{"content":[{"type":"image_url","role":"reference_image","image_url":{"url":"https://example.com/a.png"}},{"type":"audio_url","role":"reference_audio","audio_url":{"url":"mm_file://audio-1"}}]}`)
	got, err := NewRestorer(t.TempDir(), restoreStore{}).Restore(context.Background(), "task-1", request)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(request) {
		t.Fatalf("request changed: %s", got)
	}
}

func TestRestorerRejectsMetadataAndIntegrityMismatch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "task-1", "input-1.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-a-png"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := domain.InputSpoolFile{ID: "input-1", TaskID: "task-1", ContentIndex: 0, ContentType: "image_url", Role: "reference_image", SourceKind: "data_uri", MediaType: "image/png", Extension: ".png", RelativePath: "task-1/input-1.png", SizeBytes: 9, SHA256: strings.Repeat("0", 64)}
	request := []byte(`{"content":[{"type":"image_url","role":"reference_image","image_url":{"url":"proxy-input://task-1/input-1"}}]}`)
	if _, err := NewRestorer(root, restoreStore{file: file}).Restore(context.Background(), "task-1", request); err == nil {
		t.Fatal("Restore() unexpectedly succeeded")
	}
}

func TestRestorerEnforcesMaterializedRequestLimit(t *testing.T) {
	root := t.TempDir()
	payload := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3, 4}
	digest := sha256.Sum256(payload)
	path := filepath.Join(root, "task-1", "input-1.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	file := domain.InputSpoolFile{ID: "input-1", TaskID: "task-1", ContentIndex: 0, ContentType: "image_url", Role: "reference_image", SourceKind: "data_uri", MediaType: "image/png", Extension: ".png", RelativePath: "task-1/input-1.png", SizeBytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}
	restorer := NewRestorer(root, restoreStore{file: file})
	restorer.maxRequestBytes = 32
	request := []byte(`{"content":[{"type":"image_url","role":"reference_image","image_url":{"url":"proxy-input://task-1/input-1"}}]}`)
	if _, err := restorer.Restore(context.Background(), "task-1", request); err == nil || !strings.Contains(err.Error(), "64 MiB") {
		t.Fatalf("Restore() error=%v", err)
	}
}
