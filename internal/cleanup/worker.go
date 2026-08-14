package cleanup

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/store/sqlite"
	"minimax-h3-tc/internal/upstream/nodeapi"
)

type Store interface {
	ListModelNodes(context.Context) ([]domain.ModelNode, error)
	ClaimDeletionItem(context.Context, string, string, time.Duration) (sqlite.ArtifactDeletionItem, error)
	GetArtifactLocation(context.Context, string) (sqlite.ArtifactLocation, error)
	CompleteDeletionItem(context.Context, string, string, bool, int64) error
	FailDeletionItem(context.Context, string, string, string, string, time.Time, bool) error
}

type SecretOpener interface {
	Open(nonce, ciphertext []byte) (string, error)
}

type DeleteClient interface {
	DeleteArtifacts(context.Context, string, nodeapi.DeleteArtifactsRequest) (nodeapi.DeleteArtifactsResult, error)
}

type Worker struct {
	Store          Store
	Secrets        SecretOpener
	Interval       time.Duration
	LeaseDuration  time.Duration
	RequestTimeout time.Duration
	MaxAttempts    int
	Logger         *slog.Logger
	ClientFactory  func(*url.URL, string, *http.Client, int64) DeleteClient
	Now            func() time.Time
}

func (w Worker) Run(ctx context.Context) {
	if w.Store == nil || w.Secrets == nil {
		return
	}
	interval := w.Interval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		w.runOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w Worker) RunOnce(ctx context.Context) {
	w.runOnce(ctx)
}

func (w Worker) runOnce(ctx context.Context) {
	nodes, err := w.Store.ListModelNodes(ctx)
	if err != nil {
		w.logger().ErrorContext(ctx, "读取删除目标节点失败", "stage", "artifact_delete", "error_code", "node_list_failed")
		return
	}
	for _, node := range nodes {
		if ctx.Err() != nil || !node.Enabled || !node.UsesNodeAPI() {
			continue
		}
		for {
			item, err := w.Store.ClaimDeletionItem(ctx, node.ID, uuid.NewString(), w.leaseDuration())
			if errors.Is(err, sqlite.ErrNoClaimableDeletion) {
				break
			}
			if err != nil {
				w.logger().ErrorContext(ctx, "领取删除明细失败", "node_id", node.ID, "stage", "artifact_delete", "error_code", "delete_claim_failed")
				break
			}
			w.process(ctx, node, item)
		}
	}
}

func (w Worker) process(parent context.Context, node domain.ModelNode, item sqlite.ArtifactDeletionItem) {
	location, err := w.Store.GetArtifactLocation(parent, item.LocationID)
	if err != nil {
		w.fail(parent, item, "artifact_location_missing", "产物位置不存在", true)
		return
	}
	key, err := w.Secrets.Open(node.APIKeyNonce, node.APIKeyCiphertext)
	if err != nil {
		w.fail(parent, item, "node_key_unavailable", "节点密钥不可用", true)
		return
	}
	serviceURL, err := url.Parse(node.ServiceURL)
	if err != nil || serviceURL.Host == "" {
		w.fail(parent, item, "node_config_invalid", "节点服务地址无效", true)
		return
	}
	timeout := w.RequestTimeout
	if timeout <= 0 {
		timeout = node.RequestTimeout
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	clientFactory := w.ClientFactory
	if clientFactory == nil {
		clientFactory = func(base *url.URL, apiKey string, client *http.Client, maxBody int64) DeleteClient {
			return nodeapi.NewClient(base, apiKey, client, maxBody)
		}
	}
	result, err := clientFactory(serviceURL, key, &http.Client{Timeout: timeout}, 1<<20).DeleteArtifacts(
		ctx,
		"delete-"+item.ID,
		nodeapi.DeleteArtifactsRequest{OperationID: item.OperationID, ArtifactIDs: []string{location.NodeArtifactID}},
	)
	if err != nil {
		var apiError *nodeapi.HTTPError
		terminal := errors.As(err, &apiError) && (apiError.StatusCode == http.StatusUnauthorized || apiError.StatusCode == http.StatusForbidden)
		code := "node_delete_unavailable"
		if terminal {
			code = "node_authentication_failed"
		}
		w.fail(parent, item, code, "节点产物删除失败", terminal)
		return
	}
	if len(result.Items) != 1 || result.Items[0].ArtifactID != location.NodeArtifactID {
		w.fail(parent, item, "node_delete_protocol_error", "节点删除响应不匹配", false)
		return
	}
	response := result.Items[0]
	switch response.Status {
	case "deleted":
		err = w.Store.CompleteDeletionItem(parent, item.ID, item.LeaseToken, false, response.DeletedBytes)
	case "already_absent":
		err = w.Store.CompleteDeletionItem(parent, item.ID, item.LeaseToken, true, 0)
	case "artifact_locked":
		w.fail(parent, item, "artifact_locked", "节点产物仍被执行锁定", false)
		return
	default:
		w.fail(parent, item, "node_delete_failed", "节点拒绝删除产物", false)
		return
	}
	if err != nil {
		w.logger().ErrorContext(parent, "回写删除完成状态失败", "item_id", item.ID, "stage", "artifact_delete", "error_code", "delete_complete_failed")
	}
}

func (w Worker) fail(ctx context.Context, item sqlite.ArtifactDeletionItem, code, message string, terminal bool) {
	maxAttempts := w.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 8
	}
	terminal = terminal || item.AttemptCount >= maxAttempts
	delay := time.Duration(math.Min(math.Pow(2, float64(item.AttemptCount)), 300)) * time.Second
	_ = w.Store.FailDeletionItem(ctx, item.ID, item.LeaseToken, code, message, w.now().Add(delay), terminal)
	w.logger().WarnContext(ctx, "模型节点产物删除未完成", "item_id", item.ID, "node_id", item.NodeID, "stage", "artifact_delete", "error_code", code, "terminal", terminal)
}

func (w Worker) leaseDuration() time.Duration {
	if w.LeaseDuration > 0 {
		return w.LeaseDuration
	}
	return time.Minute
}

func (w Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w Worker) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}
