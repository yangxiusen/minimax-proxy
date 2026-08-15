package orchestrator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

func TestInputMaterializerImportsProxyInputFromSpoolFile(t *testing.T) {
	root := t.TempDir()
	payload := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3}
	relativePath := filepath.ToSlash(filepath.Join("task-spool", "input_spool.png"))
	if err := os.MkdirAll(filepath.Join(root, "task-spool"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(relativePath)), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	requestJSON := `{"content":[{"type":"image_url","role":"first_frame","image_url":{"url":"proxy-input://task-spool/input_spool"}}]}`
	store := &inputStoreFake{
		task:      domain.Task{TaskID: "task-spool", RequestJSON: requestJSON},
		locations: map[string]sqlite.ArtifactLocation{},
		spoolFiles: map[string]domain.InputSpoolFile{"task-spool|input_spool": {
			ID: "input_spool", TaskID: "task-spool", ContentIndex: 0, ContentType: "image_url", Role: "first_frame",
			MediaType: "image/png", Extension: ".png", RelativePath: relativePath, SizeBytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]),
		}},
	}
	client := &inputClientFake{wantKind: "image", wantSuffix: ".png", wantPayload: payload}
	materializer := InputMaterializer{Store: store, InputSpoolRoot: root}
	inputs, err := materializer.Materialize(context.Background(), "task-spool", "node-1", "request-spool", client)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0].ArtifactID != "node-input-1" || inputs[0].Role != "first_frame" {
		t.Fatalf("inputs=%+v", inputs)
	}
	if client.calls != 1 || client.last.SourceArtifactID != "input_spool" || client.last.Filename != "input_spool.png" {
		t.Fatalf("client calls=%d last=%+v", client.calls, client.last)
	}
}

type inputStoreFake struct {
	task       domain.Task
	locations  map[string]sqlite.ArtifactLocation
	spoolFiles map[string]domain.InputSpoolFile
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
func (s *inputStoreFake) GetInputSpoolFile(_ context.Context, taskID, inputID string) (domain.InputSpoolFile, error) {
	file, ok := s.spoolFiles[taskID+"|"+inputID]
	if !ok {
		return domain.InputSpoolFile{}, domain.ErrTaskNotFound
	}
	return file, nil
}

type inputClientFake struct {
	calls       int
	last        nodeapi.ImportArtifactRequest
	wantKind    string
	wantSuffix  string
	wantPayload []byte
}

func (c *inputClientFake) ImportArtifact(_ context.Context, _ string, request nodeapi.ImportArtifactRequest) (nodeapi.Artifact, error) {
	c.calls++
	c.last = request
	data, err := io.ReadAll(request.Content)
	if err != nil {
		return nodeapi.Artifact{}, err
	}
	wantKind := c.wantKind
	if wantKind == "" {
		wantKind = "image"
	}
	wantSuffix := c.wantSuffix
	if wantSuffix == "" {
		wantSuffix = ".jpg"
	}
	wantPayload := c.wantPayload
	if wantPayload == nil {
		wantPayload = []byte("fake-image-content")
	}
	if request.Kind != wantKind || !strings.HasSuffix(request.Filename, wantSuffix) || string(data) != string(wantPayload) {
		return nodeapi.Artifact{}, io.ErrUnexpectedEOF
	}
	return nodeapi.Artifact{ArtifactID: "node-input-1", Kind: "image", SizeBytes: request.ExpectedSize, SHA256: request.ExpectedSHA256, State: "active"}, nil
}
