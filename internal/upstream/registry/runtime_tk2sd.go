package registry

import (
	"context"
	"errors"
	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
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
		if !ok || snapshot.TK2SDDashboard == nil || snapshot.CheckedAt.IsZero() || snapshot.CheckedAt.Before(runtimeNow(f.Now).Add(-maxAge)) {
			return domain.TK2SDAdmission{}, false
		}
		dashboard := snapshot.TK2SDDashboard
		cooling := 0
		for _, account := range dashboard.Accounts {
			if account.Status == "cooldown" {
				cooling++
			}
		}
		return domain.TK2SDAdmission{Capacity: dashboard.Capacity, Occupied: dashboard.Occupied, Cooling: cooling, Queued: dashboard.Counts.Queued, CanDispatch: dashboard.Login.CanDispatch, LeasedTaskIDs: dashboard.LeasedTaskIDs}, true
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
				accounts[i] = monitor.TK2SDAccount{Label: account.Label, Status: account.Status, Credits: account.Credits}
			}
			s.TK2SDDashboard = &monitor.TK2SDDashboard{
				Counts: monitor.TK2SDCounts(dashboard.Counts), Occupied: dashboard.Occupied, Capacity: dashboard.Capacity,
				Accounts: accounts, Login: monitor.TK2SDLogin(dashboard.Login), LeasedTaskIDs: dashboard.LeasedTaskIDs,
			}
		}
	})
}
