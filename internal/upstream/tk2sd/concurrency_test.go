package tk2sd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func slotDashboard(lines, limit, active int) map[string]any {
	accounts := make([]map[string]any, lines)
	remaining, occupied := active, 0
	for i := range accounts {
		count := min(remaining, limit)
		if i == lines-1 {
			count = remaining
		}
		remaining -= count
		var lease any
		if count > 0 {
			occupied++
			lease = fmt.Sprintf("task-%d", i)
		}
		accounts[i] = map[string]any{"lease": lease, "not_before": 0, "credits": 100, "active_count": count, "concurrency_limit": limit, "available_slots": max(limit-count, 0)}
	}
	return map[string]any{"counts": map[string]int{"running": active, "queued": 0, "succeeded": 0, "uncertain": 0, "failed": 0, "canceled": 0}, "capacity": lines, "occupied": occupied, "slot_capacity": lines * limit, "slot_occupied": active, "concurrency_per_line": limit, "accounts": accounts, "login": map[string]any{"status": "valid", "can_dispatch": true}}
}

func TestDashboardUsesSlotsAndAllActiveTasks(t *testing.T) {
	for _, active := range []int{2, 101} {
		t.Run(strconv.Itoa(active), func(t *testing.T) {
			lines, limit := 1, 3
			if active > 3 {
				lines, limit = 6, 20
			}
			pages := 0
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-secret" {
					t.Error("missing authorization")
				}
				if r.URL.Path == "/v1/dashboard" {
					_ = json.NewEncoder(w).Encode(slotDashboard(lines, limit, active))
					return
				}
				if r.URL.Path != tasksPath || r.URL.Query().Get("filter.status") != "running" || r.URL.Query().Get("page_size") != "100" {
					t.Errorf("unexpected active task request: %s", r.URL)
					w.WriteHeader(400)
					return
				}
				pages++
				page, _ := strconv.Atoi(r.URL.Query().Get("page_num"))
				items := []map[string]string{}
				for i := (page - 1) * 100; i < min(page*100, active); i++ {
					items = append(items, map[string]string{"id": fmt.Sprintf("task-%d", i), "status": "running"})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"total": active, "items": items})
			})
			got, err := c.Dashboard(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(got)
			var view map[string]any
			_ = json.Unmarshal(encoded, &view)
			if view["slot_capacity"] != float64(lines*limit) || view["slot_occupied"] != float64(active) || view["concurrency_per_line"] != float64(limit) || len(got.LeasedTaskIDs) != active || pages != (active+99)/100 {
				t.Fatalf("incomplete capacity: %s activeIDs=%d pages=%d", encoded, len(got.LeasedTaskIDs), pages)
			}
			if strings.Contains(string(encoded), "task-0") {
				t.Fatal("internal task IDs exposed")
			}
		})
	}
}

func TestDashboardAcceptsActiveTasksAboveLoweredLimit(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(slotDashboard(1, 1, 3)) })
	got, err := c.Dashboard(context.Background())
	if err != nil {
		t.Fatal("lowering must not invalidate active task snapshot", err)
	}
	encoded, _ := json.Marshal(got)
	if !strings.Contains(string(encoded), `"slot_occupied":3`) || !strings.Contains(string(encoded), `"available_slots":0`) {
		t.Fatalf("lost active tasks: %s", encoded)
	}
}

func TestDashboardCooldownBlocksOnlyRemainingSlots(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/dashboard" {
			body := slotDashboard(2, 3, 1)
			body["accounts"].([]map[string]any)[0]["not_before"] = 9999999999
			_ = json.NewEncoder(w).Encode(body)
		} else {
			jsonReply(w, `{"total":1,"items":[{"id":"task-0","status":"running"}]}`)
		}
	})
	got, err := c.Dashboard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.CoolingSlots != 2 || got.Accounts[0].Status != "cooldown" || !got.Accounts[0].Cooldown || got.Accounts[0].AvailableSlots != 2 || got.SlotCapacity != 6 {
		t.Fatalf("cooling counted occupied slots twice: %+v", got)
	}
}

func TestDashboardRejectsIncompleteSlotEvidence(t *testing.T) {
	for _, mode := range []string{"partial_fields", "wrong_available", "missing_page", "duplicate_ids", "changing_total", "task_request_failed"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/dashboard" {
					body := slotDashboard(6, 20, 101)
					if mode == "partial_fields" {
						delete(body, "slot_occupied")
					}
					if mode == "wrong_available" {
						body["accounts"].([]map[string]any)[0]["available_slots"] = 20
					}
					_ = json.NewEncoder(w).Encode(body)
					return
				}
				if mode == "task_request_failed" {
					w.WriteHeader(503)
					jsonReply(w, `{}`)
					return
				}
				page, _ := strconv.Atoi(r.URL.Query().Get("page_num"))
				items := []map[string]string{}
				for i := (page - 1) * 100; i < min(page*100, 101); i++ {
					id := fmt.Sprintf("task-%d", i)
					if mode == "duplicate_ids" && i == 100 {
						id = "task-0"
					}
					items = append(items, map[string]string{"id": id, "status": "running"})
				}
				total := 101
				if mode == "missing_page" && page == 2 {
					items = []map[string]string{}
				}
				if mode == "changing_total" && page == 2 {
					total = 102
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"total": total, "items": items})
			})
			if _, err := c.Dashboard(context.Background()); err == nil {
				t.Fatal("accepted incomplete slot evidence")
			}
		})
	}
}
