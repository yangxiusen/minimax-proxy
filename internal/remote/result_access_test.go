package remote

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/upstream/tk2sd"
)

func TestResultRefreshPreservesOriginAndActualMetadata(t *testing.T) {
	p, s, db, task, f := processorFixture(t)
	ctx := context.Background()
	f.query.Status = "succeeded"
	if err := p.ProcessTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	original, _ := s.Get(ctx, "owner", task.TaskID)
	db.Exec(`UPDATE video_tasks SET latest_result_expires_at=? WHERE task_id='task'`, time.Now().Add(time.Minute).Unix())
	task, _ = s.Get(ctx, "owner", task.TaskID)
	f.query.VideoURL = strings.ReplaceAll(f.query.VideoURL, "signature=opaque", "signature=renewed")
	a := &ResultAccess{Store: s, Resolve: func(context.Context, string) (ResultClient, *url.URL, error) { return f, p.NodeURL, nil }}
	refreshed, err := a.Refresh(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	persisted, _ := s.Get(ctx, "owner", task.TaskID)
	if refreshed.ResultPublicURL != f.query.VideoURL || persisted.ResultInternalURL != original.ResultInternalURL || persisted.ResultMetadataJSON != original.ResultMetadataJSON || persisted.FinishedAt != original.FinishedAt {
		t.Fatalf("refresh mutated history %+v", persisted)
	}
	if len(f.keys) != 1 || f.uploads != 0 || f.metadataCalls != 1 {
		t.Fatal("refresh regenerated or fetched ready metadata")
	}
}

func TestResultRefreshFailureValidFallbackAndExpiredError(t *testing.T) {
	p, s, db, task, f := processorFixture(t)
	ctx := context.Background()
	f.query.Status = "succeeded"
	if err := p.ProcessTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	a := &ResultAccess{Store: s, Resolve: func(context.Context, string) (ResultClient, *url.URL, error) { return f, p.NodeURL, nil }}
	f.queryErr = errors.New("offline")
	for _, remaining := range []time.Duration{time.Minute, -time.Minute} {
		db.Exec(`UPDATE video_tasks SET latest_result_expires_at=? WHERE task_id='task'`, time.Now().Add(remaining).Unix())
		task, _ = s.Get(ctx, "owner", task.TaskID)
		result, err := a.Refresh(ctx, task)
		if remaining > 0 {
			if err != nil || result.ResultPublicURL == "" {
				t.Fatal(err)
			}
		} else {
			if !errors.Is(err, domain.ErrResultRefreshUnavailable) || result.ResultPublicURL != "" {
				t.Fatal("expired signature returned", err)
			}
		}
	}
	if len(f.keys) != 1 {
		t.Fatal("refresh recreated task")
	}
}

func TestResultRefreshSingleflightAndMetadataRecovery(t *testing.T) {
	p, s, db, task, f := processorFixture(t)
	ctx := context.Background()
	f.query.Status = "succeeded"
	f.metadataErr = errors.New("offline")
	if err := p.ProcessTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	db.Exec(`UPDATE video_tasks SET latest_result_expires_at=? WHERE task_id='task'`, time.Now().Add(-time.Minute).Unix())
	task, _ = s.Get(ctx, "owner", task.TaskID)
	f.metadataErr = nil
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.onQuery = func() { once.Do(func() { close(started) }); <-release }
	a := &ResultAccess{Store: s, Resolve: func(context.Context, string) (ResultClient, *url.URL, error) { return f, p.NodeURL, nil }}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := a.Refresh(ctx, task)
			if err == nil && (!strings.Contains(got.ResultMetadataJSON, "5.062") || got.MetadataStatus != "ready") {
				err = errors.New("actual metadata absent")
			}
			errs <- err
		}()
	}
	<-started
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.queries != 2 || f.metadataCalls != 2 || len(f.keys) != 1 {
		t.Fatal("not singleflight", f.queries, f.metadataCalls)
	}
}

func TestResultDeliveryDownloadUsesOriginalNodeWithoutSignatureRefresh(t *testing.T) {
	p, s, db, task, f := processorFixture(t)
	ctx := context.Background()
	f.query.Status = "succeeded"
	if err := p.ProcessTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	db.Exec(`UPDATE video_tasks SET delivery_required=1,result_public_url='https://oss.example/result.mp4',latest_result_expires_at=1 WHERE task_id='task'`)
	task, _ = s.Get(ctx, "owner", task.TaskID)
	a := &ResultAccess{Store: s, Resolve: func(_ context.Context, nodeID string) (ResultClient, *url.URL, error) {
		if nodeID != "node" {
			t.Fatal("download changed node")
		}
		return f, p.NodeURL, nil
	}}
	got, err := a.Refresh(ctx, task)
	if err != nil || got.ResultPublicURL != "https://oss.example/result.mp4" {
		t.Fatal(err, got.ResultPublicURL)
	}
	for i := 0; i < 2; i++ {
		if _, err := a.Download(ctx, task, "unused-fake-output", 2<<30); err != nil {
			t.Fatal(err)
		}
	}
	if f.queries != 1 || f.downloads != 2 || len(f.keys) != 1 {
		t.Fatal("delivery retry invoked generation or refresh")
	}
}

func TestResultSignaturePathAndOriginGuards(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:8080/prefix")
	now := time.Now()
	suffix := "?expires=9999999999&signature=safe"
	for _, raw := range []string{"http://127.0.0.1:8080/prefix/media/tasks/" + upstreamID + suffix, "http://127.0.0.1:8080/prefix/media/tasks/wrong" + suffix, "http://127.0.0.1:8080/prefix/other/" + upstreamID + suffix, "http://127.0.0.2:8080/prefix/media/tasks/" + upstreamID + suffix, "https://127.0.0.1/media/tasks/" + upstreamID + suffix, "http://user:secret@127.0.0.1:8080/prefix/media/tasks/" + upstreamID + suffix, "http://127.0.0.1:8080/prefix/media/tasks/" + upstreamID + "?expires=1&signature=x"} {
		_, err := ValidateResultURL(context.Background(), base, upstreamID, raw, now, nil)
		if raw == "http://127.0.0.1:8080/prefix/media/tasks/"+upstreamID+suffix {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil {
			t.Fatal("unsafe signature allowed", raw)
		}
	}
}

func TestResultMetadataRejectsInventedOrInvalidValues(t *testing.T) {
	for _, meta := range []tk2sd.Metadata{{Duration: 0, Width: 1280, Height: 720}, {Duration: 5, Width: 0, Height: 720}, {Duration: 5, Width: 1280, Height: -1}} {
		if _, _, err := ActualMetadata(meta); err == nil {
			t.Fatal(meta)
		}
	}
}
