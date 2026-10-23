package tk2sd

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type DashboardCounts struct {
	Running   int `json:"running"`
	Queued    int `json:"queued"`
	Succeeded int `json:"succeeded"`
	Uncertain int `json:"uncertain"`
	Failed    int `json:"failed"`
	Canceled  int `json:"canceled"`
}

type DashboardAccount struct {
	Label            string `json:"label"`
	Status           string `json:"status"`
	Credits          *int   `json:"credits"`
	ActiveCount      int    `json:"active_count"`
	ConcurrencyLimit int    `json:"concurrency_limit"`
	AvailableSlots   int    `json:"available_slots"`
	Cooldown         bool   `json:"cooldown"`
}

type DashboardLogin struct {
	Status      string `json:"status"`
	CanDispatch bool   `json:"can_dispatch"`
}

type Dashboard struct {
	Counts             DashboardCounts    `json:"counts"`
	Occupied           int                `json:"occupied"`
	Capacity           int                `json:"capacity"`
	Accounts           []DashboardAccount `json:"accounts"`
	Login              DashboardLogin     `json:"login"`
	LeasedTaskIDs      []string           `json:"-"`
	SlotCapacity       int                `json:"slot_capacity"`
	SlotOccupied       int                `json:"slot_occupied"`
	ConcurrencyPerLine int                `json:"concurrency_per_line"`
	CoolingSlots       int                `json:"-"`
}

