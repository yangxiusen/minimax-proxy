package cleaner

import (
	"context"
	"log/slog"
	"time"
)

type Store interface {
	CleanupExpired(context.Context, int) (int, int, error)
}

type Cleaner struct {
	Store     Store
	Interval  time.Duration
	BatchSize int
	Logger    *slog.Logger
}

func (c Cleaner) Run(ctx context.Context) {
	if c.Interval <= 0 {
		c.Interval = time.Hour
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 100
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tasks, keys, err := c.Store.CleanupExpired(ctx, c.BatchSize)
			if err != nil {
				c.Logger.ErrorContext(ctx, "清理过期任务失败", "stage", "cleanup", "error_code", "cleanup_failed")
				continue
			}
			if tasks > 0 || keys > 0 {
				c.Logger.InfoContext(ctx, "过期任务已建立物理清理作业", "stage", "cleanup", "task_count", tasks, "idempotency_count", keys)
			}
		}
	}
}
