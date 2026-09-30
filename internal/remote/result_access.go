package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/logsafe"
	"minimax-h3-tc/internal/netguard"
	"minimax-h3-tc/internal/upstream/tk2sd"
)

type ResultClient interface {
	Query(context.Context, string) (tk2sd.Task, error)
	Metadata(context.Context, string) (tk2sd.Metadata, error)
	Download(context.Context, string, string, int64) (int64, error)
}
type ResultStore interface {
	GetRemoteRun(context.Context, string) (domain.RemoteRun, error)
	GetTaskForExecution(context.Context, string) (domain.Task, error)
	UpdateRemoteResult(context.Context, string, string, domain.RemoteResultUpdate) error
}
type ClientResolver func(context.Context, string) (ResultClient, *url.URL, error)

type ResultAccess struct {
	Store   ResultStore
	Resolve ClientResolver
	Guard   *netguard.Guard
	Timeout time.Duration
	Now     func() time.Time
	mu      sync.Mutex
	flights map[string]*resultFlight
	slots   chan struct{}
}
type resultFlight struct {
	done chan struct{}
	task domain.Task
	err  error
}

func (a *ResultAccess) Refresh(ctx context.Context, task domain.Task) (domain.Task, error) {
	if task.ProtocolVersion != domain.ProtocolTK2SD || task.Status != domain.StatusSucceeded {
		return task, nil
	}
	if task.DeliveryRequired && task.ResultPublicURL != "" && task.MetadataStatus == "ready" {
		return task, nil
	}
	now := a.now()
	if task.LatestResultURL != "" && task.LatestResultExpiresAt > now.Add(5*time.Minute).Unix() && task.MetadataStatus == "ready" {
		return publicResult(task), nil
	}
	a.mu.Lock()
	if a.flights == nil {
		a.flights = make(map[string]*resultFlight)
		a.slots = make(chan struct{}, 4)
	}
	flightKey := task.APIKeyID + "\x00" + task.TaskID
	if f, ok := a.flights[flightKey]; ok {
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return task, ctx.Err()
		case <-f.done:
			return f.task, f.err
		}
	}
	f := &resultFlight{done: make(chan struct{})}
	a.flights[flightKey] = f
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.flights, flightKey); close(f.done); a.mu.Unlock() }()
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	case <-ctx.Done():
		f.task, f.err = task, ctx.Err()
		return f.task, f.err
	}
	f.task, f.err = a.refresh(ctx, task)
	return f.task, f.err
}

func (a *ResultAccess) refresh(ctx context.Context, task domain.Task) (domain.Task, error) {
	fresh, err := a.Store.GetTaskForExecution(ctx, task.TaskID)
	if err != nil {
		return task, err
	}
	if fresh.DeletedAt != nil || fresh.APIKeyID != task.APIKeyID || fresh.ProtocolVersion != domain.ProtocolTK2SD || fresh.Status != domain.StatusSucceeded {
		return task, domain.ErrTaskNotFound
	}
	task = fresh
	now := a.now()
	needsURL := !task.DeliveryRequired && (task.LatestResultURL == "" || task.LatestResultExpiresAt <= now.Add(5*time.Minute).Unix())
	needsMeta := task.MetadataStatus != "ready"
	if !needsURL && !needsMeta {
		return publicResult(task), nil
	}
	run, err := a.Store.GetRemoteRun(ctx, task.TaskID)
	if err != nil || run.NodeID != task.UpstreamID || run.UpstreamTaskID == "" || run.UpstreamStatus != "succeeded" {
		return fallbackResult(task, now)
	}
	ctx = logsafe.WithTask(ctx, task.TaskID, run.NodeID)
	client, base, err := a.Resolve(ctx, run.NodeID)
	if err != nil {
		return fallbackResult(task, now)
	}
	u := domain.RemoteResultUpdate{MetadataStatus: task.MetadataStatus}
	if u.MetadataStatus == "" || u.MetadataStatus == "none" {
		u.MetadataStatus = "pending"
	}
	if needsURL {
		result, err := client.Query(ctx, run.UpstreamTaskID)
		if err == nil && result.ID == run.UpstreamTaskID && result.Model == task.Model && result.Status == "succeeded" {
			if expires, err := ValidateResultURL(ctx, base, run.UpstreamTaskID, result.VideoURL, now, a.Guard); err == nil {
				u.URL, u.ExpiresAt = result.VideoURL, expires
			}
		}
	}
	if needsMeta {
		if meta, err := client.Metadata(ctx, run.UpstreamTaskID); err == nil {
			if data, _, err := ActualMetadata(meta); err == nil {
				u.MetadataJSON, u.MetadataStatus = data, "ready"
			}
		}
	}
	if u.URL != "" || u.MetadataStatus == "ready" && needsMeta {
		if err = a.Store.UpdateRemoteResult(ctx, task.TaskID, run.NodeID, u); err != nil {
			return task, err
		}
		if u.URL != "" {
			task.LatestResultURL, task.LatestResultExpiresAt = u.URL, u.ExpiresAt
		}
		if u.MetadataStatus == "ready" && needsMeta {
			task.ResultMetadataJSON, task.MetadataStatus = u.MetadataJSON, "ready"
		}
	}
	return fallbackResult(task, a.now())
}

