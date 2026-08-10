package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"minimax-h3-tc/internal/cleaner"
	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	monitorapi "minimax-h3-tc/internal/httpapi/monitor"
	"minimax-h3-tc/internal/httpapi/v2"
	monitorcache "minimax-h3-tc/internal/monitor"
	"minimax-h3-tc/internal/scheduler"
	storepkg "minimax-h3-tc/internal/store/sqlite"
	"minimax-h3-tc/internal/upstream/gradio"
	"minimax-h3-tc/internal/worker"
)

func main() {
	configPath := flag.String("config", configPathDefault(), "YAML 配置文件路径")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(*configPath, logger); err != nil {
		logger.Error("服务启动或运行失败", "stage", "lifecycle", "error_code", "service_failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(configPath string, logger *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	warnDefaultAdminPassword(logger, cfg.Admin)
	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	store, err := storepkg.Open(ctx, cfg.Database.Path, storepkg.Options{ProtectedSlots: cfg.Queue.ProtectedSlots, PerKeyLimit: cfg.Queue.PerKeyUnfinishedLimit, GlobalLimit: cfg.Queue.GlobalUnfinishedLimit, Retention: cfg.Task.Retention, IdempotencyTTL: cfg.Task.IdempotencyTTL})
	if err != nil {
		return err
	}
	defer store.Close()

	cache := newNodeCache(cfg.Upstreams)
	upstreamIDs := make([]string, 0, len(cfg.Upstreams))
	for _, upstream := range cfg.Upstreams {
		upstreamIDs = append(upstreamIDs, upstream.ID)
	}
	if err := restoreNodeSnapshots(ctx, cache, store, upstreamIDs); err != nil {
		return err
	}
	maxHealthAge := maxHealthSnapshotAge(cfg.Admin.MonitorInterval, cfg.Upstreams)
	slots := make([]scheduler.Slot, 0, len(cfg.Upstreams))
	collectorNodes := make([]monitorcache.CollectorNode, 0, len(cfg.Upstreams))
	for _, configured := range cfg.Upstreams {
		upstream := configured
		httpClient := &http.Client{Timeout: upstream.RequestTimeout}
		client := gradio.NewClientWithJobs(upstream.BaseURL, upstream.JobsBaseURL, httpClient, 8<<20)
		gate := &sync.Mutex{}
		processor := &worker.Processor{Store: store, Client: client, Upstream: upstream, Profiles: cfg.GenerationProfiles, Logger: logger, Cache: cache, Gate: gate, ExecutionTimeout: cfg.Task.ExecutionTimeout}
		slots = append(slots, scheduler.Slot{ID: upstream.ID, Processor: processor, Health: func(context.Context) error {
			return cachedNodeSchedulable(cache, upstream.ID, time.Now(), maxHealthAge)
		}, Active: func(ctx context.Context) (bool, error) {
			_, err := store.ActiveForUpstream(ctx, upstream.ID)
			if errors.Is(err, domain.ErrTaskNotFound) {
				return false, nil
			}
			return err == nil, err
		}})
		collectorNodes = append(collectorNodes, monitorcache.CollectorNode{Upstream: upstream, Client: client, Gate: gate})
	}
	dispatcher := scheduler.New(slots, time.Second, logger)
	available := cacheAvailability(cache, time.Now, maxHealthAge)
	v2Handler := v2.NewHandler(v2.Dependencies{Store: store, APIKeys: cfg.APIKeys, Profiles: cfg.GenerationProfiles, Logger: logger, Wake: dispatcher.Wake, Available: available})
	monitorHandler := monitorapi.NewHandler(monitorapi.Dependencies{Admin: cfg.Admin, Cache: cache, Store: store, Logger: logger, Wake: dispatcher.Wake})
	handler := newAppHandler(v2Handler, monitorHandler)
	server := &http.Server{Addr: cfg.Server.Address, Handler: handler, ReadTimeout: cfg.Server.ReadTimeout, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: cfg.Server.WriteTimeout, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	collector := &monitorcache.Collector{Cache: cache, Nodes: collectorNodes, Interval: cfg.Admin.MonitorInterval, Logger: logger}

	go dispatcher.Run(ctx)
	go collector.Run(ctx)
	go (cleaner.Cleaner{Store: store, Interval: time.Hour, BatchSize: 100, Logger: logger}).Run(ctx)
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("MiniMax H3 V2 中转服务开始监听", "stage", "lifecycle", "address", cfg.Server.Address, "upstream_count", len(cfg.Upstreams))
		serverErrors <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			cancel()
			return err
		}
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	logger.Info("服务已安全停止", "stage", "lifecycle")
	return nil
}

type nodeStateStore interface {
	ActiveForUpstream(context.Context, string) (domain.Task, error)
	LatestFinishedForUpstream(context.Context, string) (domain.AdminTaskSummary, error)
}

func newNodeCache(upstreams []config.UpstreamConfig) *monitorcache.Cache {
	nodes := make([]monitorcache.NodeSnapshot, 0, len(upstreams))
	for _, upstream := range upstreams {
		address := ""
		if upstream.BaseURL != nil {
			address = upstream.BaseURL.Host
		}
		nodes = append(nodes, monitorcache.NodeSnapshot{ID: upstream.ID, Address: address})
	}
	return monitorcache.NewCache(nodes)
}

func cacheAvailability(cache *monitorcache.Cache, now func() time.Time, maxAge time.Duration) func() bool {
	return func() bool {
		return cache != nil && cache.AvailableFresh(now(), maxAge)
	}
}

func maxHealthSnapshotAge(interval time.Duration, upstreams []config.UpstreamConfig) time.Duration {
	maxAge := 2 * interval
	for _, upstream := range upstreams {
		candidate := upstream.RequestTimeout + interval
		if candidate > maxAge {
			maxAge = candidate
		}
	}
	return maxAge
}

func cachedNodeHealth(cache *monitorcache.Cache, id string, now time.Time, maxAge time.Duration) error {
	if cache == nil {
		return errors.New("节点状态缓存不可用")
	}
	node, ok := cache.Get(id)
	if !ok || node.Health != monitorcache.HealthHealthy || node.CheckedAt.IsZero() || node.CheckedAt.Before(now.Add(-maxAge)) {
		return errors.New("节点健康状态不可用或已过期")
	}
	return nil
}

func cachedNodeSchedulable(cache *monitorcache.Cache, id string, now time.Time, maxAge time.Duration) error {
	if err := cachedNodeHealth(cache, id, now, maxAge); err != nil {
		return err
	}
	node, _ := cache.Get(id)
	if node.SchedulingBlocked || node.Runtime == monitorcache.RuntimeRunning || node.PrivateQueue != nil && *node.PrivateQueue > 0 {
		return errors.New("私有实例仍有任务运行")
	}
	return nil
}

func restoreNodeSnapshots(ctx context.Context, cache *monitorcache.Cache, store nodeStateStore, upstreamIDs []string) error {
	for _, upstreamID := range upstreamIDs {
		active, err := store.ActiveForUpstream(ctx, upstreamID)
		if err != nil && !errors.Is(err, domain.ErrTaskNotFound) {
			return err
		}
		latest, latestErr := store.LatestFinishedForUpstream(ctx, upstreamID)
		if latestErr != nil && !errors.Is(latestErr, domain.ErrTaskNotFound) {
			return latestErr
		}
		cache.Update(upstreamID, func(node *monitorcache.NodeSnapshot) {
			if err == nil {
				node.Runtime = monitorcache.RuntimeRunning
				node.CurrentTask = &monitorcache.CurrentTaskSnapshot{ID: active.TaskID, Status: string(active.Status.V2()), StartedAt: active.StartedAt}
			}
			if latestErr == nil {
				duration := int64(0)
				if !latest.StartedAt.IsZero() && !latest.FinishedAt.Before(latest.StartedAt) {
					duration = int64(latest.FinishedAt.Sub(latest.StartedAt) / time.Second)
				}
				node.LatestFinishedTask = &monitorcache.FinishedTaskSnapshot{ID: latest.TaskID, APIKeyID: latest.APIKeyID, Status: string(latest.Status), DurationSeconds: duration, FinishedAt: latest.FinishedAt}
			}
		})
	}
	return nil
}

func newAppHandler(v2Handler, monitorHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v2/", v2Handler)
	mux.Handle("/monitor", monitorHandler)
	mux.Handle("/monitor/", monitorHandler)
	return mux
}

func configPathDefault() string {
	if value := os.Getenv("MINIMAX_CONFIG"); value != "" {
		return value
	}
	return "config.yaml"
}

func warnDefaultAdminPassword(logger *slog.Logger, admin config.AdminConfig) {
	if admin.Password != "123" {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("检测到默认管理密码，请在生产环境立即修改", "stage", "configuration", "error_code", "default_admin_password", "username", admin.Username)
}
