package cleanup

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/store/sqlite"
	"minimax-h3-tc/internal/upstream/nodeapi"
)

func TestRunOnceDeletesRemoteArtifactAndClosesIntent(t *testing.T) {
	store := &cleanupStoreFake{
		nodes: []domain.ModelNode{{ModelNodeInput: domain.ModelNodeInput{
			ID: "node-1", ServiceURL: "http://127.0.0.1:7860", ProtocolVersion: nodeapi.ProtocolVersion,
			APIKeyNonce: []byte{1}, APIKeyCiphertext: []byte{2}, RequestTimeout: time.Second, Enabled: true,
		}}},
		item:     sqlite.ArtifactDeletionItem{ID: "item-1", LocationID: "location-1", NodeID: "node-1", OperationID: "operation-1", LeaseToken: "lease", AttemptCount: 1},
		location: sqlite.ArtifactLocation{ID: "location-1", NodeArtifactID: "node-artifact-1"},
	}
	client := &deleteClientFake{result: nodeapi.DeleteArtifactsResult{
		OperationID: "operation-1", Items: []nodeapi.DeleteArtifactItem{{ArtifactID: "node-artifact-1", Status: "deleted", DeletedBytes: 123}},
	}}
	worker := Worker{
		Store: store, Secrets: cleanupSecretsFake{}, Interval: time.Hour,
		ClientFactory: func(*url.URL, string, *http.Client, int64) DeleteClient { return client },
	}

	worker.RunOnce(context.Background())

	if store.completedItem != "item-1" || store.deletedBytes != 123 || client.request.OperationID != "operation-1" {
		t.Fatalf("store=%+v request=%+v", store, client.request)
	}
}

func TestRunOnceTreatsAuthenticationAsTerminal(t *testing.T) {
	store := &cleanupStoreFake{
		nodes: []domain.ModelNode{{ModelNodeInput: domain.ModelNodeInput{
			ID: "node-1", ServiceURL: "http://127.0.0.1:7860", ProtocolVersion: nodeapi.ProtocolVersion,
			APIKeyNonce: []byte{1}, APIKeyCiphertext: []byte{2}, RequestTimeout: time.Second, Enabled: true,
		}}},
		item:     sqlite.ArtifactDeletionItem{ID: "item-1", LocationID: "location-1", NodeID: "node-1", OperationID: "operation-1", LeaseToken: "lease", AttemptCount: 1},
		location: sqlite.ArtifactLocation{ID: "location-1", NodeArtifactID: "node-artifact-1"},
	}
	worker := Worker{
		Store: store, Secrets: cleanupSecretsFake{}, Interval: time.Hour,
		ClientFactory: func(*url.URL, string, *http.Client, int64) DeleteClient {
			return &deleteClientFake{err: &nodeapi.HTTPError{StatusCode: http.StatusUnauthorized}}
		},
	}

	worker.RunOnce(context.Background())

	if store.failureCode != "node_authentication_failed" || !store.failureTerminal {
		t.Fatalf("failure=%q terminal=%v", store.failureCode, store.failureTerminal)
	}
}

type cleanupStoreFake struct {
	nodes           []domain.ModelNode
	item            sqlite.ArtifactDeletionItem
	location        sqlite.ArtifactLocation
	claimed         bool
	completedItem   string
	deletedBytes    int64
	failureCode     string
	failureTerminal bool
}

func (s *cleanupStoreFake) ListModelNodes(context.Context) ([]domain.ModelNode, error) {
	return s.nodes, nil
}
func (s *cleanupStoreFake) ClaimDeletionItem(context.Context, string, string, time.Duration) (sqlite.ArtifactDeletionItem, error) {
	if s.claimed {
		return sqlite.ArtifactDeletionItem{}, sqlite.ErrNoClaimableDeletion
	}
	s.claimed = true
	return s.item, nil
}
func (s *cleanupStoreFake) GetArtifactLocation(context.Context, string) (sqlite.ArtifactLocation, error) {
	return s.location, nil
}
func (s *cleanupStoreFake) CompleteDeletionItem(_ context.Context, itemID, _ string, _ bool, deletedBytes int64) error {
	s.completedItem, s.deletedBytes = itemID, deletedBytes
	return nil
}
func (s *cleanupStoreFake) FailDeletionItem(_ context.Context, _ string, _ string, code, _ string, _ time.Time, terminal bool) error {
	s.failureCode, s.failureTerminal = code, terminal
	return nil
}

type cleanupSecretsFake struct{}

func (cleanupSecretsFake) Open([]byte, []byte) (string, error) { return "node-key", nil }

type deleteClientFake struct {
	result  nodeapi.DeleteArtifactsResult
	err     error
	request nodeapi.DeleteArtifactsRequest
}

func (c *deleteClientFake) DeleteArtifacts(_ context.Context, _ string, request nodeapi.DeleteArtifactsRequest) (nodeapi.DeleteArtifactsResult, error) {
	c.request = request
	return c.result, c.err
}