func (a *ResultAccess) Download(ctx context.Context, task domain.Task, destination string, maxBytes int64) (int64, error) {
	fresh, err := a.Store.GetTaskForExecution(ctx, task.TaskID)
	if err != nil {
		return 0, err
	}
	if fresh.DeletedAt != nil || fresh.APIKeyID != task.APIKeyID || fresh.ProtocolVersion != domain.ProtocolTK2SD {
		return 0, domain.ErrTaskNotFound
	}
	run, err := a.Store.GetRemoteRun(ctx, task.TaskID)
	if err != nil {
		return 0, err
	}
	if run.NodeID != fresh.UpstreamID || run.Phase != domain.RemoteTerminal || run.UpstreamStatus != "succeeded" || run.UpstreamTaskID == "" {
		return 0, domain.ErrStateConflict
	}
	client, _, err := a.Resolve(ctx, run.NodeID)
	if err != nil {
		return 0, err
	}
	if maxBytes <= 0 || maxBytes > 2<<30 {
		maxBytes = 2 << 30
	}
	return client.Download(ctx, run.UpstreamTaskID, destination, maxBytes)
}

func (a *ResultAccess) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}
func publicResult(t domain.Task) domain.Task {
	if !t.DeliveryRequired {
		t.ResultPublicURL = t.LatestResultURL
	}
	return t
}
func fallbackResult(t domain.Task, now time.Time) (domain.Task, error) {
	if t.DeliveryRequired && t.ResultPublicURL != "" {
		return t, nil
	}
	if t.LatestResultURL != "" && t.LatestResultExpiresAt > now.Unix() {
		return publicResult(t), nil
	}
	t.ResultPublicURL = ""
	return t, domain.ErrResultRefreshUnavailable
}

func ActualMetadata(meta tk2sd.Metadata) (string, string, error) {
	if meta.Width <= 0 || meta.Height <= 0 || meta.Duration <= 0 || math.IsNaN(meta.Duration) || math.IsInf(meta.Duration, 0) {
		return "", "", errors.New("远程结果元数据无效")
	}
	x, y := meta.Width, meta.Height
	for y != 0 {
		x, y = y, x%y
	}
	ratio := fmt.Sprintf("%d:%d", meta.Width/x, meta.Height/x)
	var resolution *string
	switch size := min(meta.Width, meta.Height); size {
	case 480, 720, 1080, 2160:
		s := fmt.Sprintf("%dp", size)
		resolution = &s
	}
	data, err := json.Marshal(struct {
		Duration   float64 `json:"duration"`
		Width      int     `json:"width"`
		Height     int     `json:"height"`
		Ratio      string  `json:"ratio"`
		Resolution *string `json:"resolution"`
	}{meta.Duration, meta.Width, meta.Height, ratio, resolution})
	return string(data), ratio, err
}

func ValidateResultURL(ctx context.Context, base *url.URL, id, raw string, now time.Time, guard *netguard.Guard) (int64, error) {
	u, err := url.Parse(raw)
	if err != nil || base == nil || base.Hostname() == "" || (base.Scheme != "http" && base.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawPath != "" || strings.Contains(u.Path, "\\") || (u.Scheme != "http" && u.Scheme != "https") {
		return 0, tk2sd.ErrUnsafeURL
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["expires"]) != 1 || len(q["signature"]) != 1 || q.Get("signature") == "" {
		return 0, tk2sd.ErrUnsafeURL
	}
	expires, err := strconv.ParseInt(q.Get("expires"), 10, 64)
	if err != nil || expires <= now.Unix() {
		return 0, tk2sd.ErrUnsafeURL
	}
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	if u.Scheme == base.Scheme && strings.EqualFold(u.Hostname(), base.Hostname()) && port(u) == port(base) {
		if id == "" || strings.ContainsAny(id, "/\\.%") || u.Path != strings.TrimRight(base.Path, "/")+"/media/tasks/"+id {
			return 0, tk2sd.ErrUnsafeURL
		}
		return expires, nil
	}
	if u.Scheme != "https" {
		return 0, tk2sd.ErrUnsafeURL
	}
	if guard == nil {
		guard = netguard.New(netguard.Options{})
	}
	if _, err = guard.Validate(ctx, raw); err != nil {
		return 0, tk2sd.ErrUnsafeURL
	}
	return expires, nil
}
