package sqlite

import (
	"context"
	"minimax-h3-tc/internal/domain"
	"path/filepath"
	"testing"
	"time"
)

func TestModelCatalogPreservesSelectionsAndRoutes(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "catalog.db"), Options{Now: func() time.Time { return time.Unix(200, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	node, err := s.CreateModelNode(ctx, domain.ModelNodeInput{ID: "n", ServiceURL: "http://node.example", ProtocolVersion: domain.ProtocolTK2SD, APIKeyCiphertext: []byte{1}, APIKeyNonce: []byte{2}, APIKeyFingerprint: "sha256:fixture", Enabled: false, PollInterval: time.Second, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	cap := domain.ModelCapability{SchemaVersion: 1, Modes: []domain.ModeCapability{{Scenario: "t2va", Durations: []int{5}}}}
	cat, err := s.GetModelCatalog(ctx, "n")
	if err != nil {
		t.Fatal(err)
	}
	items := []domain.NodeModel{{ModelID: "a", Present: true, Verified: true, Capabilities: cap}}
	cat, err = s.RefreshModelCatalog(ctx, "n", node.Version, cat.Revision, items, "")
	if err != nil {
		t.Fatal(err)
	}
	if cat.Items[0].Enabled {
		t.Fatal("background discovery opened new model")
	}
	if cat.LastSuccessAt != 200 || cat.ValidUntil != 200+1800 {
		t.Fatalf("catalog=%+v", cat)
	}
	failed, err := s.RefreshModelCatalog(ctx, "n", node.Version, cat.Revision, nil, "model_discovery_failed")
	if err != nil {
		t.Fatal(err)
	}
	if len(failed.Items) != 1 || failed.ValidUntil != cat.ValidUntil {
		t.Fatal("failure erased snapshot")
	}
}
