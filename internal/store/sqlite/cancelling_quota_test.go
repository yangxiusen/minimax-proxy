package sqlite

import (
	"context"
	"errors"
	"minimax-h3-tc/internal/domain"
	"testing"
)

func TestCancellationPendingStillCountsAgainstAdmissionQuota(t *testing.T) {
	for _, test := range []struct {
		name           string
		perKey, global int
		owner          string
		want           error
	}{{"owner", 1, 10, "owner", domain.ErrPerKeyLimit}, {"global", 10, 1, "other", domain.ErrGlobalLimit}} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := newStore(t, Options{PerKeyLimit: test.perKey, GlobalLimit: test.global})
			if _, err := store.Create(ctx, task("pending-cancel", "owner"), "", nil); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET status='cancelling',upstream_slot_active=1 WHERE task_id='pending-cancel'`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Create(ctx, task("new", test.owner), "", nil); !errors.Is(err, test.want) {
				t.Fatalf("quota error=%v want=%v", err, test.want)
			}
		})
	}
}
