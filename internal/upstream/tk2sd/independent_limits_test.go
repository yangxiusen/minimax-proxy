package tk2sd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func independentLimitsDashboard() map[string]any {
	body := slotDashboard(2, 2, 3)
	body["slot_capacity"] = 7
	accounts := body["accounts"].([]map[string]any)
	accounts[1]["concurrency_limit"] = 5
	accounts[1]["available_slots"] = 4
	return body
}

func TestDashboardAcceptsIndependentAccountLimits(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/dashboard" {
			_ = json.NewEncoder(w).Encode(independentLimitsDashboard())
			return
		}
		jsonReply(w, `{"total":3,"items":[{"id":"a","status":"running"},{"id":"b","status":"running"},{"id":"c","status":"running"}]}`)
	})
	got, err := c.Dashboard(context.Background())
	if err != nil {
		t.Fatalf("independent account limits rejected: %v", err)
	}
	if got.SlotCapacity != 7 || got.SlotOccupied != 3 || len(got.LeasedTaskIDs) != 3 || got.Accounts[1].ConcurrencyLimit != 5 || got.Accounts[1].AvailableSlots != 4 {
		t.Fatalf("incorrect independent capacity: %+v", got)
	}
}

func TestDashboardRejectsInvalidIndependentCapacity(t *testing.T) {
	for _, mode := range []string{"wrong_total", "zero_limit", "excessive_limit", "wrong_available"} {
		t.Run(mode, func(t *testing.T) {
			body := independentLimitsDashboard()
			account := body["accounts"].([]map[string]any)[1]
			switch mode {
			case "wrong_total":
				body["slot_capacity"] = 8
			case "zero_limit":
				account["concurrency_limit"] = 0
			case "excessive_limit":
				account["concurrency_limit"] = 21
			case "wrong_available":
				account["available_slots"] = 5
			}
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(body) })
			if _, err := c.Dashboard(context.Background()); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("invalid independent capacity accepted: %v", err)
			}
		})
	}
}
