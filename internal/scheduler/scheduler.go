package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"minimax-h3-tc/internal/domain"
)

type Processor interface{ ProcessOne(context.Context) error }

type Slot struct {
	ID        string
	Processor Processor
	Health    func(context.Context) error
	Active    func(context.Context) (bool, error)
}

type Scheduler struct {
	slots    []Slot
	wake     chan struct{}
	interval time.Duration
	logger   *slog.Logger
}

func New(slots []Slot, interval time.Duration, logger *slog.Logger) *Scheduler {
	if interval <= 0 {
		interval = time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{slots: slots, wake: make(chan struct{}, 1), interval: interval, logger: logger}
}

func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	var group sync.WaitGroup
	for _, configured := range s.slots {
		slot := configured
		if slot.Processor == nil {
			continue
		}
		group.Add(1)
		go func() { defer group.Done(); s.runSlot(ctx, slot) }()
	}
	group.Wait()
}

func (s *Scheduler) runSlot(ctx context.Context, slot Slot) {
	for {
		if ctx.Err() != nil {
			return
		}
		hasActive := false
		if slot.Active != nil {
			active, err := slot.Active(ctx)
			if err != nil {
				s.logger.ErrorContext(ctx, "读取实例活动任务失败", "upstream_id", slot.ID, "stage", "active_check", "error_code", "active_check_failed")
				if !s.wait(ctx) {
					return
				}
				continue
			}
			hasActive = active
		}
		if !hasActive && slot.Health != nil {
			healthCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := slot.Health(healthCtx)
			cancel()
			if err != nil {
				if !errors.Is(err, domain.ErrNodeDisabled) {
					s.logger.WarnContext(ctx, "私有服务健康检查失败，暂停该实例调度", "upstream_id", slot.ID, "stage", "health", "error_code", "upstream_unhealthy")
				}
				if !s.wait(ctx) {
					return
				}
				continue
			}
		}
		err := slot.Processor.ProcessOne(ctx)
		if err == nil {
			continue
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if !errors.Is(err, domain.ErrQueueEmpty) && !errors.Is(err, domain.ErrUpstreamBusy) {
			s.logger.ErrorContext(ctx, "上游执行槽处理失败", "upstream_id", slot.ID, "stage", "worker", "error_code", "worker_error")
		}
		if !s.wait(ctx) {
			return
		}
	}
}

func (s *Scheduler) wait(ctx context.Context) bool {
	timer := time.NewTimer(s.interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.wake:
		return true
	case <-timer.C:
		return true
	}
}
