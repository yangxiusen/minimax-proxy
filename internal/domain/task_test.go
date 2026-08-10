package domain

import "testing"

func TestInternalStatusMapsToV2Status(t *testing.T) {
	tests := map[InternalStatus]V2Status{
		StatusQueuedOpen: V2Queued, StatusQueuedLocked: V2Queued,
		StatusDispatching: V2Running, StatusRunning: V2Running, StatusReconciling: V2Running, StatusCancelling: V2Running,
		StatusSucceeded: V2Succeeded, StatusFailed: V2Failed, StatusCancelled: V2Cancelled,
	}
	for internal, want := range tests {
		if got := internal.V2(); got != want {
			t.Errorf("%s.V2() = %s, want %s", internal, got, want)
		}
	}
}

func TestAdminTaskCapabilitiesFollowLifecycle(t *testing.T) {
	for _, status := range AllInternalStatuses() {
		wantCancel := status == StatusQueuedOpen || status == StatusQueuedLocked || status == StatusDispatching || status == StatusRunning || status == StatusReconciling
		wantDelete := status == StatusSucceeded || status == StatusFailed || status == StatusCancelled
		if got := status.AdminCanCancel(); got != wantCancel {
			t.Errorf("%s.AdminCanCancel() = %v, want %v", status, got, wantCancel)
		}
		if got := status.AdminCanDelete(); got != wantDelete {
			t.Errorf("%s.AdminCanDelete() = %v, want %v", status, got, wantDelete)
		}
	}
}

func TestOnlyQueuedOpenCanBeCancelled(t *testing.T) {
	for _, status := range AllInternalStatuses() {
		if got, want := status.CanCancel(), status == StatusQueuedOpen; got != want {
			t.Errorf("%s.CanCancel() = %v, want %v", status, got, want)
		}
	}
}
