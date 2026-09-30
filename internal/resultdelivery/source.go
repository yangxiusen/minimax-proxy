package resultdelivery

import (
	"context"
	"minimax-h3-tc/internal/domain"
)

type TaskSourceStore interface {
	GetTaskForExecution(context.Context, string) (domain.Task, error)
}
type BoundResultDownloader interface {
	Download(context.Context, domain.Task, string, int64) (int64, error)
}
type TaskSource struct {
	Store  TaskSourceStore
	Remote BoundResultDownloader
	Public VideoDownloader
}

func (s TaskSource) DownloadTask(ctx context.Context, id, destination string) (int64, error) {
	task, err := s.Store.GetTaskForExecution(ctx, id)
	if err != nil {
		return 0, err
	}
	if task.ProtocolVersion == domain.ProtocolTK2SD {
		return s.Remote.Download(ctx, task, destination, DefaultMaxVideoBytes)
	}
	return s.Public.Download(ctx, task.ResultInternalURL, destination)
}
