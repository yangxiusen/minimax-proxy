package tk2sd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const dashboardBody = `{"counts":{"running":2,"queued":3,"succeeded":4,"uncertain":1,"failed":5,"canceled":6},"occupied":1,"capacity":2,"accounts":[{"id":"private-account","name":"1-1","lease":"private-task","credits":80,"not_before":0,"error":"private error"},{"id":"other-private-account","name":"1-2","lease":null,"credits":null,"not_before":0}],"login":{"status":"valid","can_dispatch":true,"message":"private login details"},"session_file_present":true}`

func TestDashboardReadsAuthenticatedSnapshotAndExposesOnlySafeFields(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/dashboard" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("unexpected dashboard request: %s %s", r.Method, r.URL)
		}
		jsonReply(w, dashboardBody)
	})
	got, err := c.Dashboard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Counts.Running != 2 || got.Counts.Queued != 3 || got.Counts.Succeeded != 4 || got.Counts.Uncertain != 1 || got.Counts.Failed != 5 || got.Counts.Canceled != 6 || got.Occupied != 1 || got.Capacity != 2 || got.Login.Status != "valid" || !got.Login.CanDispatch {
		t.Fatalf("wrong dashboard: %+v", got)
	}
	if len(got.Accounts) != 2 || got.Accounts[0].Label != "1-1" || got.Accounts[0].Status != "occupied" || got.Accounts[0].Credits == nil || *got.Accounts[0].Credits != 80 || got.Accounts[1].Status != "idle" || got.Accounts[1].Credits != nil {
		t.Fatalf("wrong accounts: %+v", got.Accounts)
	}
	if len(got.LeasedTaskIDs) != 1 || got.LeasedTaskIDs[0] != "private-task" {
		t.Fatalf("internal leases: %+v", got.LeasedTaskIDs)
	}
	encoded, _ := json.Marshal(got)
	for _, secret := range []string{"private-account", "private-task", "private error", "private login details", "test-secret", "session_file_present"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("dashboard leaked %q: %s", secret, encoded)
		}
	}
}

func TestDashboardRejectsMalformedOrUnavailableResponses(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"counts":{}}`, strings.Replace(dashboardBody, `"queued":3`, `"queued":-1`, 1),
		strings.Replace(dashboardBody, `"capacity":2`, `"capacity":0`, 1),
		strings.Replace(dashboardBody, `"can_dispatch":true`, `"can_dispatch":"true"`, 1),
		strings.Replace(dashboardBody, `"accounts":[`, `"accounts":null,"unused":[`, 1),
	} {
		t.Run(body[:min(len(body), 40)], func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, body) })
			if _, err := c.Dashboard(context.Background()); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("malformed dashboard accepted: %v", err)
			}
		})
	}
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		jsonReply(w, `{"detail":"private token"}`)
	})
	if _, err := c.Dashboard(context.Background()); err == nil {
		t.Fatal("unauthorized dashboard accepted")
	}
	c, _ = testClient(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, dashboardBody) })
	c.maxBody = 128
	if _, err := c.Dashboard(context.Background()); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("oversized dashboard accepted: %v", err)
	}
}
