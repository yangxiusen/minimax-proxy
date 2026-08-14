package sqlite

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
)

func TestListAdminTasksFiltersAcrossOwners(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 20, GlobalLimit: 100, Now: func() time.Time { return now }})
	ctx := context.Background()
	for _, input := range []domain.NewTask{
		task("match-a", "customer-a"),
		task("match-b", "customer-b"),
		task("other", "customer-b"),
	} {
		if _, err := store.Create(ctx, input, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET status='succeeded',upstream_id='gpu-1',finished_at=? WHERE task_id IN ('match-a','match-b')`, now.Unix()); err != nil {
		t.Fatal(err)
	}

	items, total, err := store.ListAdminTasks(ctx, domain.AdminTaskFilter{
		Status: domain.V2Succeeded, UpstreamID: "gpu-1", Search: "match-", PageNum: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("items = %+v, total = %d", items, total)
	}
	if items[0].TaskID != "match-b" || items[0].APIKeyID != "customer-b" || items[1].TaskID != "match-a" || items[1].APIKeyID != "customer-a" {
		t.Fatalf("items = %+v", items)
	}
	for _, item := range items {
		if item.Status != domain.V2Succeeded || item.UpstreamID != "gpu-1" || item.Scenario != "t2va" || item.Resolution != "2K" || !item.CreatedAt.Equal(now) {
			t.Errorf("summary = %+v", item)
		}
	}

	empty, total, err := store.ListAdminTasks(ctx, domain.AdminTaskFilter{Status: domain.V2Status("unknown"), PageNum: 1, PageSize: 20})
	if err != nil || total != 0 || len(empty) != 0 {
		t.Fatalf("unknown status items = %+v, total = %d, err = %v", empty, total, err)
	}
}

func TestListAdminTasksIncludesArtifactForPlaybackSigning(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 20, GlobalLimit: 100})
	ctx := context.Background()
	if _, err := store.Create(ctx, task("artifact-task", "customer-a"), "", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateArtifact(ctx, TaskArtifact{
		ID: "artifact-1", TaskID: "artifact-task", Kind: "final_video", SizeBytes: 4,
		SHA256: "digest", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET status='succeeded',result_artifact_id='artifact-1' WHERE task_id='artifact-task'`); err != nil {
		t.Fatal(err)
	}

	items, total, err := store.ListAdminTasks(ctx, domain.AdminTaskFilter{PageNum: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].ResultArtifactID != "artifact-1" {
		t.Fatalf("items = %+v, total = %d", items, total)
	}
}

func TestListAdminTasksTreatsSearchWildcardsLiterally(t *testing.T) {
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 20, GlobalLimit: 100})
	ctx := context.Background()
	for _, input := range []domain.NewTask{
		task("%task", "ordinary"),
		task("_task", "ordinary"),
		task("plain", "%customer"),
		task("unrelated", "ordinary"),
	} {
		if _, err := store.Create(ctx, input, "", nil); err != nil {
			t.Fatal(err)
		}
	}

	percent, total, err := store.ListAdminTasks(ctx, domain.AdminTaskFilter{Search: "%", PageNum: 1, PageSize: 20})
	if err != nil || total != 2 || len(percent) != 2 {
		t.Fatalf("percent items = %+v, total = %d, err = %v", percent, total, err)
	}
	underscore, total, err := store.ListAdminTasks(ctx, domain.AdminTaskFilter{Search: "_", PageNum: 1, PageSize: 20})
	if err != nil || total != 1 || len(underscore) != 1 || underscore[0].TaskID != "_task" {
		t.Fatalf("underscore items = %+v, total = %d, err = %v", underscore, total, err)
	}
}

func TestListAdminTasksPaginatesStablyAndDefaultsPageSize(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 20, GlobalLimit: 100, Now: func() time.Time { return now }})
	ctx := context.Background()
	for i := 1; i <= 12; i++ {
		if _, err := store.Create(ctx, task(fmt.Sprintf("task-%02d", i), "customer"), "", nil); err != nil {
			t.Fatal(err)
		}
	}

	first, total, err := store.ListAdminTasks(ctx, domain.AdminTaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 12 || len(first) != 10 || first[0].TaskID != "task-12" || first[9].TaskID != "task-03" {
		t.Fatalf("first page = %+v, total = %d", first, total)
	}
	second, total, err := store.ListAdminTasks(ctx, domain.AdminTaskFilter{PageNum: 2, PageSize: 10})
	if err != nil || total != 12 || len(second) != 2 || second[0].TaskID != "task-02" || second[1].TaskID != "task-01" {
		t.Fatalf("second page = %+v, total = %d, err = %v", second, total, err)
	}
	maxInt := int(^uint(0) >> 1)
	overflow, total, err := store.ListAdminTasks(ctx, domain.AdminTaskFilter{PageNum: maxInt, PageSize: 10})
	if err != nil || total != 12 || len(overflow) != 0 {
		t.Fatalf("overflow page = %+v, total = %d, err = %v", overflow, total, err)
	}
}

func TestListAdminTasksExcludesDeletedAndExpiredTasks(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 20, GlobalLimit: 100, Now: func() time.Time { return now }, Retention: time.Hour})
	ctx := context.Background()
	for _, id := range []string{"visible", "deleted", "expired"} {
		if _, err := store.Create(ctx, task(id, "customer"), "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET deleted_at=? WHERE task_id='deleted'`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET expires_at=? WHERE task_id='expired'`, now.Unix()); err != nil {
		t.Fatal(err)
	}

	items, total, err := store.ListAdminTasks(ctx, domain.AdminTaskFilter{PageNum: 1, PageSize: 20})
	if err != nil || total != 1 || len(items) != 1 || items[0].TaskID != "visible" {
		t.Fatalf("items = %+v, total = %d, err = %v", items, total, err)
	}
}

func TestLatestFinishedForUpstreamReturnsMostRecentVisibleTask(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	store := newStore(t, Options{ProtectedSlots: 0, PerKeyLimit: 20, GlobalLimit: 100, Now: func() time.Time { return now }})
	ctx := context.Background()
	for _, input := range []domain.NewTask{
		task("older", "customer-a"),
		task("latest", "customer-b"),
		task("other-node", "customer-c"),
		task("deleted-latest", "customer-d"),
	} {
		if _, err := store.Create(ctx, input, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET status='succeeded',upstream_id='gpu-1',started_at=?,finished_at=?,duration=5 WHERE task_id='older'`, now.Add(-5*time.Minute).Unix(), now.Add(-3*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET status='cancelled',upstream_id='gpu-1',started_at=?,finished_at=?,duration=7 WHERE task_id='latest'`, now.Add(-2*time.Minute).Unix(), now.Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET status='failed',upstream_id='gpu-2',finished_at=? WHERE task_id='other-node'`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE video_tasks SET status='failed',upstream_id='gpu-1',finished_at=?,deleted_at=? WHERE task_id='deleted-latest'`, now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}

	got, err := store.LatestFinishedForUpstream(ctx, "gpu-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskID != "latest" || got.APIKeyID != "customer-b" || got.UpstreamID != "gpu-1" || got.Status != domain.V2Cancelled || got.Duration != 7 || !got.StartedAt.Equal(now.Add(-2*time.Minute)) || !got.FinishedAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("latest = %+v", got)
	}
	if _, err := store.LatestFinishedForUpstream(ctx, "missing"); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Fatalf("missing error = %v", err)
	}
}
