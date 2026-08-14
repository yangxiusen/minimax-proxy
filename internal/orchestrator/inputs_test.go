package orchestrator

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
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

func TestInputMaterializerLogsBase64UploadMilestonesWithoutContent(t *testing.T) {
	var logs bytes.Buffer
	payload := []byte("fake-image-content")
	requestJSON := `{"content":[{"type":"image_url","role":"first_frame","image_url":{"url":"data:image/jpeg;base64,` + base64.StdEncoding.EncodeToString(payload) + `"}}]}`
	store := &inputStoreFake{task: domain.Task{TaskID: "task-log", RequestJSON: requestJSON}, locations: map[string]sqlite.ArtifactLocation{}}
	materializer := InputMaterializer{Store: store, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}

	if _, err := materializer.Materialize(context.Background(), "task-log", "node-log", "request-log", &inputClientFake{}); err != nil {
		t.Fatal(err)
	}
	output := logs.String()
	for _, expected := range []string{"输入素材处理开始", "输入素材 Base64 解码完成", "开始向节点导入输入素材", "输入素材节点导入完成", `"task_id":"task-log"`, `"node_id":"node-log"`} {
		if !strings.Contains(output, expected) {
			t.Fatalf("logs missing %q: %s", expected, output)
		}
	}
	if strings.Contains(output, base64.StdEncoding.EncodeToString(payload)) || strings.Contains(output, string(payload)) {
		t.Fatalf("logs leaked input media: %s", output)
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
