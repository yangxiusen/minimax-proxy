package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/store/sqlite"
	"minimax-h3-tc/internal/upstream/nodeapi"
)

type fakeStore struct {
	access sqlite.ArtifactAccess
	err    error
}

func (s fakeStore) GetArtifactAccess(context.Context, string) (sqlite.ArtifactAccess, error) {
	return s.access, s.err
}

type fakeNodes struct{ node domain.ModelNode }

func (s fakeNodes) GetModelNode(context.Context, string) (domain.ModelNode, error) {
	return s.node, nil
}

type fakeSecrets struct {
	key string
	err error
}

func (s fakeSecrets) Open([]byte, []byte) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if s.key != "" {
		return s.key, nil
	}
	return "node-secret", nil
}

type fakeClient struct {
	content  *nodeapi.ArtifactContent
	metadata nodeapi.Artifact
	err      error
}

func (c *fakeClient) GetArtifact(context.Context, string, string) (nodeapi.Artifact, error) {
	return c.metadata, c.err
}
func (c *fakeClient) GetArtifactContent(context.Context, string, string, string) (*nodeapi.ArtifactContent, error) {
	return c.content, c.err
}
func (c *fakeClient) ImportArtifact(context.Context, string, nodeapi.ImportArtifactRequest) (nodeapi.Artifact, error) {
	return c.metadata, c.err
}

func TestSignedURLBindsOwnerArtifactMethodAndExpiry(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	service, client := testService(t, now)
	expires := now.Add(15 * time.Minute).Unix()
	signature := service.signature(http.MethodGet, "artifact-1", "owner-a", expires)
	auth := Authorization{Expires: expires, Signature: signature, Method: http.MethodGet}
	content, err := service.Open(context.Background(), "req", "artifact-1", "bytes=0-3", auth)
	if err != nil {
		t.Fatal(err)
	}
	_ = content.Body.Close()
	if content.ContentRange != "bytes 0-3/4" {
		t.Fatalf("content=%+v", content)
	}
	client.content = &nodeapi.ArtifactContent{Body: io.NopCloser(strings.NewReader("data")), StatusCode: 200, ContentLength: 4, ETag: `"digest"`}
	auth.Signature = strings.Repeat("a", len(auth.Signature))
	if _, err := service.Open(context.Background(), "req", "artifact-1", "", auth); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("tampered err=%v", err)
	}
	auth.Signature = signature
	auth.Expires = now.Add(-time.Second).Unix()
	if _, err := service.Open(context.Background(), "req", "artifact-1", "", auth); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired err=%v", err)
	}
}

func TestLegacyPublicURLAllowsOnlySafeAbsoluteHTTPURLs(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "https", value: "https://cdn.example/video.mp4?token=legacy", want: true},
		{name: "http public host", value: "http://video.example:8080/video.mp4", want: true},
		{name: "public ipv4", value: "http://8.8.8.8/video.mp4", want: true},
		{name: "relative", value: "/v2/files/artifact/content", want: false},
		{name: "ftp", value: "ftp://cdn.example/video.mp4", want: false},
		{name: "userinfo", value: "https://user:password@cdn.example/video.mp4", want: false},
		{name: "fragment", value: "https://cdn.example/video.mp4#token", want: false},
		{name: "loopback", value: "http://127.0.0.1:7860/video.mp4", want: false},
		{name: "short loopback", value: "http://127.1:7860/video.mp4", want: false},
		{name: "octal loopback", value: "http://0177.0.0.1:7860/video.mp4", want: false},
		{name: "hex loopback", value: "http://0x7f.0.0.1:7860/video.mp4", want: false},
		{name: "integer loopback", value: "http://2130706433:7860/video.mp4", want: false},
		{name: "private ipv4", value: "http://10.0.0.8/video.mp4", want: false},
		{name: "carrier grade nat", value: "http://100.64.0.1/video.mp4", want: false},
		{name: "benchmark range", value: "http://198.18.0.1/video.mp4", want: false},
		{name: "documentation range", value: "http://192.0.2.1/video.mp4", want: false},
		{name: "reserved range", value: "http://240.0.0.1/video.mp4", want: false},
		{name: "private ipv6", value: "http://[fd00::1]/video.mp4", want: false},
		{name: "documentation ipv6", value: "http://[2001:db8::1]/video.mp4", want: false},
		{name: "localhost", value: "http://localhost:7860/video.mp4", want: false},
		{name: "surrounding whitespace", value: " https://cdn.example/video.mp4 ", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := LegacyPublicURL(test.value)
			if ok != test.want {
				t.Fatalf("LegacyPublicURL(%q) = %q, %t; want valid=%t", test.value, got, ok, test.want)
			}
			if ok && got != test.value {
				t.Fatalf("LegacyPublicURL(%q) normalized unexpectedly to %q", test.value, got)
			}
		})
	}
}

