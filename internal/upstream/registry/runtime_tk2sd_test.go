package registry

import (
	"context"
	"errors"
	"minimax-h3-tc/internal/monitor"
	"minimax-h3-tc/internal/upstream/tk2sd"
	"testing"
	"time"
)

type dashboardProbe struct {
	healthErr    error
	dashboardErr error
	healthCalls  int
	dashboard    tk2sd.Dashboard
}

func (p *dashboardProbe) Health(context.Context) error {
	p.healthCalls++
	return p.healthErr
}

func (p *dashboardProbe) Dashboard(context.Context) (tk2sd.Dashboard, error) {
	return p.dashboard, p.dashboardErr
}

func TestTK2SDDashboardFailureClearsUpstreamMetricsButKeepsHealth(t *testing.T) {
	cache := monitor.NewCache([]monitor.NodeSnapshot{{ID: "tk-node"}})
	f := NodeRuntimeFactory{Cache: cache, Now: func() time.Time { return time.Unix(2_000_000_000, 0) }}
	probe := &dashboardProbe{dashboard: tk2sd.Dashboard{
		Counts: tk2sd.DashboardCounts{Running: 2, Queued: 3}, Occupied: 1, Capacity: 2,
		Accounts:      []tk2sd.DashboardAccount{{Label: "1-1", Status: "occupied"}, {Label: "1-2", Status: "idle"}},
		LeasedTaskIDs: []string{"internal-only"},
		Login:         tk2sd.DashboardLogin{Status: "valid", CanDispatch: true},
	}}
	f.probeTK2SD(context.Background(), "tk-node", true, probe)
	first, _ := cache.Get("tk-node")
	if first.Health != monitor.HealthHealthy || first.TK2SDDashboard == nil || first.TK2SDDashboard.Counts.Queued != 3 || first.TK2SDDashboard.Occupied != 1 || len(first.TK2SDDashboard.LeasedTaskIDs) != 1 {
		t.Fatalf("first snapshot: %+v", first)
	}
	probe.dashboardErr = errors.New("private upstream error")
	f.probeTK2SD(context.Background(), "tk-node", true, probe)
	second, _ := cache.Get("tk-node")
	if second.Health != monitor.HealthHealthy || second.TK2SDDashboard != nil || second.LastError != nil || probe.healthCalls != 2 {
		t.Fatalf("dashboard failure affected health or left stale counts: %+v", second)
	}
	probe.healthErr = errors.New("health failed")
	f.probeTK2SD(context.Background(), "tk-node", true, probe)
	third, _ := cache.Get("tk-node")
	if third.Health != monitor.HealthUnhealthy || third.TK2SDDashboard != nil {
		t.Fatalf("health failure: %+v", third)
	}
}
