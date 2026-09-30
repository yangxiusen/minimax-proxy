package v2

import (
	"context"
	"encoding/json"
	"minimax-h3-tc/internal/domain"
)

type RemoteResultAccess interface {
	Refresh(context.Context, domain.Task) (domain.Task, error)
}

func (h *handler) mapRemoteTask(ctx context.Context, task domain.Task) (TaskResponse, error) {
	if h.remoteResults != nil && task.Status == domain.StatusSucceeded {
		var err error
		task, err = h.remoteResults.Refresh(ctx, task)
		if err != nil {
			return remoteResponse(task), err
		}
	}
	response := remoteResponse(task)
	if task.Status == domain.StatusSucceeded && task.ResultPublicURL != "" {
		response.Content = &TaskContent{URL: task.ResultPublicURL}
	}
	if task.Status == domain.StatusFailed {
		response.Error = &TaskError{Code: task.ErrorCode, Message: task.ErrorMessage}
	}
	return response, nil
}
func remoteResponse(task domain.Task) TaskResponse {
	r := TaskResponse{ID: task.TaskID, Model: task.Model, Status: task.PublicStatus(), CreatedAt: task.CreatedAt.Unix(), UpdatedAt: task.UpdatedAt.Unix(), TaskType: "generation", Modality: "video"}
	if task.ResultMetadataJSON != "" {
		var metadata struct {
			Duration   *float64 `json:"duration"`
			Resolution *string  `json:"resolution"`
			Ratio      *string  `json:"ratio"`
		}
		if json.Unmarshal([]byte(task.ResultMetadataJSON), &metadata) == nil {
			r.Duration = metadata.Duration
			r.Resolution = metadata.Resolution
			r.Ratio = metadata.Ratio
		}
	}
	return r
}
func (r TaskResponse) MarshalJSON() ([]byte, error) {
	type plain TaskResponse
	if r.DeliveryError != nil {
		return json.Marshal(struct {
			plain
			Content *TaskContent `json:"content"`
		}{plain: plain(r)})
	}
	return json.Marshal(plain(r))
}