func TestSignedURLUsesNodePublicRouteAndSharedContract(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	client := &fakeClient{}
	service, err := NewService(
		fakeStore{access: accessRecord(now, domain.StatusSucceeded, now.Add(time.Hour).UnixMilli())},
		fakeNodes{node: migrationNode("node", "https://node.example")},
		fakeSecrets{key: "Abcdefghijklmnopqrstuvwx12345678"},
		Options{
			SigningKey: []byte("01234567890123456789012345678901"),
			Now:        func() time.Time { return now },
			ClientFactory: func(*url.URL, string, *http.Client, int64) NodeClient {
				return client
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	signed, err := service.SignURL(context.Background(), "artifact-1", "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://node.example/public/v1/artifacts/node-artifact/content?expires=2000172800&signature=1O8mgkHczi1j49Z-1DHL32jiq8iD6as6KNF6DvbLCZU"
	if signed != want {
		t.Fatalf("SignURL() = %q, want %q", signed, want)
	}
}

func TestSignedURLRejectsWrongOwnerExpiredArtifactAndInvalidNodeURL(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	service, _ := testService(t, now)

	if _, err := service.SignURL(context.Background(), "artifact-1", "owner-b"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("cross owner err=%v", err)
	}

	service.store = fakeStore{access: accessRecord(now, domain.StatusSucceeded, now.Add(-time.Second).UnixMilli())}
	if _, err := service.SignURL(context.Background(), "artifact-1", "owner-a"); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired artifact err=%v", err)
	}

	service.store = fakeStore{access: accessRecord(now, domain.StatusSucceeded, now.Add(time.Hour).UnixMilli())}
	service.nodes = fakeNodes{node: migrationNode("node", "https://node.example/private")}
	if _, err := service.SignURL(context.Background(), "artifact-1", "owner-a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid node URL err=%v", err)
	}

	service.nodes = fakeNodes{node: migrationNode("node", "https://node.example")}
	service.secrets = fakeSecrets{err: errors.New("decrypt failed")}
	if _, err := service.SignURL(context.Background(), "artifact-1", "owner-a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable node key err=%v", err)
	}
}

func TestBearerOwnerAndLifecycleAreCheckedBeforeNodeRead(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	service, _ := testService(t, now)
	if _, err := service.Open(context.Background(), "req", "artifact-1", "", Authorization{BearerOwner: "owner-b"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("cross owner err=%v", err)
	}
	service.store = fakeStore{access: accessRecord(now, domain.StatusSucceeded, now.Add(-time.Second).UnixMilli())}
	if _, err := service.Open(context.Background(), "req", "artifact-1", "", Authorization{BearerOwner: "owner-a"}); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired artifact err=%v", err)
	}
	deleted := accessRecord(now, domain.StatusSucceeded, now.Add(time.Hour).UnixMilli())
	deleted.TaskDeletedAt = now.UnixMilli()
	service.store = fakeStore{access: deleted}
	if _, err := service.Open(context.Background(), "req", "artifact-1", "", Authorization{BearerOwner: "owner-a"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted task err=%v", err)
	}
}

func TestDownloadConcurrencyIsReleasedWhenStreamCloses(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	client := &fakeClient{content: &nodeapi.ArtifactContent{Body: io.NopCloser(strings.NewReader("data")), StatusCode: http.StatusOK, ContentLength: 4, ContentType: "video/mp4", ETag: `"digest"`}}
	service, err := NewService(fakeStore{access: accessRecord(now, domain.StatusSucceeded, now.Add(time.Hour).UnixMilli())}, fakeNodes{node: migrationNode("node", "https://node.example")}, fakeSecrets{}, Options{SigningKey: []byte("01234567890123456789012345678901"), Now: func() time.Time { return now }, OwnerConcurrency: 1, NodeConcurrency: 1, ClientFactory: func(*url.URL, string, *http.Client, int64) NodeClient { return client }})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Open(context.Background(), "req-1", "artifact-1", "", Authorization{BearerOwner: "owner-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Open(context.Background(), "req-2", "artifact-1", "", Authorization{BearerOwner: "owner-a"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second err=%v", err)
	}
	_ = first.Body.Close()
	client.content = &nodeapi.ArtifactContent{Body: io.NopCloser(strings.NewReader("data")), StatusCode: http.StatusOK, ContentLength: 4, ContentType: "video/mp4", ETag: `"digest"`}
	third, err := service.Open(context.Background(), "req-3", "artifact-1", "", Authorization{BearerOwner: "owner-a"})
	if err != nil {
		t.Fatalf("after close err=%v", err)
	}
	_ = third.Body.Close()
}

func testService(t *testing.T, now time.Time) (*Service, *fakeClient) {
	t.Helper()
	client := &fakeClient{content: &nodeapi.ArtifactContent{Body: io.NopCloser(strings.NewReader("data")), StatusCode: http.StatusPartialContent, ContentLength: 4, ContentRange: "bytes 0-3/4", ContentType: "video/mp4", ETag: `"digest"`}}
	service, err := NewService(fakeStore{access: accessRecord(now, domain.StatusSucceeded, now.Add(time.Hour).UnixMilli())}, fakeNodes{node: domain.ModelNode{ModelNodeInput: domain.ModelNodeInput{ID: "node", ServiceURL: "https://node.example", ProtocolVersion: "h3-node-v1", APIKeyNonce: []byte{1}, APIKeyCiphertext: []byte{2}, PollInterval: time.Second, RequestTimeout: time.Second, Enabled: true}}}, fakeSecrets{}, Options{SigningKey: []byte("01234567890123456789012345678901"), Now: func() time.Time { return now }, ClientFactory: func(*url.URL, string, *http.Client, int64) NodeClient { return client }})
	if err != nil {
		t.Fatal(err)
	}
	return service, client
}

func accessRecord(now time.Time, status domain.InternalStatus, expires int64) sqlite.ArtifactAccess {
	return sqlite.ArtifactAccess{Artifact: sqlite.TaskArtifact{ID: "artifact-1", TaskID: "task-1", Kind: "final_video", SizeBytes: 4, SHA256: "digest", State: "active", ExpiresAt: expires}, Location: sqlite.ArtifactLocation{ID: "location", ArtifactID: "artifact-1", NodeID: "node", NodeArtifactID: "node-artifact", State: "active", IsPrimary: true, SizeBytes: 4, SHA256: "digest"}, APIKeyID: "owner-a", TaskStatus: status}
}

type migrationStore struct {
	artifact  sqlite.TaskArtifact
	locations map[string]sqlite.ArtifactLocation
	activated string
}

func (s *migrationStore) GetArtifactAccess(context.Context, string) (sqlite.ArtifactAccess, error) {
	return sqlite.ArtifactAccess{}, sqlite.ErrArtifactNotFound
}
func (s *migrationStore) GetArtifact(context.Context, string) (sqlite.TaskArtifact, error) {
	return s.artifact, nil
}
func (s *migrationStore) GetArtifactLocation(_ context.Context, id string) (sqlite.ArtifactLocation, error) {
	location, ok := s.locations[id]
	if !ok {
		return sqlite.ArtifactLocation{}, sqlite.ErrArtifactNotFound
	}
	return location, nil
}
func (s *migrationStore) GetActiveArtifactLocation(_ context.Context, artifactID, nodeID string) (sqlite.ArtifactLocation, error) {
	for _, location := range s.locations {
		if location.ArtifactID == artifactID && location.NodeID == nodeID && location.State == "active" {
			return location, nil
		}
	}
	return sqlite.ArtifactLocation{}, sqlite.ErrArtifactNotFound
}
func (s *migrationStore) CreateArtifactLocation(_ context.Context, location sqlite.ArtifactLocation) error {
	s.locations[location.ID] = location
	return nil
}
func (s *migrationStore) UpdateImportingArtifactLocation(_ context.Context, id, nodeArtifactID string, size int64, digest string) error {
	location := s.locations[id]
	location.NodeArtifactID, location.SizeBytes, location.SHA256 = nodeArtifactID, size, digest
	s.locations[id] = location
	return nil
}
func (s *migrationStore) ActivatePrimaryArtifactLocation(_ context.Context, artifactID, id string, _ int64, _ string) error {
	for key, location := range s.locations {
		if location.ArtifactID == artifactID {
			location.IsPrimary = key == id
			if key == id {
				location.State = "active"
			}
			s.locations[key] = location
		}
	}
	s.activated = id
	return nil
}

type migrationNodes struct{ nodes map[string]domain.ModelNode }

func (s migrationNodes) GetModelNode(_ context.Context, id string) (domain.ModelNode, error) {
	return s.nodes[id], nil
}

type migrationClient struct {
	metadata     nodeapi.Artifact
	content      []byte
	importResult nodeapi.Artifact
	maxRead      int
}

func (c *migrationClient) GetArtifact(context.Context, string, string) (nodeapi.Artifact, error) {
	return c.metadata, nil
}
func (c *migrationClient) GetArtifactContent(context.Context, string, string, string) (*nodeapi.ArtifactContent, error) {
	return &nodeapi.ArtifactContent{Body: io.NopCloser(bytes.NewReader(c.content)), StatusCode: http.StatusOK, ContentLength: int64(len(c.content)), ETag: `"` + c.metadata.SHA256 + `"`}, nil
}
func (c *migrationClient) ImportArtifact(_ context.Context, _ string, input nodeapi.ImportArtifactRequest) (nodeapi.Artifact, error) {
	buffer := make([]byte, 3)
	var body []byte
	for {
		n, err := input.Content.Read(buffer)
		if n > 0 {
			if n > c.maxRead {
				c.maxRead = n
			}
			body = append(body, buffer[:n]...)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nodeapi.Artifact{}, err
		}
	}
	if !bytes.Equal(body, c.content) {
		return nodeapi.Artifact{}, errors.New("传输内容不一致")
	}
	return c.importResult, nil
}

func TestMigrateStreamsAndOnlySwitchesPrimaryAfterDoubleIntegrityCheck(t *testing.T) {
	payload := []byte("bounded-video-stream")
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	manifest := json.RawMessage(`{"width":832,"height":480}`)
	store := &migrationStore{artifact: sqlite.TaskArtifact{ID: "logical", Kind: "final_video", SizeBytes: int64(len(payload)), SHA256: digest, MediaJSON: string(manifest), State: "active"}, locations: map[string]sqlite.ArtifactLocation{"source": {ID: "source", ArtifactID: "logical", NodeID: "source", NodeArtifactID: "node-source", State: "active", IsPrimary: true, SizeBytes: int64(len(payload)), SHA256: digest}}}
	source := &migrationClient{metadata: nodeapi.Artifact{ArtifactID: "node-source", Kind: "video", SizeBytes: int64(len(payload)), SHA256: digest, MediaManifest: manifest, State: "active"}, content: payload}
	target := &migrationClient{content: payload, importResult: nodeapi.Artifact{ArtifactID: "node-target", Kind: "video", SizeBytes: int64(len(payload)), SHA256: digest, MediaManifest: manifest, State: "active"}}
	nodes := migrationNodes{nodes: map[string]domain.ModelNode{"source": migrationNode("source", "https://source.example"), "target": migrationNode("target", "https://target.example")}}
	service, err := NewService(store, nodes, fakeSecrets{}, Options{SigningKey: []byte("01234567890123456789012345678901"), ClientFactory: func(base *url.URL, _ string, _ *http.Client, _ int64) NodeClient {
		if base.Host == "source.example" {
			return source
		}
		return target
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Migrate(context.Background(), "request", MigrationRequest{OperationID: "migrate-1", ArtifactID: "logical", SourceNodeID: "source", TargetNodeID: "target", TargetLocationID: "target-location", Filename: "video.mp4"})
	if err != nil || result.NodeArtifactID != "node-target" || store.activated != "target-location" || target.maxRead > 3 {
		t.Fatalf("result=%+v activated=%q maxRead=%d err=%v", result, store.activated, target.maxRead, err)
	}
	if sourceLocation := store.locations["source"]; sourceLocation.State != "active" || sourceLocation.IsPrimary {
		t.Fatalf("source=%+v", sourceLocation)
	}
}

func TestMigrateHashMismatchKeepsSourcePrimary(t *testing.T) {
	payload := []byte("video")
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	manifest := json.RawMessage(`{"width":1}`)
	store := &migrationStore{artifact: sqlite.TaskArtifact{ID: "logical", Kind: "final_video", SizeBytes: int64(len(payload)), SHA256: digest, MediaJSON: string(manifest), State: "active"}, locations: map[string]sqlite.ArtifactLocation{"source": {ID: "source", ArtifactID: "logical", NodeID: "source", NodeArtifactID: "node-source", State: "active", IsPrimary: true, SizeBytes: int64(len(payload)), SHA256: digest}}}
	source := &migrationClient{metadata: nodeapi.Artifact{ArtifactID: "node-source", Kind: "video", SizeBytes: int64(len(payload)), SHA256: digest, MediaManifest: manifest, State: "active"}, content: payload}
	target := &migrationClient{content: payload, importResult: nodeapi.Artifact{ArtifactID: "bad", Kind: "video", SizeBytes: int64(len(payload)), SHA256: "bad", MediaManifest: manifest, State: "active"}}
	service, _ := NewService(store, migrationNodes{nodes: map[string]domain.ModelNode{"source": migrationNode("source", "https://source.example"), "target": migrationNode("target", "https://target.example")}}, fakeSecrets{}, Options{SigningKey: []byte("01234567890123456789012345678901"), ClientFactory: func(base *url.URL, _ string, _ *http.Client, _ int64) NodeClient {
		if base.Host == "source.example" {
			return source
		}
		return target
	}})
	if _, err := service.Migrate(context.Background(), "request", MigrationRequest{OperationID: "migrate", ArtifactID: "logical", SourceNodeID: "source", TargetNodeID: "target", TargetLocationID: "target-location"}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("err=%v", err)
	}
	if store.activated != "" || !store.locations["source"].IsPrimary {
		t.Fatalf("activated=%q source=%+v", store.activated, store.locations["source"])
	}
}

func migrationNode(id, serviceURL string) domain.ModelNode {
	return domain.ModelNode{ModelNodeInput: domain.ModelNodeInput{ID: id, ServiceURL: serviceURL, ProtocolVersion: "h3-node-v1", APIKeyNonce: []byte{1}, APIKeyCiphertext: []byte{2}, PollInterval: time.Second, RequestTimeout: time.Second, Enabled: true}}
}
