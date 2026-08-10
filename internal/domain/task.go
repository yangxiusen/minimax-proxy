package domain

import (
	"errors"
	"time"
)

var (
	ErrTaskNotFound        = errors.New("任务不存在")
	ErrTaskNotOperable     = errors.New("任务当前状态不可操作")
	ErrIdempotencyConflict = errors.New("幂等键对应不同请求")
	ErrPerKeyLimit         = errors.New("API Key 未结束任务达到上限")
	ErrGlobalLimit         = errors.New("全局未结束任务达到上限")
	ErrUpstreamBusy        = errors.New("上游实例正在执行任务")
	ErrQueueEmpty          = errors.New("队列为空")
	ErrStateConflict       = errors.New("任务状态已变化")
	ErrResourceUnavailable = errors.New("资源不足")
)

type InternalStatus string

const (
	StatusQueuedOpen   InternalStatus = "queued_open"
	StatusQueuedLocked InternalStatus = "queued_locked"
	StatusDispatching  InternalStatus = "dispatching"
	StatusRunning      InternalStatus = "running"
	StatusReconciling  InternalStatus = "reconciling"
	StatusCancelling   InternalStatus = "cancelling"
	StatusSucceeded    InternalStatus = "succeeded"
	StatusFailed       InternalStatus = "failed"
	StatusCancelled    InternalStatus = "cancelled"
)

type V2Status string

const (
	V2Queued    V2Status = "queued"
	V2Running   V2Status = "running"
	V2Succeeded V2Status = "succeeded"
	V2Failed    V2Status = "failed"
	V2Cancelled V2Status = "cancelled"
)

func (s InternalStatus) V2() V2Status {
	switch s {
	case StatusQueuedOpen, StatusQueuedLocked:
		return V2Queued
	case StatusDispatching, StatusRunning, StatusReconciling, StatusCancelling:
		return V2Running
	case StatusSucceeded:
		return V2Succeeded
	case StatusFailed:
		return V2Failed
	default:
		return V2Cancelled
	}
}

func (s InternalStatus) CanCancel() bool { return s == StatusQueuedOpen }

func (s InternalStatus) AdminCanCancel() bool {
	switch s {
	case StatusQueuedOpen, StatusQueuedLocked, StatusDispatching, StatusRunning, StatusReconciling:
		return true
	default:
		return false
	}
}

func (s InternalStatus) AdminCanDelete() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCancelled
}

func AllInternalStatuses() []InternalStatus {
	return []InternalStatus{StatusQueuedOpen, StatusQueuedLocked, StatusDispatching, StatusRunning, StatusReconciling, StatusCancelling, StatusSucceeded, StatusFailed, StatusCancelled}
}

type Action string

const (
	ActionCancelled Action = "cancelled"
	ActionDeleted   Action = "deleted"
)

type NewTask struct {
	TaskID, APIKeyID, Model, Scenario string
	RequestJSON, RequestHash          string
	Resolution, Ratio                 string
	Duration, InputImageCount         int
}

type Task struct {
	QueueSeq                                 int64
	TaskID, APIKeyID, Model, Scenario        string
	RequestJSON, RequestHash                 string
	Status                                   InternalStatus
	CancelLocked                             bool
	UpstreamID, GradioEventID, UpstreamJobID string
	UpstreamJobsBeforeJSON                   string
	RetryCount                               int
	AttemptStartedAt, CancelRequestedAt      time.Time
	GalleryBeforeJSON                        string
	ResultInternalURL, ResultPublicURL       string
	Resolution, RatioRequested, RatioActual  string
	Duration                                 int
	UsageTotalSeconds, UsageInputSeconds     int
	UsageOutputSeconds, UsageInputImageCount int
	ErrorCode, ErrorMessage                  string
	CreatedAt, UpdatedAt                     time.Time
	StartedAt, FinishedAt, ExpiresAt         time.Time
	DeletedAt                                *time.Time
	Version                                  int64
}

type TaskFilter struct {
	Status   V2Status
	TaskIDs  []string
	PageNum  int
	PageSize int
}

type AdminTaskFilter struct {
	Status     V2Status
	UpstreamID string
	Search     string
	PageNum    int
	PageSize   int
}

type AdminTaskSummary struct {
	TaskID, APIKeyID, UpstreamID     string
	Scenario, Resolution             string
	Status                           V2Status
	InternalStatus                   InternalStatus
	RetryCount                       int
	ResultPublicURL                  string
	Duration                         int
	CreatedAt, StartedAt, FinishedAt time.Time
}
