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
	"time"
)

func TestPrepareRequestStoresDataURIAsOriginalFileAndRewritesJSON(t *testing.T) {
	root := t.TempDir()
	payload := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3, 4}
	requestJSON := []byte(`{"model":"MiniMax-H3","content":[{"type":"text","text":"hello"},{"type":"image_url","role":"first_frame","image_url":{"url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(payload) + `"}}],"resolution":"768P","duration":5,"ratio":"adaptive"}`)
	prepared, err := New(root).PrepareRequest(context.Background(), "task-1", requestJSON)
	if err != nil {
		t.Fatalf("PrepareRequest() error=%v", err)
	}
	if len(prepared.Files) != 1 {
		t.Fatalf("files len=%d, want 1", len(prepared.Files))
	}
	file := prepared.Files[0]
	if file.Extension != ".png" || file.MediaType != "image/png" || file.ContentIndex != 1 || file.Role != "first_frame" {
		t.Fatalf("file metadata=%+v", file)
	}
	digest := sha256.Sum256(payload)
	if file.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("sha256=%s, want %s", file.SHA256, hex.EncodeToString(digest[:]))
	}
	stored, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file.RelativePath)))
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if string(stored) != string(payload) {
		t.Fatalf("stored bytes=%x, want %x", stored, payload)
	}
	if strings.Contains(string(prepared.JSON), ";base64,") {
		t.Fatalf("rewritten JSON still contains base64: %s", string(prepared.JSON))
	}
	var rewritten struct {
		Content []struct {
			Type     string `json:"type"`
			ImageURL *struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(prepared.JSON, &rewritten); err != nil {
		t.Fatalf("unmarshal rewritten: %v", err)
	}
	if got := rewritten.Content[1].ImageURL.URL; got != "proxy-input://task-1/"+file.ID {
		t.Fatalf("rewritten url=%q, want proxy input ref", got)
	}
	if err := prepared.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "task-1")); !os.IsNotExist(err) {
		t.Fatalf("task dir still exists or stat error=%v", err)
	}
}

func TestCleanupOrphansKeepsLiveTaskAndRemovesOldCandidates(t *testing.T) {
	root := t.TempDir()
	liveDir := filepath.Join(root, "live-task")
	orphanDir := filepath.Join(root, "orphan-task")
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(orphanDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, "input.png"), []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	partPath := filepath.Join(root, "stale.part")
	if err := os.WriteFile(partPath, []byte("part"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(orphanDir, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(partPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := New(root).CleanupOrphans(context.Background(), map[string]bool{"live-task": true}, time.Hour); err != nil {
		t.Fatalf("CleanupOrphans() error=%v", err)
	}
	if _, err := os.Stat(liveDir); err != nil {
		t.Fatalf("live dir removed: %v", err)
	}
	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Fatalf("orphan dir still exists or stat err=%v", err)
	}
	if _, err := os.Stat(partPath); !os.IsNotExist(err) {
		t.Fatalf("part file still exists or stat err=%v", err)
	}
}

func TestPrepareRequestPreservesAudioMP3Extension(t *testing.T) {
	payload := []byte{'I', 'D', '3', 4, 0, 0}
	requestJSON := []byte(`{"content":[{"type":"audio_url","role":"reference_audio","audio_url":{"url":"data:audio/mpeg;base64,` + base64.StdEncoding.EncodeToString(payload) + `"}}]}`)
	prepared, err := New(t.TempDir()).PrepareRequest(context.Background(), "task-audio", requestJSON)
	if err != nil {
		t.Fatalf("PrepareRequest() error=%v", err)
	}
	if len(prepared.Files) != 1 || prepared.Files[0].Extension != ".mp3" || prepared.Files[0].MediaType != "audio/mpeg" {
		t.Fatalf("files=%+v", prepared.Files)
	}
}
