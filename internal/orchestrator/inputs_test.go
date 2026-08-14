package orchestrator

import (
	"context"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/store/sqlite"
	"minimax-h3-tc/internal/upstream/nodeapi"
)

func TestInputMaterializerImportsDataURIWithRoleAndReusesNodeLocation(t *testing.T) {
	payload := []byte("fake-image-content")
	requestJSON := `{"content":[{"type":"text","text":"test"},{"type":"image_url","role":"first_frame","image_url":{"url":"data:image/jpeg;base64,` + base64.StdEncoding.EncodeToString(payload) + `"}}]}`
	store := &inputStoreFake{task: domain.Task{TaskID: "task-1", RequestJSON: requestJSON}, locations: map[string]sqlite.ArtifactLocation{}}
	client := &inputClientFake{}
	materializer := InputMaterializer{Store: store}

	first, err := materializer.Materialize(context.Background(), "task-1", "node-1", "request-1", client)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Role != "first_frame" || first[0].ArtifactID != "node-input-1" || client.calls != 1 {
		t.Fatalf("first=%+v calls=%d", first, client.calls)
	}
	if client.last.ExternalTaskID != "task-1" {
		t.Fatalf("external task id=%q", client.last.ExternalTaskID)
	}
	second, err := materializer.Materialize(context.Background(), "task-1", "node-1", "request-2", client)
	if err != nil {
		t.Fatal(err)
	}
	if second[0].ArtifactID != "node-input-1" || client.calls != 1 {
		t.Fatalf("second=%+v calls=%d", second, client.calls)
	}
}

type inputStoreFake struct {
	task      domain.Task
	locations map[string]sqlite.ArtifactLocation
}

func (s *inputStoreFake) GetTaskForExecution(context.Context, string) (domain.Task, error) {
	return s.task, nil
}
func (s *inputStoreFake) GetActiveArtifactLocation(_ context.Context, artifactID, nodeID string) (sqlite.ArtifactLocation, error) {
	location, ok := s.locations[artifactID+"|"+nodeID]
	if !ok {
		return sqlite.ArtifactLocation{}, sqlite.ErrArtifactNotFound
	}
	return location, nil
}
func (s *inputStoreFake) RegisterInputArtifact(_ context.Context, artifactID, _, _, nodeID, nodeArtifactID string, _ int64, _, _ string) error {
	s.locations[artifactID+"|"+nodeID] = sqlite.ArtifactLocation{ArtifactID: artifactID, NodeID: nodeID, NodeArtifactID: nodeArtifactID, State: "active"}
	return nil
}

type inputClientFake struct {
	calls int
	last  nodeapi.ImportArtifactRequest
}

func (c *inputClientFake) ImportArtifact(_ context.Context, _ string, request nodeapi.ImportArtifactRequest) (nodeapi.Artifact, error) {
	c.calls++
	c.last = request
	data, err := io.ReadAll(request.Content)
	if err != nil {
		return nodeapi.Artifact{}, err
	}
	if request.Kind != "image" || !strings.HasSuffix(request.Filename, ".jpg") || string(data) != "fake-image-content" {
		return nodeapi.Artifact{}, io.ErrUnexpectedEOF
	}
	return nodeapi.Artifact{ArtifactID: "node-input-1", Kind: "image", SizeBytes: request.ExpectedSize, SHA256: request.ExpectedSHA256, State: "active"}, nil
}
