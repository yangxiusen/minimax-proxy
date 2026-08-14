package nodeapi

import (
	"encoding/json"
	"io"
)

const ProtocolVersion = "h3-node-v1"

type Health struct {
	Status          string            `json:"status"`
	ProtocolVersion string            `json:"protocol_version"`
	NodeTime        int64             `json:"node_time"`
	Components      map[string]string `json:"components"`
	ComfyUIExposure string            `json:"comfyui_exposure"`
	Runtime         *HealthRuntime    `json:"runtime,omitempty"`
}

type HealthRuntime struct {
	QueueRunning     *int                  `json:"queue_running"`
	QueuePending     *int                  `json:"queue_pending"`
	MemoryTotalBytes *int64                `json:"memory_total_bytes"`
	MemoryFreeBytes  *int64                `json:"memory_free_bytes"`
	VRAMTotalBytes   *int64                `json:"vram_total_bytes"`
	VRAMFreeBytes    *int64                `json:"vram_free_bytes"`
	CPUPercent       *float64              `json:"cpu_percent"`
	GPUPercent       *float64              `json:"gpu_percent"`
	Devices          []HealthRuntimeDevice `json:"devices"`
	ExecutionSlots   *ExecutionSlots       `json:"execution_slots"`
}

type HealthRuntimeDevice struct {
	Index          int      `json:"index"`
	Name           string   `json:"name"`
	GPUPercent     *float64 `json:"gpu_percent"`
	VRAMTotalBytes *int64   `json:"vram_total_bytes"`
	VRAMFreeBytes  *int64   `json:"vram_free_bytes"`
}

type ExecutionSlots struct {
	Used     int `json:"used"`
	Capacity int `json:"capacity"`
}

type Capabilities struct {
	ProtocolVersion      string         `json:"protocol_version"`
	CapabilitiesRevision string         `json:"capabilities_revision"`
	Stages               []string       `json:"stages"`
	Scenarios            []string       `json:"scenarios"`
	Ratios               []string       `json:"ratios"`
	Raw                  map[string]any `json:"-"`
}

type InputArtifact struct {
	ArtifactID string `json:"artifact_id"`
	Role       string `json:"role,omitempty"`
}

type ExpectedMedia struct {
	FPSMultiplier    *int `json:"fps_multiplier,omitempty"`
	PreserveTimeline bool `json:"preserve_timeline"`
	PreserveAudio    bool `json:"preserve_audio"`
}

type ExecutionRequest struct {
	OperationID    string          `json:"operation_id"`
	ExternalTaskID string          `json:"external_task_id"`
	StageID        string          `json:"stage_id"`
	StageType      string          `json:"stage_type"`
	InputArtifacts []InputArtifact `json:"input_artifacts"`
	Parameters     json.RawMessage `json:"parameters"`
	ExpectedMedia  ExpectedMedia   `json:"expected_media"`
}

type ExecutionReference struct {
	ExecutionID string `json:"execution_id"`
	Status      string `json:"status"`
	OperationID string `json:"operation_id"`
}

type Execution struct {
	ExecutionID       string     `json:"execution_id"`
	OperationID       string     `json:"operation_id"`
	ExternalTaskID    string     `json:"external_task_id"`
	StageID           string     `json:"stage_id"`
	StageType         string     `json:"stage_type"`
	Status            string     `json:"status"`
	ComfyJobID        string     `json:"comfy_job_id"`
	ProgressJSON      string     `json:"progress_json"`
	ResultArtifactID  string     `json:"result_artifact_id"`
	ErrorCode         string     `json:"error_code"`
	ErrorMessage      string     `json:"error_message"`
	CancelRequestedAt int64      `json:"cancel_requested_at"`
	CreatedAt         int64      `json:"created_at"`
	UpdatedAt         int64      `json:"updated_at"`
	StartedAt         int64      `json:"started_at"`
	HeartbeatAt       int64      `json:"heartbeat_at"`
	FinishedAt        int64      `json:"finished_at"`
	Error             *NodeError `json:"error"`
}

type NodeError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details"`
}

type Artifact struct {
	ArtifactID    string          `json:"artifact_id"`
	Kind          string          `json:"kind"`
	SizeBytes     int64           `json:"size_bytes"`
	SHA256        string          `json:"sha256"`
	MediaManifest json.RawMessage `json:"media_manifest"`
	State         string          `json:"state"`
	Locked        bool            `json:"locked"`
	CreatedAt     int64           `json:"created_at"`
	ExpiresAt     int64           `json:"expires_at"`
}

type ArtifactContent struct {
	Body          io.ReadCloser
	StatusCode    int
	ContentLength int64
	ContentRange  string
	ContentType   string
	ETag          string
}

type ImportArtifactRequest struct {
	OperationID      string
	SourceArtifactID string
	ExpectedSize     int64
	ExpectedSHA256   string
	Kind             string
	Filename         string
	Content          io.Reader
}

type DeleteArtifactsRequest struct {
	OperationID string   `json:"operation_id"`
	ArtifactIDs []string `json:"artifact_ids"`
}

type DeleteArtifactItem struct {
	ArtifactID   string `json:"artifact_id"`
	Status       string `json:"status"`
	DeletedBytes int64  `json:"deleted_bytes"`
}

type DeleteArtifactsResult struct {
	OperationID string               `json:"operation_id"`
	Items       []DeleteArtifactItem `json:"items"`
}

type errorEnvelope struct {
	Error NodeError `json:"error"`
}
