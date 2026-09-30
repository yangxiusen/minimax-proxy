package v2

import (
	"context"
	"encoding/json"
	"errors"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/inputobject"
	"testing"
)

type routedReusableStore struct {
	fixedTaskStore
	files       []domain.InputSpoolFile
	owner, hash string
	err         error
}

func (s *routedReusableStore) FindReusableInputObjects(_ context.Context, owner, hash string) (string, []domain.InputSpoolFile, error) {
	s.owner, s.hash = owner, hash
	return `{"resolution":"old-ignored-value"}`, s.files, s.err
}
func TestRoutedInputsReuseOSSWithoutReplacingCurrentRequest(t *testing.T) {
	store := &routedReusableStore{files: []domain.InputSpoolFile{{ID: "old-input", TaskID: "old-task", ContentIndex: 1, ContentType: "image_url", Role: "reference_image", ObjectURL: "https://cdn.example/image.png"}}}
	objects := &inputObjectPreparerFake{err: errors.New("must not upload again")}
	h := &handler{store: store, inputObjects: objects}
	payload := []byte(`{"model":"seed-model","duration":5,"resolution":"new-ignored-value","content":[{"type":"text","text":"test"},{"type":"image_url","role":"reference_image","image_url":{"url":"data:image/png;base64,AAAA"}}]}`)
	got, err := h.prepareRoutedInputs(context.Background(), "new-task", "owner", "hash", payload)
	if err != nil {
		t.Fatal(err)
	}
	if objects.calls != 0 || store.owner != "owner" || store.hash != "hash" {
		t.Fatalf("reuse calls=%d owner=%q hash=%q", objects.calls, store.owner, store.hash)
	}
	var request domain.GenerationRequest
	if err = json.Unmarshal(got.JSON, &request); err != nil {
		t.Fatal(err)
	}
	if request.Model != "seed-model" || request.Resolution != "new-ignored-value" || request.Content[1].ImageURL.URL != "https://cdn.example/image.png" {
		t.Fatalf("request changed: %+v", request)
	}
	if got.Files[0].TaskID != "new-task" || got.Files[0].ID == "old-input" || store.files[0].TaskID != "old-task" {
		t.Fatal("metadata was not rebound independently")
	}
}
func TestRoutedInputsUploadWhenNoReusableObjectsExist(t *testing.T) {
	store := &routedReusableStore{err: domain.ErrTaskNotFound}
	objects := &inputObjectPreparerFake{result: inputobject.PreparedRequest{Enabled: true, JSON: []byte(`{"model":"new"}`)}}
	h := &handler{store: store, inputObjects: objects}
	if _, err := h.prepareRoutedInputs(context.Background(), "new-task", "owner", "hash", []byte(`{"model":"new"}`)); err != nil {
		t.Fatal(err)
	}
	if objects.calls != 1 {
		t.Fatalf("upload calls=%d", objects.calls)
	}
}
