package v2

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/inputobject"
	"minimax-h3-tc/internal/inputspool"
)

type inputObjectPreparerFake struct {
	result    inputobject.PreparedRequest
	err       error
	calls     int
	namespace string
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
	p.namespace = namespace
	return p.result, p.err
}

func TestCreateUsesObjectInputRequestWithoutLocalSpoolMetadata(t *testing.T) {
	store := &createSpyStore{}
	objects := &inputObjectPreparerFake{result: inputobject.PreparedRequest{Enabled: true, JSON: []byte(`{"model":"MiniMax-H3","content":[{"type":"text","text":"海边日落"},{"type":"image_url","role":"first_frame","image_url":{"url":"https://cdn.example/input.png"}}],"resolution":"2K","duration":5,"ratio":"16:9"}`)}}
	spooler := inputspool.New(t.TempDir())
	handler := NewHandler(Dependencies{Store: store, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "key-a", Enabled: true}}, Profiles: profiles(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), InputSpooler: spooler, InputObjects: objects})
	body := []byte(`{"model":"MiniMax-H3","content":[{"type":"text","text":"海边日落"},{"type":"image_url","role":"first_frame","image_url":{"url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="}}],"resolution":"2K","duration":5,"ratio":"16:9"}`)
	response := request(t, handler, http.MethodPost, "/v2/video_generation", body, "key-a")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if objects.calls != 1 || len(store.lastTask.InputSpoolFiles) != 0 || !strings.Contains(store.lastTask.RequestJSON, "https://cdn.example/input.png") || strings.Contains(store.lastTask.RequestJSON, "proxy-input://") {
		t.Fatalf("calls=%d task=%+v", objects.calls, store.lastTask)
	}
	if len(objects.namespace) != 64 || objects.namespace == store.lastTask.TaskID {
		t.Fatalf("unstable namespace=%q task_id=%q", objects.namespace, store.lastTask.TaskID)
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
