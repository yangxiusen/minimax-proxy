package domain

import "errors"

const (
	RemotePreparing    = "preparing"
	RemotePrepared     = "prepared"
	RemoteSubmitIntent = "submit_intent"
	RemoteSubmitted    = "submitted"
	RemoteTerminal     = "terminal"
)

var (
	ErrRemotePending            = errors.New("远程任务等待下次对账")
	ErrRemoteReconciliation     = errors.New("远程提交证据需要人工核实")
	ErrRemoteNotCancellable     = errors.New("远程任务已开始执行，不支持取消")
	ErrResultRefreshUnavailable = errors.New("result_refresh_unavailable")
)

type RemoteRun struct {
	TaskID, NodeID, Phase, SubmissionKey        string
	RequestBodyJSON, RequestBodyHash            string
	UpstreamTaskID, UpstreamStatus, CancelState string
	ReconciliationRequired                      bool
	SubmitAttempts                              int
	LeaseToken                                  string
	LeaseExpiresAt, NextPollAt                  int64
	LastErrorCode                               string
	CreatedAt, UpdatedAt                        int64
}

// TK2SDAdmission is a recent account snapshot used only to bound new claims.
type TK2SDAdmission struct {
	Capacity, Occupied, Cooling, Queued int
	CanDispatch                         bool
	LeasedTaskIDs                       []string
}

type NodeAsset struct {
	TaskID, NodeID                           string
	ContentIndex                             int
	AssetID, ContentType, Role, SourceSHA256 string
	SizeBytes                                int64
	MetadataJSON                             string
	CreatedAt, UpdatedAt                     int64
}

type RemoteCompletion struct {
	Status, ResultURL              string
	ResultExpiresAt                int64
	MetadataJSON, MetadataStatus   string
	Ratio, ErrorCode, ErrorMessage string
	Feedback                       *UpstreamFeedback
	UploadJob                      *ResultUploadJob
}

type RemoteResultUpdate struct {
	URL                          string
	ExpiresAt                    int64
	MetadataJSON, MetadataStatus string
}
