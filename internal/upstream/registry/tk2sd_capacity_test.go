package registry

import (
	"context"
	"encoding/json"
	"minimax-h3-tc/internal/monitor"
	"minimax-h3-tc/internal/upstream/tk2sd"
	"strings"
	"testing"
	"time"
)

func TestTK2SDSlotSnapshotReachesAdmissionAndRecovers(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	cache := monitor.NewCache([]monitor.NodeSnapshot{{ID: "slots"}, {ID: "other"}})
	f := NodeRuntimeFactory{Cache: cache, Now: func() time.Time { return now }}
	p := &dashboardProbe{dashboard: tk2sd.Dashboard{Capacity: 1, Occupied: 1, SlotCapacity: 3, SlotOccupied: 2, ConcurrencyPerLine: 3, CoolingSlots: 1, Login: tk2sd.DashboardLogin{CanDispatch: true}, LeasedTaskIDs: []string{"owned-a", "owned-b"}, Accounts: []tk2sd.DashboardAccount{{ActiveCount: 2, ConcurrencyLimit: 3, AvailableSlots: 1, Cooldown: true}}}}
	f.probeTK2SD(context.Background(), "slots", true, p)
	snapshot, _ := cache.Get("slots")
	got, ok := tk2sdAdmission(snapshot, now, time.Minute)
	if !ok || got.Capacity != 3 || got.Occupied != 2 || got.Cooling != 1 || len(got.LeasedTaskIDs) != 2 {
		t.Fatalf("slot admission: %+v %v", got, ok)
	}
	encoded, _ := json.Marshal(snapshot.TK2SDDashboard)
	if !strings.Contains(string(encoded), `"concurrency_limit":3`) || strings.Contains(string(encoded), "owned-") {
		t.Fatalf("unsafe/incomplete view: %s", encoded)
	}
	if _, ok := tk2sdAdmission(snapshot, now.Add(61*time.Second), time.Minute); ok {
		t.Fatal("stale snapshot dispatched")
	}
	p.dashboardErr = tk2sd.ErrInvalidResponse
	f.probeTK2SD(context.Background(), "slots", true, p)
	broken, _ := cache.Get("slots")
	if _, ok := tk2sdAdmission(broken, now, time.Minute); ok {
		t.Fatal("partial snapshot dispatched")
	}
	p.dashboardErr = nil
	p.dashboard.SlotOccupied = 1
	p.dashboard.CoolingSlots = 0
	p.dashboard.LeasedTaskIDs = []string{"owned-a"}
	f.probeTK2SD(context.Background(), "slots", true, p)
	recovered, _ := cache.Get("slots")
	if got, ok := tk2sdAdmission(recovered, now, time.Minute); !ok || got.Occupied != 1 || got.Cooling != 0 {
		t.Fatalf("did not recover: %+v", got)
	}
	// 列表可能晚于dashboard观察到新任务，按较大占用保守判断。
	p.dashboard.LeasedTaskIDs = []string{"owned-a", "owned-b"}
	f.probeTK2SD(context.Background(), "slots", true, p)
	later, _ := cache.Get("slots")
	if got, _ := tk2sdAdmission(later, now, time.Minute); got.Occupied != 2 {
		t.Fatalf("unaccounted observations: %+v", got)
	}
}
