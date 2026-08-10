package gradio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

var ErrJobNotFound = errors.New("私有模型任务不存在")

const jobsSnapshotLimit = 256

var canonicalJobID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type JobStatus string

const (
	JobPending    JobStatus = "pending"
	JobInProgress JobStatus = "in_progress"
	JobCompleted  JobStatus = "completed"
	JobFailed     JobStatus = "failed"
	JobCancelled  JobStatus = "cancelled"
)

type Job struct {
	ID         string    `json:"id"`
	Status     JobStatus `json:"status"`
	CreateTime int64     `json:"create_time"`
}

func (c *Client) ListJobs(ctx context.Context) ([]Job, error) {
	var response struct {
		Jobs []Job `json:"jobs"`
	}
	endpoint := c.jobsEndpoint("api", "jobs")
	query := endpoint.Query()
	query.Set("limit", fmt.Sprint(jobsSnapshotLimit))
	endpoint.RawQuery = query.Encode()
	if err := c.jobsRequest(ctx, http.MethodGet, endpoint.String(), nil, &response); err != nil {
		return nil, err
	}
	for _, job := range response.Jobs {
		if err := validateJob(job); err != nil {
			return nil, err
		}
	}
	return response.Jobs, nil
}

func (c *Client) GetJob(ctx context.Context, jobID string) (Job, error) {
	if err := validateJobID(jobID); err != nil {
		return Job{}, err
	}
	var job Job
	err := c.jobsRequest(ctx, http.MethodGet, c.jobsEndpoint("api", "jobs", jobID).String(), nil, &job)
	if err != nil {
		return Job{}, err
	}
	if err := validateJob(job); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (c *Client) CancelJob(ctx context.Context, jobID string) (bool, error) {
	if err := validateJobID(jobID); err != nil {
		return false, err
	}
	var response struct {
		Cancelled bool `json:"cancelled"`
	}
	err := c.jobsRequest(ctx, http.MethodPost, c.jobsEndpoint("api", "jobs", jobID, "cancel").String(), http.NoBody, &response)
	return response.Cancelled, err
}

func (c *Client) jobsRequest(ctx context.Context, method, endpoint string, body io.Reader, target any) error {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("私有 Jobs API: %w", err)
	}
	if response.StatusCode == http.StatusNotFound {
		response.Body.Close()
		return ErrJobNotFound
	}
	if err := decodeLimited(response, c.maxBody, target); err != nil {
		return fmt.Errorf("私有 Jobs API 响应: %w", err)
	}
	return nil
}

func validateJob(job Job) error {
	if err := validateJobID(job.ID); err != nil {
		return err
	}
	switch job.Status {
	case JobPending, JobInProgress, JobCompleted, JobFailed, JobCancelled:
		return nil
	default:
		return fmt.Errorf("私有 Jobs API 返回未知状态 %q", job.Status)
	}
}

func validateJobID(jobID string) error {
	if !canonicalJobID.MatchString(jobID) {
		return errors.New("job_id 必须是规范 UUID")
	}
	return nil
}
