package sqlite

import (
	"context"
	"minimax-h3-tc/internal/domain"
	"testing"
	"time"
)

func reviewedTKNode(t *testing.T, s *Store, now int64) domain.ModelNode {
	t.Helper()
	node, err := s.CreateModelNode(context.Background(), domain.ModelNodeInput{ID: "tk-review", ServiceURL: "http://tk.example", ProtocolVersion: domain.ProtocolTK2SD, APIKeyCiphertext: []byte{1}, APIKeyNonce: []byte{2}, APIKeyFingerprint: "test", Enabled: true, MaxConcurrency: 1, PollInterval: time.Second, RequestTimeout: time.Second, ModelCatalog: &domain.ModelCatalog{Source: "discovered", Status: "ready", LastAttemptAt: now, LastSuccessAt: now, ValidUntil: now + 1800, Items: []domain.NodeModel{{ModelID: "video-1.5-pro", Enabled: true, Present: true, Verified: true, Capabilities: domain.ModelCapability{SchemaVersion: 1, Modes: []domain.ModeCapability{{Scenario: "t2va", Durations: []int{5}}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return node
}
func TestDisableBusyTKNodeWithNormalizedConnection(t *testing.T) {
	ctx := context.Background()
	s := newStore(t, Options{PerKeyLimit: 10, GlobalLimit: 100})
	node := reviewedTKNode(t, s, time.Now().Unix())
	in := task("active", "owner")
	in.Model = "video-1.5-pro"
	in.ProtocolVersion = domain.ProtocolTK2SD
	in.RequestJSON = `{"model":"video-1.5-pro","content":[{"type":"text","text":"test"}],"duration":5}`
	if _, err := s.Create(ctx, in, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextRemote(ctx, node.ID, node.Version, 1); err != nil {
		t.Fatal(err)
	}
	update := node.ModelNodeInput
	update.Enabled = false
	update.BaseURL = ""
	update.JobsBaseURL = ""
	update.PublicBaseURL = ""
	update.HealthPath = ""
	update.SubmitAPIName = ""
	update.CheckAPIName = ""
	if _, err := s.UpdateModelNode(ctx, node.ID, node.Version, update); err != nil {
		t.Fatalf("disable busy tk: %v", err)
	}
}
func TestSelectionEditKeepsRefreshedCatalogExpiry(t *testing.T) {
	ctx := context.Background()
	now := int64(100)
	s := newStore(t, Options{Now: func() time.Time { return time.Unix(now, 0) }})
	node := reviewedTKNode(t, s, now)
	old, err := s.GetModelCatalog(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	now = 1000
	fresh, err := s.RefreshModelCatalog(ctx, node.ID, node.Version, old.Revision, old.Items, "")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Revision != old.Revision {
		t.Fatal("unchanged capabilities bumped revision")
	}
	update := node.ModelNodeInput
	update.ModelCatalog = &old
	if _, err = s.UpdateModelNode(ctx, node.ID, node.Version, update); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetModelCatalog(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSuccessAt != fresh.LastSuccessAt || got.ValidUntil != fresh.ValidUntil {
		t.Fatalf("freshness reverted: got=%+v fresh=%+v", got, fresh)
	}
}
