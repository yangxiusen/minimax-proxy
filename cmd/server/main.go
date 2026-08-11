package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"minimax-h3-tc/internal/cleaner"
	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	managerapi "minimax-h3-tc/internal/httpapi/manager"
	"minimax-h3-tc/internal/httpapi/v2"
	monitorcache "minimax-h3-tc/internal/monitor"
	storepkg "minimax-h3-tc/internal/store/sqlite"
	upstreamregistry "minimax-h3-tc/internal/upstream/registry"
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

	importedCount, err := bootstrapLegacyNodes(ctx, store, cfg.LegacyUpstreams)
	if err != nil {
		return err
	}
	if importedCount > 0 {
		logger.Info("已从旧配置导入模型服务节点", "stage", "node_bootstrap", "imported_count", importedCount)
	}
	nodes, err := store.ListModelNodes(ctx)
	if err != nil {
		return err
	}
	cache := monitorcache.NewCache(nil)
	runtimeFactory := upstreamregistry.NodeRuntimeFactory{
		Store: store, Cache: cache, Profiles: cfg.GenerationProfiles, ExecutionTimeout: cfg.Task.ExecutionTimeout,
		MonitorInterval: cfg.Admin.MonitorInterval, Logger: logger,
	}
	nodeRegistry := upstreamregistry.New(store, runtimeFactory.Start, cache, time.Second, logger)
	maxHealthAge := 3 * cfg.Admin.MonitorInterval
	available := cacheAvailability(cache, time.Now, maxHealthAge)
	v2Handler := v2.NewHandler(v2.Dependencies{Store: store, APIKeys: cfg.APIKeys, Profiles: cfg.GenerationProfiles, Logger: logger, Wake: nodeRegistry.Wake, Available: available})
	prober := upstreamregistry.NodeProber{}
	managerHandler := managerapi.NewHandler(managerapi.Dependencies{
		Admin: cfg.Admin, Cache: cache, Store: store, Nodes: store, Logger: logger, Wake: nodeRegistry.Wake,
		ProbeNode: func(ctx context.Context, input domain.ModelNodeInput) managerapi.NodeProbeResult {
			result := prober.Probe(ctx, input)
			return managerapi.NodeProbeResult{
				Gradio: managerapi.NodeCheck{OK: result.GradioOK, ErrorCode: result.GradioErrorCode},
				Jobs:   managerapi.NodeCheck{OK: result.JobsOK, ErrorCode: result.JobsErrorCode},
			}
		},
	})
	handler := newAppHandler(v2Handler, managerHandler)
	server := &http.Server{Addr: cfg.Server.Address, Handler: handler, ReadTimeout: cfg.Server.ReadTimeout, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: cfg.Server.WriteTimeout, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}

	registryDone := make(chan struct{})
	go func() {
		defer close(registryDone)
		nodeRegistry.Run(ctx)
	}()
	go (cleaner.Cleaner{Store: store, Interval: time.Hour, BatchSize: 100, Logger: logger}).Run(ctx)
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("MiniMax H3 V2 中转服务开始监听", "stage", "lifecycle", "address", cfg.Server.Address, "node_count", len(nodes))
		serverErrors <- server.ListenAndServe()
	}()
	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	<-registryDone
	if serveErr != nil {
		return serveErr
	}
	if shutdownErr != nil {
		return shutdownErr
	}
	logger.Info("服务已安全停止", "stage", "lifecycle")
	return nil
}

type legacyNodeStore interface {
	LegacyNodeImportPending(context.Context) (bool, error)
	ListModelNodes(context.Context) ([]domain.ModelNode, error)
	ImportLegacyNodes(context.Context, []domain.ModelNodeInput) (int, bool, error)
}

func bootstrapLegacyNodes(ctx context.Context, store legacyNodeStore, legacy []config.LegacyUpstreamConfig) (int, error) {
	pending, err := store.LegacyNodeImportPending(ctx)
	if err != nil || !pending {
		return 0, err
	}
	nodes, err := store.ListModelNodes(ctx)
	if err != nil {
		return 0, err
	}
	if len(nodes) > 0 {
		_, _, err := store.ImportLegacyNodes(ctx, nil)
		return 0, err
	}
	upstreams, err := config.ParseLegacyUpstreams(legacy)
	if err != nil {
		return 0, err
	}
	inputs := make([]domain.ModelNodeInput, 0, len(upstreams))
	for _, upstream := range upstreams {
		inputs = append(inputs, domain.ModelNodeInput{
			ID: upstream.ID, BaseURL: upstream.BaseURL.String(), JobsBaseURL: upstream.JobsBaseURL.String(), PublicBaseURL: upstream.PublicBaseURL.String(),
			HealthPath: upstream.HealthPath, SubmitAPIName: upstream.SubmitAPIName, CheckAPIName: upstream.CheckAPIName,
			PollInterval: upstream.PollInterval, RequestTimeout: upstream.RequestTimeout, Enabled: true,
		})
	}
	count, _, err := store.ImportLegacyNodes(ctx, inputs)
	return count, err
}

func cacheAvailability(cache *monitorcache.Cache, now func() time.Time, maxAge time.Duration) func() bool {
	return func() bool {
		return cache != nil && cache.AvailableFresh(now(), maxAge)
	}
}

func newAppHandler(v2Handler, managerHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v2/", v2Handler)
	mux.Handle("/manager", managerHandler)
	mux.Handle("/manager/", managerHandler)
	mux.HandleFunc("GET /monitor", redirectLegacyManagerPath("/manager/"))
	mux.HandleFunc("GET /monitor/{$}", redirectLegacyManagerPath("/manager/"))
	mux.HandleFunc("GET /monitor/login", redirectLegacyManagerPath("/manager/login"))
	return mux
}

func redirectLegacyManagerPath(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	}
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