func (c *Client) Dashboard(ctx context.Context) (Dashboard, error) {
	var raw struct {
		Counts *struct {
			Running, Queued, Succeeded, Uncertain, Failed, Canceled *int
		} `json:"counts"`
		Occupied           *int `json:"occupied"`
		Capacity           *int `json:"capacity"`
		SlotCapacity       *int `json:"slot_capacity"`
		SlotOccupied       *int `json:"slot_occupied"`
		ConcurrencyPerLine *int `json:"concurrency_per_line"`
		Accounts           *[]struct {
			Lease            *string  `json:"lease"`
			NotBefore        *float64 `json:"not_before"`
			Credits          *int     `json:"credits"`
			ActiveCount      *int     `json:"active_count"`
			ConcurrencyLimit *int     `json:"concurrency_limit"`
			AvailableSlots   *int     `json:"available_slots"`
		} `json:"accounts"`
		Login *struct {
			Status      string `json:"status"`
			CanDispatch *bool  `json:"can_dispatch"`
		} `json:"login"`
	}
	if err := c.request(ctx, http.MethodGet, "/v1/dashboard", &raw, false); err != nil {
		return Dashboard{}, err
	}
	if raw.Counts == nil || raw.Occupied == nil || raw.Capacity == nil || raw.Accounts == nil || raw.Login == nil || raw.Login.CanDispatch == nil {
		return Dashboard{}, ErrInvalidResponse
	}
	values := []*int{raw.Counts.Running, raw.Counts.Queued, raw.Counts.Succeeded, raw.Counts.Uncertain, raw.Counts.Failed, raw.Counts.Canceled}
	for _, value := range values {
		if value == nil || *value < 0 {
			return Dashboard{}, ErrInvalidResponse
		}
	}
	if *raw.Capacity < 0 || *raw.Occupied < 0 || *raw.Occupied > *raw.Capacity || len(*raw.Accounts) != *raw.Capacity {
		return Dashboard{}, ErrInvalidResponse
	}
	result := Dashboard{
		Counts:   DashboardCounts{Running: *values[0], Queued: *values[1], Succeeded: *values[2], Uncertain: *values[3], Failed: *values[4], Canceled: *values[5]},
		Occupied: *raw.Occupied, Capacity: *raw.Capacity,
		Accounts:     make([]DashboardAccount, 0, len(*raw.Accounts)),
		Login:        DashboardLogin{Status: dashboardLoginStatus(raw.Login.Status), CanDispatch: *raw.Login.CanDispatch},
		SlotCapacity: *raw.Capacity, SlotOccupied: *raw.Occupied, ConcurrencyPerLine: 1,
	}
	slots := raw.SlotCapacity != nil || raw.SlotOccupied != nil || raw.ConcurrencyPerLine != nil
	if slots {
		if raw.SlotCapacity == nil || raw.SlotOccupied == nil || raw.ConcurrencyPerLine == nil || *raw.ConcurrencyPerLine < 1 || *raw.ConcurrencyPerLine > 20 || *raw.SlotCapacity < 0 || *raw.SlotOccupied < 0 || *raw.SlotCapacity / *raw.ConcurrencyPerLine != *raw.Capacity || *raw.SlotCapacity%*raw.ConcurrencyPerLine != 0 {
			return Dashboard{}, ErrInvalidResponse
		}
		// 降低并发后，现有占用可以暂时超过新容量，不得丢弃有效快照。
		result.SlotCapacity, result.SlotOccupied, result.ConcurrencyPerLine = *raw.SlotCapacity, *raw.SlotOccupied, *raw.ConcurrencyPerLine
	}
	now := float64(time.Now().Unix())
	totalActive, occupiedLines := 0, 0
	for i, account := range *raw.Accounts {
		if account.NotBefore == nil || account.Credits != nil && *account.Credits < 0 {
			return Dashboard{}, ErrInvalidResponse
		}
		active, limit := 0, 1
		if account.Lease != nil {
			if !validID(*account.Lease) {
				return Dashboard{}, ErrInvalidResponse
			}
			active = 1
			if !slots {
				result.LeasedTaskIDs = append(result.LeasedTaskIDs, *account.Lease)
			}
		}
		if slots {
			if account.ActiveCount == nil || account.ConcurrencyLimit == nil || account.AvailableSlots == nil || *account.ActiveCount < 0 || *account.ConcurrencyLimit != result.ConcurrencyPerLine || *account.ActiveCount > result.SlotOccupied-totalActive || *account.AvailableSlots != max(*account.ConcurrencyLimit-*account.ActiveCount, 0) {
				return Dashboard{}, ErrInvalidResponse
			}
			active, limit = *account.ActiveCount, *account.ConcurrencyLimit
		} else if account.ActiveCount != nil || account.ConcurrencyLimit != nil || account.AvailableSlots != nil {
			return Dashboard{}, ErrInvalidResponse
		}
		totalActive += active
		status := "idle"
		if active > 0 {
			status = "occupied"
			occupiedLines++
		}
		cooldown := *account.NotBefore > now
		available := max(limit-active, 0)
		if cooldown {
			result.CoolingSlots += available
			status = "cooldown"
		} else if active >= limit {
			status = "full"
		}
		// 旧版保留 occupied 状态名称，新增名额数不改变线路统计语义。
		if !slots && active > 0 {
			status = "occupied"
		}
		result.Accounts = append(result.Accounts, DashboardAccount{Label: fmt.Sprintf("1-%d", i+1), Status: status, Credits: account.Credits, ActiveCount: active, ConcurrencyLimit: limit, AvailableSlots: available, Cooldown: cooldown})
	}
	if occupiedLines != result.Occupied || totalActive != result.SlotOccupied {
		return Dashboard{}, ErrInvalidResponse
	}
	// 满载、已排队或禁止派发时无需读取任务列表；下一次有空余时重新取完整证据。
	if slots && result.Login.CanDispatch && result.Counts.Queued == 0 && result.SlotOccupied < result.SlotCapacity {
		ids, err := c.activeTaskIDs(ctx)
		if err != nil {
			return Dashboard{}, err
		}
		result.LeasedTaskIDs = ids
	}
	return result, nil
}

func dashboardLoginStatus(status string) string {
	switch status {
	case "valid", "checking", "expired", "login_required", "disabled", "unavailable":
		return status
	default:
		return "unknown"
	}
}
