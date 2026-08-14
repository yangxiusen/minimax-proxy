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

	artifactservice "minimax-h3-tc/internal/artifact"
	"minimax-h3-tc/internal/authkey"
	callbackservice "minimax-h3-tc/internal/callback"
	"minimax-h3-tc/internal/cleaner"
	cleanupworker "minimax-h3-tc/internal/cleanup"
	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	managerapi "minimax-h3-tc/internal/httpapi/manager"
	"minimax-h3-tc/internal/httpapi/v2"
	monitorcache "minimax-h3-tc/internal/monitor"
	"minimax-h3-tc/internal/orchestrator"
	profileservice "minimax-h3-tc/internal/profile"
	"minimax-h3-tc/internal/secretbox"
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
	nodeSecrets, err := secretbox.NewFromEnvironment()
	if err != nil {
		return err
	}
	artifactSigningKey, err := nodeSecrets.DeriveArtifactSigningKey()
	if err != nil {
		return err
	}
	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	store, err := storepkg.Open(ctx, cfg.Database.Path, storepkg.Options{ProtectedSlots: cfg.Queue.ProtectedSlots, PerKeyLimit: cfg.Queue.PerKeyUnfinishedLimit, GlobalLimit: cfg.Queue.GlobalUnfinishedLimit, Retention: cfg.Task.Retention, IdempotencyTTL: cfg.Task.IdempotencyTTL})
	if err != nil {
		return err
	}
	defer store.Close()
	pendingKeyImport, err := store.APIKeyBootstrapPending(ctx)
	if err != nil {
		return err
	}
	var legacyKeyInputs []domain.ExternalAPIKeyInput
	if pendingKeyImport {
		legacyKeys, err := config.ParseLegacyAPIKeys(cfg.APIKeys)
		if err != nil {
			return err
		}
		legacyKeyInputs, err = legacyAPIKeyInputs(legacyKeys)
		if err != nil {
			return err
		}
	}
	importedKeys, imported, err := store.ImportLegacyAPIKeys(ctx, legacyKeyInputs)
	if err != nil {
		return err
	}
	if imported {
		logger.Info("已从旧配置导入对外 API Key", "stage", "api_key_bootstrap", "imported_count", importedKeys)
	}
	if !pendingKeyImport {
		if legacyKeys, parseErr := config.ParseLegacyAPIKeys(cfg.APIKeys); parseErr == nil {
			if inputs, inputErr := legacyAPIKeyInputs(legacyKeys); inputErr == nil {
				if err := store.BackfillExternalAPIKeyPlaintexts(ctx, inputs); err != nil {
					return err
				}
			}
		}
	}
	cfg.APIKeys = nil
	keyAuthenticator := authkey.NewAuthenticator(store)
	if err := keyAuthenticator.Reload(ctx); err != nil {
		return err
	}
	apiKeyService := authkey.NewService(store, keyAuthenticator, authkey.ServiceOptions{})

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
	artifactService, err := artifactservice.NewService(store, store, nodeSecrets, artifactservice.Options{
		SigningKey: artifactSigningKey,
		URLPrefix:  cfg.Server.PublicBaseURL.String() + "/v2/files",
	})
	if err != nil {
		return err
	}
	runtimeFactory := upstreamregistry.NodeRuntimeFactory{
		Store: store, Cache: cache, Profiles: cfg.GenerationProfiles, ExecutionTimeout: cfg.Task.ExecutionTimeout,
		MonitorInterval: cfg.Admin.MonitorInterval, Logger: logger, NodeSecrets: nodeSecrets,
		ArtifactMigrator: orchestrator.MigrationService{Artifacts: artifactService},
	}
	nodeRegistry := upstreamregistry.New(store, runtimeFactory.Start, cache, time.Second, logger)
	maxHealthAge := 3 * cfg.Admin.MonitorInterval
	available := cacheAvailability(cache, time.Now, maxHealthAge)
	callbackService := callbackservice.NewService(nil, callbackservice.Options{})
	callbackStore := callbackservice.PersistentStore{Repository: store, Secrets: nodeSecrets}
	prober := upstreamregistry.NodeProber{}
	profileService := profileservice.New(store, profileservice.CapabilityMatcher{Source: profileservice.RuntimeCapabilitySource{Nodes: store, Cache: cache}}, nil)
	managerHandler := managerapi.NewHandler(managerapi.Dependencies{
		Admin: cfg.Admin, Cache: cache, Store: store, Nodes: store, Logger: logger, Wake: nodeRegistry.Wake, NodeSecrets: nodeSecrets,
		ProfileService: profileService,
		Cleanups:       store,
		APIKeyService:  apiKeyService,
		ArtifactURLs:   artifactService,
		ProbeNode: func(ctx context.Context, input managerapi.NodeProbeInput) managerapi.NodeProbeResult {
			result := prober.ProbeNodeAPI(ctx, input.Node, input.APIKey)
			checks := []managerapi.NodeCheck{
				{Name: "health", Status: probeCheckStatus(result.HealthErrorCode), ErrorCode: result.HealthErrorCode},
				{Name: "capabilities", Status: probeCheckStatus(result.CapabilityErrorCode), ErrorCode: result.CapabilityErrorCode},
			}
			return managerapi.NodeProbeResult{
				Reachable: result.Reachable, Authenticated: result.Authenticated, ProtocolVersion: result.ProtocolVersion,
				Checks: checks, Capabilities: result.Capabilities,
			}
		},
	})
	v2Handler := v2.NewHandler(v2.Dependencies{Store: store, Authenticator: keyAuthenticator, Profiles: cfg.GenerationProfiles, Logger: logger, Wake: nodeRegistry.Wake, Available: available, CallbackService: callbackService, CallbackCipher: nodeSecrets, ActiveProfiles: store, ArtifactURLs: artifactService})
	filesHandler := v2.NewFilesHandler(v2.FilesDependencies{Service: artifactService, Authenticator: keyAuthenticator, Logger: logger})
	handler := newAppHandler(v2Handler, filesHandler, managerHandler)
	server := &http.Server{Addr: cfg.Server.Address, Handler: handler, ReadTimeout: cfg.Server.ReadTimeout, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: cfg.Server.WriteTimeout, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}

	registryDone := make(chan struct{})
	go func() {
		defer close(registryDone)
		nodeRegistry.Run(ctx)
	}()
	go (cleaner.Cleaner{Store: store, Interval: time.Hour, BatchSize: 100, Logger: logger}).Run(ctx)
	go (cleanupworker.Worker{Store: store, Secrets: nodeSecrets, Logger: logger}).Run(ctx)
	go apiKeyService.Run(ctx, time.Second)
	go func() {
		worker := callbackservice.Worker{Store: callbackStore, Service: callbackService, Logger: logger}
		if err := worker.Run(ctx, time.Second); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("callback worker 已停止", "stage", "callback", "error_code", "callback_worker_stopped")
		}
	}()
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

