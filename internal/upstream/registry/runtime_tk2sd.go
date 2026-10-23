package registry

import (
	"context"
	"errors"
	"log/slog"
	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/logsafe"
	"minimax-h3-tc/internal/monitor"
	"minimax-h3-tc/internal/remote"
	"minimax-h3-tc/internal/scheduler"
	"minimax-h3-tc/internal/upstream/tk2sd"
	"net/http"
	"sync"
	"time"
)

type tkRuntimeStore interface {
	remote.Store
	remote.AssetStore
}
type remoteDispatcher struct{ processor *remote.Processor }

type tk2sdMonitorClient interface {
	Health(context.Context) error
	Dashboard(context.Context) (tk2sd.Dashboard, error)
}

func (d remoteDispatcher) ProcessOne(ctx context.Context) error {
	err := d.processor.ProcessOne(ctx)
	if errors.Is(err, domain.ErrRemotePending) {
		return nil
	}
	return err
}

func (f NodeRuntimeFactory) startTK2SD(parent context.Context, node domain.ModelNode, input domain.ModelNodeInput, upstream config.UpstreamConfig) (Runtime, error) {
	store, ok := f.Store.(tkRuntimeStore)
	if !ok || f.NodeSecrets == nil {
		return nil, errors.New("tk2sd 运行时依赖未配置")
	}
	key, err := f.NodeSecrets.Open(input.APIKeyNonce, input.APIKeyCiphertext)
	if err != nil {
		return nil, errors.New("节点凭据解密失败")
	}
	if err = f.initializeSnapshot(parent, node, input, upstream); err != nil {
		return nil, err
	}
	client := tk2sd.NewClient(upstream.ServiceURL, key, &http.Client{Timeout: upstream.RequestTimeout}, 1<<20).WithLogger(f.Logger)
	processor := &remote.Processor{Store: store, Client: client, Inputs: &remote.InputMaterializer{Store: store, Root: f.InputSpoolRoot, Timeout: upstream.RequestTimeout}, NodeID: node.ID, NodeVersion: node.Version, NodeURL: upstream.ServiceURL, Capacity: input.MaxConcurrency, PollInterval: input.PollInterval, Now: f.Now, Logger: f.Logger}
	maxAge := upstream.RequestTimeout + monitorInterval(f.MonitorInterval)
	processor.Admission = func() (domain.TK2SDAdmission, bool) {
		snapshot, ok := f.Cache.Get(node.ID)
		if !ok {
			return domain.TK2SDAdmission{}, false
		}
		return tk2sdAdmission(snapshot, runtimeNow(f.Now), maxAge)
	}
	ctx, cancel := context.WithCancel(parent)
	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	health := func(context.Context) error {
		if !input.Enabled {
			return domain.ErrNodeDisabled
		}
		return cachedOfficialSchedulable(f.Cache, node.ID, runtimeNow(f.Now), upstream.RequestTimeout+monitorInterval(f.MonitorInterval))
	}
	slots := make([]scheduler.Slot, max(1, input.MaxConcurrency))
	for i := range slots {
		slots[i] = scheduler.Slot{ID: node.ID, Health: health, Processor: remoteDispatcher{processor}}
	}
	dispatcher := scheduler.New(slots, time.Second, f.Logger)
	var group sync.WaitGroup
	group.Add(3)
	go func() { defer group.Done(); dispatcher.Run(ctx) }()
	go func() {
		defer group.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			_ = processor.Resume(ctx)
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-ticker.C:
			}
		}
	}()
	go func() {
		defer group.Done()
		ticker := time.NewTicker(monitorInterval(f.MonitorInterval))
		defer ticker.Stop()
		for {
			probeCtx, stop := context.WithTimeout(ctx, upstream.RequestTimeout)
			f.probeTK2SD(probeCtx, node.ID, input.Enabled, client)
			stop()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() { group.Wait(); close(done) }()
	return &nodeRuntime{cancel: cancel, done: done, wake: func() {
		dispatcher.Wake()
		select {
		case wake <- struct{}{}:
		default:
		}
	}}, nil
}

func (f NodeRuntimeFactory) probeTK2SD(ctx context.Context, nodeID string, enabled bool, client tk2sdMonitorClient) {
	previous, _ := f.Cache.Get(nodeID)
	healthErr := client.Health(ctx)
	var dashboard tk2sd.Dashboard
	var dashboardErr error
	if healthErr == nil {
		dashboard, dashboardErr = client.Dashboard(ctx)
	}
	now := runtimeNow(f.Now)
	f.Cache.Update(nodeID, func(s *monitor.NodeSnapshot) {
		s.Applying = false
		s.Disabled = !enabled
		s.CheckedAt = now
		s.UpdatedAt = now
		s.TK2SDDashboard = nil
		if healthErr != nil {
			s.Health = monitor.HealthUnhealthy
			s.LastError = &monitor.ErrorSnapshot{Code: "tk2sd_api_unhealthy"}
			return
		}
		s.Health = monitor.HealthHealthy
		s.LastHealthyAt = now
		s.LastError = nil
		if dashboardErr == nil {
			accounts := make([]monitor.TK2SDAccount, len(dashboard.Accounts))
			for i, account := range dashboard.Accounts {
				accounts[i] = monitor.TK2SDAccount{Label: account.Label, Status: account.Status, Credits: account.Credits, ActiveCount: account.ActiveCount, ConcurrencyLimit: account.ConcurrencyLimit, AvailableSlots: account.AvailableSlots, Cooldown: account.Cooldown}
			}
			s.TK2SDDashboard = &monitor.TK2SDDashboard{
				Counts: monitor.TK2SDCounts(dashboard.Counts), Occupied: dashboard.Occupied, Capacity: dashboard.Capacity,
				Accounts: accounts, Login: monitor.TK2SDLogin(dashboard.Login), LeasedTaskIDs: dashboard.LeasedTaskIDs,
				SlotCapacity: dashboard.SlotCapacity, SlotOccupied: dashboard.SlotOccupied, ConcurrencyPerLine: dashboard.ConcurrencyPerLine, CoolingSlots: dashboard.CoolingSlots,
			}
		}
	})
	logger := f.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if healthErr == nil && dashboardErr != nil && (previous.TK2SDDashboard != nil || previous.CheckedAt.IsZero() || previous.Health != monitor.HealthHealthy) {
		logger.WarnContext(ctx, "tk2sd容量快照获取失败，暂停该节点新派发并继续已有任务对账", "stage", "tk2sd_capacity", "node_id", nodeID, "error_code", "tk2sd_capacity_unavailable", "error_reason", logsafe.Error(dashboardErr))
	}
	old := previous.TK2SDDashboard
	if healthErr == nil && dashboardErr == nil && (old == nil || old.SlotCapacity != dashboard.SlotCapacity || old.SlotOccupied != dashboard.SlotOccupied || old.CoolingSlots != dashboard.CoolingSlots || old.Counts.Queued != dashboard.Counts.Queued || old.Login.CanDispatch != dashboard.Login.CanDispatch || old.ConcurrencyPerLine != dashboard.ConcurrencyPerLine) {
		reason := "ready"
		if !dashboard.Login.CanDispatch {
			reason = "upstream_dispatch_paused"
		} else if dashboard.Counts.Queued > 0 {
			reason = "upstream_queue_pending"
		} else if dashboard.SlotOccupied >= dashboard.SlotCapacity || dashboard.CoolingSlots >= dashboard.SlotCapacity-dashboard.SlotOccupied {
			reason = "no_available_slots"
		}
		logger.InfoContext(ctx, "tk2sd容量快照已更新", "stage", "tk2sd_capacity", "node_id", nodeID, "slot_capacity", dashboard.SlotCapacity, "slot_occupied", dashboard.SlotOccupied, "concurrency_per_line", dashboard.ConcurrencyPerLine, "cooling_slots", dashboard.CoolingSlots, "upstream_queued", dashboard.Counts.Queued, "can_dispatch", dashboard.Login.CanDispatch, "dispatch_reason", reason)
	}
}

func tk2sdAdmission(snapshot monitor.NodeSnapshot, now time.Time, maxAge time.Duration) (domain.TK2SDAdmission, bool) {
	dashboard := snapshot.TK2SDDashboard
	if dashboard == nil || snapshot.CheckedAt.IsZero() || snapshot.CheckedAt.Before(now.Add(-maxAge)) {
		return domain.TK2SDAdmission{}, false
	}
	capacity, occupied, cooling := dashboard.SlotCapacity, max(dashboard.SlotOccupied, len(dashboard.LeasedTaskIDs)), dashboard.CoolingSlots
	if dashboard.ConcurrencyPerLine == 0 {
		capacity, occupied = dashboard.Capacity, dashboard.Occupied
		for _, account := range dashboard.Accounts {
			if account.Status == "cooldown" {
				cooling++
			}
		}
	}
	return domain.TK2SDAdmission{Capacity: capacity, Occupied: occupied, Cooling: cooling, Queued: dashboard.Counts.Queued, CanDispatch: dashboard.Login.CanDispatch, LeasedTaskIDs: dashboard.LeasedTaskIDs}, true
}
