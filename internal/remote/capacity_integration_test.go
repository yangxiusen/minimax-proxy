package remote_test

import (
	"context"
	"errors"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/remote"
	"minimax-h3-tc/internal/routing"
	"minimax-h3-tc/internal/upstream/tk2sd"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestIntegrationTK2SDNodeKeepsMultipleTasksActiveWithinCapacity(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	var ids [3]string
	for i := range ids {
		ids[i] = f.create("key-a", integrationBody(t, false))
	}
	for _, id := range ids[:2] {
		if err := f.processor.ProcessOne(ctx); !errors.Is(err, domain.ErrRemotePending) {
			t.Fatalf("submit %s: %v", id, err)
		}
		if got := f.task(id); got.Status != domain.StatusRunning || !got.UpstreamSlotActive {
			t.Fatalf("not active: %+v", got)
		}
	}
	if err := f.processor.ProcessOne(ctx); !errors.Is(err, domain.ErrUpstreamBusy) {
		t.Fatalf("third task exceeded capacity: %v", err)
	}
	if got := f.task(ids[2]); got.Status != domain.StatusQueuedOpen && got.Status != domain.StatusQueuedLocked {
		t.Fatalf("third task was not queued: %+v", got)
	}
	f.upstream.mu.Lock()
	submissions := len(f.upstream.jobs)
	f.upstream.mu.Unlock()
	if submissions != 2 {
		t.Fatalf("submitted=%d want=2", submissions)
	}
	f.upstream.setStatus(ids[0], "succeeded")
	if err := f.processor.ProcessTask(ctx, f.task(ids[0])); err != nil {
		t.Fatal(err)
	}
	if err := f.processor.ProcessOne(ctx); !errors.Is(err, domain.ErrRemotePending) {
		t.Fatalf("third task not claimed after slot released: %v", err)
	}
	f.upstream.mu.Lock()
	submissions = len(f.upstream.jobs)
	f.upstream.mu.Unlock()
	if submissions != 3 || !f.task(ids[2]).UpstreamSlotActive {
		t.Fatalf("after release: submitted=%d, third=%+v", submissions, f.task(ids[2]))
	}
}

func TestIntegrationTK2SDSpareNodeDrainsCommonProxyQueue(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	spareUpstream := newIntegrationUpstream(t)
	spareUpstream.nodeID = "tk-spare"
	spareUpstream.store = f.store
	input := f.node.ModelNodeInput
	input.ID = "tk-spare"
	input.ServiceURL = spareUpstream.server.URL
	input.MaxConcurrency = 1
	catalog, err := (&routing.Inventory{Store: f.store, HTTPClient: spareUpstream.server.Client()}).Preview(ctx, input, integrationNodeKey)
	if err != nil {
		t.Fatal(err)
	}
	input.ModelCatalog = &catalog
	spare, err := f.store.CreateModelNode(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse(spareUpstream.server.URL)
	spareProcessor := &remote.Processor{
		Store: f.store, Client: tk2sd.NewClient(base, integrationNodeKey, &http.Client{Timeout: 3 * time.Second}, 1<<20),
		Inputs: f.processor.Inputs, NodeID: spare.ID, NodeVersion: spare.Version, NodeURL: base,
		Capacity: 1, PollInterval: time.Second,
	}
	ids := []string{f.create("key-a", integrationBody(t, false)), f.create("key-a", integrationBody(t, false)), f.create("key-a", integrationBody(t, false)), f.create("key-a", integrationBody(t, false))}
	for _, id := range ids[:2] {
		if err := f.processor.ProcessOne(ctx); !errors.Is(err, domain.ErrRemotePending) || !f.task(id).UpstreamSlotActive {
			t.Fatalf("first node submit %s: %v", id, err)
		}
	}
	if err := f.processor.ProcessOne(ctx); !errors.Is(err, domain.ErrUpstreamBusy) {
		t.Fatalf("full first node claimed task: %v", err)
	}
	if err := spareProcessor.ProcessOne(ctx); !errors.Is(err, domain.ErrRemotePending) || f.task(ids[2]).UpstreamID != spare.ID {
		t.Fatalf("spare node did not drain shared queue: %v", err)
	}
	if err := spareProcessor.ProcessOne(ctx); !errors.Is(err, domain.ErrUpstreamBusy) {
		t.Fatalf("full spare node claimed task: %v", err)
	}
	if got := f.task(ids[3]); got.UpstreamSlotActive || got.Status != domain.StatusQueuedOpen && got.Status != domain.StatusQueuedLocked {
		t.Fatalf("fourth task did not wait in Proxy: %+v", got)
	}
}
