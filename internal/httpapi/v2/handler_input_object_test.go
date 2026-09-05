package v2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/inputobject"
	"minimax-h3-tc/internal/inputspool"
	"minimax-h3-tc/internal/store/sqlite"
)

type inputObjectPreparerFake struct {
	result     inputobject.PreparedRequest
	err        error
	calls      int
	namespaces []string
}

func TestInputObjectNamespaceIsStableAndOwnerIsolated(t *testing.T) {
	first := inputObjectNamespace("owner-a", strings.Repeat("1", 64))
	if first != inputObjectNamespace("owner-a", strings.Repeat("1", 64)) {
		t.Fatal("same request produced a different namespace")
	}
	if first == inputObjectNamespace("owner-b", strings.Repeat("1", 64)) {
		t.Fatal("different owners shared a namespace")
	}
}

func (p *inputObjectPreparerFake) Prepare(_ context.Context, namespace string, _ []byte) (inputobject.PreparedRequest, error) {
	p.calls++
	p.namespaces = append(p.namespaces, namespace)
	return p.result, p.err
}

func TestCreateUsesObjectInputRequestAndPersistsRemoteMetadata(t *testing.T) {
	store := &createSpyStore{}
	objects := &inputObjectPreparerFake{result: inputobject.PreparedRequest{
		Enabled: true,
		JSON:    []byte(`{"model":"MiniMax-H3","content":[{"type":"text","text":"海边日落"},{"type":"image_url","role":"first_frame","image_url":{"url":"https://cdn.example/input.png"}}],"resolution":"2K","duration":5,"ratio":"16:9"}`),
		Files: []domain.InputSpoolFile{{
			ContentIndex: 1, ContentType: "image_url", Role: "first_frame", SourceKind: "data_uri",
			DeclaredMIME: "image/png", DetectedMIME: "image/png", MediaType: "image/png", Extension: ".png",
			RelativePath: "MiniMax-H3/inputs/request/1-deadbeef.png", ObjectURL: "https://cdn.example/input.png",
			SizeBytes: 68, SHA256: strings.Repeat("a", 64),
		}},
	}}
	spooler := inputspool.New(t.TempDir())
	handler := NewHandler(Dependencies{Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}}, Profiles: profiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), InputSpooler: spooler, InputObjects: objects})
	body := []byte(`{"model":"MiniMax-H3","content":[{"type":"text","text":"海边日落"},{"type":"image_url","role":"first_frame","image_url":{"url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="}}],"resolution":"2K","duration":5,"ratio":"16:9"}`)
	response := request(t, handler, http.MethodPost, "/v2/video_generation", body, "key-a")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if objects.calls != 1 || len(store.lastTask.InputSpoolFiles) != 1 || !strings.Contains(store.lastTask.RequestJSON, "https://cdn.example/input.png") || strings.Contains(store.lastTask.RequestJSON, "proxy-input://") {
		t.Fatalf("calls=%d task=%+v", objects.calls, store.lastTask)
	}
	file := store.lastTask.InputSpoolFiles[0]
	if file.TaskID != store.lastTask.TaskID || !strings.HasPrefix(file.ID, "input_") || file.ObjectURL != "https://cdn.example/input.png" {
		t.Fatalf("persisted remote metadata=%+v task_id=%q", file, store.lastTask.TaskID)
	}
	if len(objects.namespaces) != 1 || len(objects.namespaces[0]) != 64 || objects.namespaces[0] == store.lastTask.TaskID {
		t.Fatalf("unstable namespace=%v task_id=%q", objects.namespaces, store.lastTask.TaskID)
	}
}

func TestCreateDoesNotPersistTaskWhenObjectInputPreparationFails(t *testing.T) {
	store := &createSpyStore{}
	objects := &inputObjectPreparerFake{err: errors.New("upload failed")}
	handler := NewHandler(Dependencies{Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}}, Profiles: profiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), InputObjects: objects})
	response := request(t, handler, http.MethodPost, "/v2/video_generation", validCreateJSON(), "key-a")
	if response.Code != http.StatusBadGateway || store.createCalls != 0 {
		t.Fatalf("status=%d creates=%d body=%s", response.Code, store.createCalls, response.Body.String())
	}
}

