package remote_test

import (
	"context"
	"errors"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/monitor"
	"minimax-h3-tc/internal/remote"
	"minimax-h3-tc/internal/routing"
	"minimax-h3-tc/internal/upstream/registry"
	"minimax-h3-tc/internal/upstream/tk2sd"
	"net/http"
	"net/url"
	"testing"
	"time"
)

type capacityNodeSecret struct{}

func (capacityNodeSecret) Open([]byte, []byte) (string, error) { return integrationNodeKey, nil }

func TestIntegrationTK2SDSlowCapacityProbeDoesNotBlockOtherRuntime(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := f.node.ModelNodeInput
	input.RequestTimeout = 10 * time.Second
	slowNode, err := f.store.UpdateModelNode(ctx, f.node.ID, f.node.Version, input)
	if err != nil {
		t.Fatal(err)
	}
	block, started, submitted := make(chan struct{}), make(chan struct{}, 1), make(chan struct{}, 1)
	f.upstream.slotLimit, f.upstream.dashboardBlock, f.upstream.dashboardStarted = 2, block, started
	fast := newIntegrationUpstream(t)
	fast.nodeID, fast.store, fast.slotLimit, fast.submitObserved = "fast-slots", f.store, 1, submitted
	input.ID, input.ServiceURL, input.MaxConcurrency = fast.nodeID, fast.server.URL, 1
	catalog, err := (&routing.Inventory{Store: f.store, HTTPClient: fast.server.Client()}).Preview(ctx, input, integrationNodeKey)
	if err != nil {
		t.Fatal(err)
	}
	input.ModelCatalog = &catalog
	fastNode, err := f.store.CreateModelNode(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	factory := registry.NodeRuntimeFactory{Store: f.store, Cache: monitor.NewCache(nil), NodeSecrets: capacityNodeSecret{}, InputSpoolRoot: f.root, MonitorInterval: 20 * time.Millisecond, Logger: f.processor.Logger}
	slowRuntime, err := factory.Start(ctx, slowNode)
	if err != nil {
		t.Fatal(err)
	}
	defer slowRuntime.Stop()
	defer close(block)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("slow probe did not start")
	}
	fastRuntime, err := factory.Start(ctx, fastNode)
	if err != nil {
		t.Fatal(err)
	}
	defer fastRuntime.Stop()
	id := f.create("key-a", integrationBody(t, false))
	fastRuntime.Wake()
	select {
	case <-submitted:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked capacity probe prevented other node submission")
	}
	if task := f.task(id); task.UpstreamID != fast.nodeID || !task.UpstreamSlotActive {
		t.Fatalf("other runtime did not claim: %+v", task)
	}
}

func TestIntegrationTK2SDSingleLineSlotsRefillAfterSnapshotFailure(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	input := f.node.ModelNodeInput
	input.MaxConcurrency = 3
	updated, err := f.store.UpdateModelNode(ctx, f.node.ID, f.node.Version, input)
	if err != nil {
		t.Fatal(err)
	}
	f.processor.Capacity, f.processor.NodeVersion = 3, updated.Version
	f.upstream.slotLimit = 3
	var snapshot domain.TK2SDAdmission
	valid := false
	refresh := func() error {
		d, err := f.processor.Client.(*tk2sd.Client).Dashboard(ctx)
		valid = err == nil
		if valid {
			snapshot = domain.TK2SDAdmission{Capacity: d.SlotCapacity, Occupied: max(d.SlotOccupied, len(d.LeasedTaskIDs)), Cooling: d.CoolingSlots, Queued: d.Counts.Queued, CanDispatch: d.Login.CanDispatch, LeasedTaskIDs: d.LeasedTaskIDs}
		}
		return err
	}
	f.processor.Admission = func() (domain.TK2SDAdmission, bool) { return snapshot, valid }
	ids := make([]string, 5)
	for i := range ids {
		ids[i] = f.create("key-a", integrationBody(t, false))
	}
	for _, id := range ids[:3] {
		if err := refresh(); err != nil {
			t.Fatal(err)
		}
		if err := f.processor.ProcessOne(ctx); !errors.Is(err, domain.ErrRemotePending) || !f.task(id).UpstreamSlotActive {
			t.Fatalf("single line did not use all slots: %s %v", id, err)
		}
	}
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	if err := f.processor.ProcessOne(ctx); !errors.Is(err, domain.ErrUpstreamBusy) {
		t.Fatalf("full node dispatched: %v", err)
	}
	f.upstream.setStatus(ids[0], "succeeded")
	if err := f.processor.ProcessTask(ctx, f.task(ids[0])); err != nil {
		t.Fatal(err)
	}
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	if err := f.processor.ProcessOne(ctx); !errors.Is(err, domain.ErrRemotePending) || !f.task(ids[3]).UpstreamSlotActive {
		t.Fatalf("did not refill slot: %v", err)
	}
	f.upstream.mu.Lock()
	f.upstream.dashboardUnavailable = true
	f.upstream.mu.Unlock()
	if err := refresh(); err == nil {
		t.Fatal("expected failed snapshot")
	}
	if err := f.processor.ProcessOne(ctx); !errors.Is(err, domain.ErrUpstreamBusy) || f.task(ids[4]).UpstreamSlotActive {
		t.Fatalf("invalid snapshot dispatched: %v", err)
	}
	f.upstream.setStatus(ids[1], "succeeded")
	if err := f.processor.ProcessTask(ctx, f.task(ids[1])); err != nil || f.task(ids[1]).UpstreamSlotActive {
		t.Fatalf("snapshot failure blocked existing completion: %v", err)
	}
	f.upstream.mu.Lock()
	f.upstream.dashboardUnavailable = false
	f.upstream.mu.Unlock()
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	if err := f.processor.ProcessOne(ctx); !errors.Is(err, domain.ErrRemotePending) || !f.task(ids[4]).UpstreamSlotActive {
		t.Fatalf("snapshot recovery did not resume queue: %v", err)
	}
	f.upstream.mu.Lock()
	submissions := len(f.upstream.jobs)
	f.upstream.mu.Unlock()
	if submissions != 5 {
		t.Fatalf("unexpected submissions=%d", submissions)
	}
}

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