func legacyAPIKeyInputs(keys []config.APIKeyConfig) ([]domain.ExternalAPIKeyInput, error) {
	inputs := make([]domain.ExternalAPIKeyInput, 0, len(keys))
	for _, key := range keys {
		if key.ID == "" || key.Key == "" {
			return nil, errors.New("旧 API Key 的 id 和 key 不能为空")
		}
		prefixLength := 4
		if len(key.Key) >= 8 && len(key.Key) >= 4 && key.Key[:4] == "mmx_" {
			prefixLength = 8
		}
		if len(key.Key) < prefixLength || len(key.Key) < 4 {
			return nil, errors.New("旧 API Key 长度无效")
		}
		inputs = append(inputs, domain.ExternalAPIKeyInput{ID: key.ID, Name: key.ID, Key: key.Key, KeyDigest: authkey.Digest(key.Key), KeyPrefix: key.Key[:prefixLength], KeySuffix: key.Key[len(key.Key)-4:], Enabled: key.Enabled})
	}
	return inputs, nil
}

func probeCheckStatus(errorCode string) string {
	if errorCode == "" {
		return "passed"
	}
	return "failed"
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

func newAppHandler(v2Handler, filesHandler, managerHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	if filesHandler != nil {
		mux.Handle("/v2/files/", filesHandler)
	}
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
