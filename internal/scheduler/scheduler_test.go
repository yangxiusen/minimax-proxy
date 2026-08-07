package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestSchedulerRunsUpstreamSlotsInParallel(t *testing.T) {
	started := make(chan string, 2)
	scheduler := New([]Slot{{ID: "gpu-1", Processor: blockingProcessor{id: "gpu-1", started: started}}, {ID: "gpu-2", Processor: blockingProcessor{id: "gpu-2", started: started}}}, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { scheduler.Run(ctx); close(done) }()
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case id := <-started:
			seen[id] = true
		case <-time.After(time.Second):
			t.Fatal("upstream slots did not start in parallel")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}

func TestWakeIsNonBlocking(t *testing.T) {
	scheduler := New(nil, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for i := 0; i < 100; i++ {
		scheduler.Wake()
	}
}

func TestUnhealthySlotDoesNotBlockHealthySlot(t *testing.T) {
	started := make(chan string, 1)
	unhealthyCalled := make(chan struct{}, 1)
	scheduler := New([]Slot{
		{ID: "bad", Processor: notifyingProcessor{called: unhealthyCalled}, Health: func(context.Context) error { return errors.New("unhealthy") }},
		{ID: "good", Processor: blockingProcessor{id: "good", started: started}},
	}, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { scheduler.Run(ctx); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("healthy slot did not start")
	}
	select {
	case <-unhealthyCalled:
		t.Fatal("unhealthy processor was called")
	default:
	}
	cancel()
	<-done
}

type blockingProcessor struct {
	id      string
	started chan<- string
}

type notifyingProcessor struct{ called chan<- struct{} }

func (p notifyingProcessor) ProcessOne(context.Context) error { p.called <- struct{}{}; return nil }

func (p blockingProcessor) ProcessOne(ctx context.Context) error {
	p.started <- p.id
	<-ctx.Done()
	return ctx.Err()
}
