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
	Label   string `json:"label"`
	Status  string `json:"status"`
	Credits *int   `json:"credits"`
}

type DashboardLogin struct {
	Status      string `json:"status"`
	CanDispatch bool   `json:"can_dispatch"`
}

type Dashboard struct {
	Counts        DashboardCounts    `json:"counts"`
	Occupied      int                `json:"occupied"`
	Capacity      int                `json:"capacity"`
	Accounts      []DashboardAccount `json:"accounts"`
	Login         DashboardLogin     `json:"login"`
	LeasedTaskIDs []string           `json:"-"`
}

func (c *Client) Dashboard(ctx context.Context) (Dashboard, error) {
	var raw struct {
		Counts *struct {
			Running, Queued, Succeeded, Uncertain, Failed, Canceled *int
		} `json:"counts"`
		Occupied *int `json:"occupied"`
		Capacity *int `json:"capacity"`
		Accounts *[]struct {
			Lease     *string  `json:"lease"`
			NotBefore *float64 `json:"not_before"`
			Credits   *int     `json:"credits"`
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
		Accounts: make([]DashboardAccount, 0, len(*raw.Accounts)),
		Login:    DashboardLogin{Status: dashboardLoginStatus(raw.Login.Status), CanDispatch: *raw.Login.CanDispatch},
	}
	now := float64(time.Now().Unix())
	for i, account := range *raw.Accounts {
		if account.NotBefore == nil || account.Credits != nil && *account.Credits < 0 {
			return Dashboard{}, ErrInvalidResponse
		}
		status := "idle"
		if account.Lease != nil {
			if !validID(*account.Lease) {
				return Dashboard{}, ErrInvalidResponse
			}
			status = "occupied"
			result.LeasedTaskIDs = append(result.LeasedTaskIDs, *account.Lease)
		} else if *account.NotBefore > now {
			status = "cooldown"
		}
		result.Accounts = append(result.Accounts, DashboardAccount{Label: fmt.Sprintf("1-%d", i+1), Status: status, Credits: account.Credits})
	}
	if len(result.LeasedTaskIDs) != result.Occupied {
		return Dashboard{}, ErrInvalidResponse
	}
	return result, nil
}

func dashboardLoginStatus(status string) string {
	switch status {
	case "valid", "checking", "expired", "login_required", "disabled":
		return status
	default:
		return "unknown"
	}
}