func TestCreateReusesRepeatedObjectInputRequestWithoutUploadingAgain(t *testing.T) {
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"), sqlite.Options{
		ProtectedSlots: 0, PerKeyLimit: 10, GlobalLimit: 100, Retention: time.Hour, IdempotencyTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	objects := &namespaceObjectPreparer{}
	handler := NewHandler(Dependencies{
		Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}},
		Profiles: profiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		InputObjects: objects,
	})
	body := []byte(`{"model":"MiniMax-H3","content":[{"type":"text","text":"海边日落"},{"type":"image_url","role":"first_frame","image_url":{"url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="}}],"resolution":"2K","duration":5,"ratio":"16:9"}`)

	first := request(t, handler, http.MethodPost, "/v2/video_generation", body, "key-a")
	second := request(t, handler, http.MethodPost, "/v2/video_generation", body, "key-a")
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("first=%d %s second=%d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	var firstBody, secondBody struct {
		TaskID string `json:"task_id"`
	}
	decode(t, first, &firstBody)
	decode(t, second, &secondBody)
	if firstBody.TaskID == "" || secondBody.TaskID == "" || firstBody.TaskID == secondBody.TaskID {
		t.Fatalf("task ids first=%q second=%q", firstBody.TaskID, secondBody.TaskID)
	}
	if objects.calls != 1 {
		t.Fatalf("Prepare calls=%d, want 1", objects.calls)
	}
	firstFiles, err := store.ListInputSpoolFiles(context.Background(), firstBody.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	secondFiles, err := store.ListInputSpoolFiles(context.Background(), secondBody.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstFiles) != 1 || len(secondFiles) != 1 {
		t.Fatalf("first files=%+v second files=%+v", firstFiles, secondFiles)
	}
	if firstFiles[0].RelativePath != secondFiles[0].RelativePath || firstFiles[0].ObjectURL != secondFiles[0].ObjectURL {
		t.Fatalf("object input was not reused: first=%+v second=%+v", firstFiles[0], secondFiles[0])
	}
	if firstFiles[0].ID == secondFiles[0].ID || firstFiles[0].TaskID == secondFiles[0].TaskID {
		t.Fatalf("metadata row was not rebound to the new task: first=%+v second=%+v", firstFiles[0], secondFiles[0])
	}
}

type namespaceObjectPreparer struct {
	calls int
}

func (p *namespaceObjectPreparer) Prepare(_ context.Context, namespace string, _ []byte) (inputobject.PreparedRequest, error) {
	p.calls++
	digest := sha256.Sum256([]byte(namespace))
	suffix := hex.EncodeToString(digest[:8])
	relativePath := fmt.Sprintf("MiniMax-H3/inputs/%s/1-%s.png", namespace, suffix)
	return inputobject.PreparedRequest{
		Enabled: true,
		JSON:    []byte(fmt.Sprintf(`{"model":"MiniMax-H3","content":[{"type":"text","text":"海边日落"},{"type":"image_url","role":"first_frame","image_url":{"url":"https://cdn.example/%s/input.png"}}],"resolution":"2K","duration":5,"ratio":"adaptive"}`, namespace)),
		Files: []domain.InputSpoolFile{{
			ContentIndex: 1, ContentType: "image_url", Role: "first_frame", SourceKind: "data_uri",
			DeclaredMIME: "image/png", DetectedMIME: "image/png", MediaType: "image/png", Extension: ".png",
			RelativePath: relativePath, ObjectURL: "https://cdn.example/" + namespace + "/input.png",
			SizeBytes: 68, SHA256: strings.Repeat("a", 64),
		}},
	}, nil
}
