// Package daemon composes the local Meridian control plane.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/artifacts"
	"github.com/OrlojHQ/meridian/internal/datadir"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/gatewaytls"
	"github.com/OrlojHQ/meridian/internal/httpapi"
	"github.com/OrlojHQ/meridian/internal/observability"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	agentsandboxprovider "github.com/OrlojHQ/meridian/internal/provider/agentsandbox"
	dockerprovider "github.com/OrlojHQ/meridian/internal/provider/docker"
	"github.com/OrlojHQ/meridian/internal/providergateway"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/system"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

type Config struct {
	ProviderGatewayURL string
	Provider           string
	Listen             string
	PreviewListen      string
	DataDir            string
	// DataLease tunes the data directory lease; zero uses the defaults.
	DataLease         datadir.Options
	APITokenFile      string
	AllowNonLoopback  bool
	TranscriptKeyFile string
	SecretKeyFile     string
	IdlePause         time.Duration
	IdleScanInterval  time.Duration
	RuntimeRefresh    time.Duration
	MaxLiveCapsules   int
	OfficialPackTag   string
	Logger            *log.Logger
	Docker            dockerprovider.Config
	DockerCPUs        float64
	AgentSandbox      agentsandboxprovider.Config
	Observability     observability.Config

	// ProviderGatewayAllowPrivateHTTP lets custom provider connections opt in
	// to plaintext HTTP upstreams on private-network addresses.
	ProviderGatewayAllowPrivateHTTP bool
	// ProviderGatewayListen is the local provider gateway's listen address
	// (ADR 0037): empty chooses one from the Docker engine, "off" disables
	// it. It applies only to Docker without ProviderGatewayURL.
	ProviderGatewayListen string
}

// testProvider replaces provider construction when tests call UseTestProvider.
// Production leaves it nil and builds Docker or Agent Sandbox from Config.
var testProvider func(context.Context, Config) (ports.CapsuleProvider, func() error, error)

// Run serves the local control plane until ctx ends. Stopping it, during
// startup or after, is a clean shutdown and returns nil.
func Run(ctx context.Context, config Config) error {
	err := run(ctx, config)
	// A stop during startup ends whichever step was running with the
	// context's error; that is the requested shutdown, not a failure.
	if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return nil
	}
	return err
}

func run(ctx context.Context, config Config) error {
	if config.Provider != "docker" && config.Provider != "agentsandbox" {
		return fmt.Errorf("unsupported provider %q (supported: docker, agentsandbox)", config.Provider)
	}
	if config.Listen == "" {
		return fmt.Errorf("listen address is required")
	}
	if err := ValidateProviderGatewayListen(config); err != nil {
		return err
	}
	// A fresh install has no data directory yet. Create it private, as the
	// store did before the lease ran first; an existing directory keeps its
	// mode, and the lease still refuses a symlink or a non-directory.
	if err := os.MkdirAll(config.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	// Take the data directory before anything writes to it. A second daemon
	// on the same directory, even across a VM boundary, corrupts SQLite.
	lease, err := datadir.Acquire(ctx, config.DataDir, "meridiand", config.DataLease)
	if err != nil {
		return fmt.Errorf("acquire data directory: %w", err)
	}
	defer lease.Release()
	apiTokenPath := config.APITokenFile
	if apiTokenPath == "" {
		apiTokenPath = apiauth.DefaultTokenPath(config.DataDir)
	}
	apiToken, err := apiauth.OpenOrCreateToken(apiTokenPath)
	if err != nil {
		return fmt.Errorf("initialize API authentication: %w", err)
	}
	if err := ValidateAPIListenAddress(config.Listen, config.AllowNonLoopback, apiToken != nil); err != nil {
		return err
	}
	if config.PreviewListen == "" {
		config.PreviewListen = "127.0.0.1:8081"
	}
	if err := ValidatePreviewListenAddress(config.PreviewListen); err != nil {
		return err
	}
	if config.Logger == nil {
		config.Logger = log.Default()
	}
	if err := observability.ValidateMetricsListen(
		config.Observability.MetricsListen,
		config.Observability.AllowUnsafeMetricsBind,
	); err != nil {
		return err
	}
	shutdownTracing, err := observability.SetupTracing(ctx, config.Observability)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			config.Logger.Printf("trace shutdown failed: %v", err)
		}
	}()
	metrics := observability.NewMetrics()

	listener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	previewListener, err := net.Listen("tcp", config.PreviewListen)
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("listen for previews: %w", err)
	}
	var metricsListener net.Listener
	if config.Observability.MetricsListen != "" {
		metricsListener, err = net.Listen("tcp", config.Observability.MetricsListen)
		if err != nil {
			_ = listener.Close()
			_ = previewListener.Close()
			return fmt.Errorf("listen for metrics: %w", err)
		}
	}
	handler := newSwitchHandler()
	previewHandler := newSwitchHandler()
	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// SSE responses legitimately outlive ordinary request deadlines, and
		// WebSockets take over the connection after upgrade. Keep WriteTimeout
		// disabled globally while handlers retain explicit body/frame limits.
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}
	previewHTTPServer := &http.Server{
		Handler:           previewHandler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       0,
		WriteTimeout:      0,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	metricsHTTPServer := &http.Server{
		Handler:           metrics.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	serverErrors := make(chan error, 1)
	go func() {
		err := httpServer.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
		close(serverErrors)
	}()
	previewServerErrors := make(chan error, 1)
	go func() {
		err := previewHTTPServer.Serve(previewListener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			previewServerErrors <- err
		}
		close(previewServerErrors)
	}()
	metricsServerErrors := make(chan error, 1)
	if metricsListener != nil {
		go func() {
			err := metricsHTTPServer.Serve(metricsListener)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				metricsServerErrors <- err
			}
			close(metricsServerErrors)
		}()
	}
	defer httpServer.Close()
	defer previewHTTPServer.Close()
	defer metricsHTTPServer.Close()

	store, err := sqlite.Open(ctx, config.DataDir)
	if err != nil {
		_ = httpServer.Close()
		_ = previewHTTPServer.Close()
		return err
	}
	defer store.Close()
	secretKeyPath := config.SecretKeyFile
	if secretKeyPath == "" {
		secretKeyPath = secrets.DefaultKeyPath(config.DataDir)
	}
	allowSecretKeyCreate := true
	if err := store.View(ctx, func(reader ports.Reader) error {
		connections, connectionErr := reader.ListProviderConnections(ctx)
		if connectionErr != nil {
			return connectionErr
		}
		if len(connections) > 0 {
			allowSecretKeyCreate = false
		}
		setups, setupErr := reader.ListHarnessSetups(ctx)
		if setupErr != nil {
			return setupErr
		}
		if len(setups) > 0 {
			allowSecretKeyCreate = false
		}
		// Any owner's secret means the installation key already exists.
		items, _, err := reader.ListSecrets(ctx, "", ports.Page{Limit: 1})
		if err == nil && len(items) > 0 {
			allowSecretKeyCreate = false
		}
		return err
	}); err != nil {
		return fmt.Errorf("inspect stored credentials: %w", err)
	}
	secretKey, err := secrets.OpenOrCreateKey(secretKeyPath, allowSecretKeyCreate)
	if err != nil {
		return fmt.Errorf("initialize credential encryption: %w", err)
	}
	defer secretKey.Zero()
	// Bind each credential envelope sealed before owners existed to its
	// row's owner, before anything can open one (ADR 0046). This daemon
	// holds the data-directory lease, so no other writer races it.
	rewrapped, err := app.BindEnvelopeOwners(ctx, store, secretKey)
	if err != nil {
		_ = httpServer.Close()
		_ = previewHTTPServer.Close()
		return fmt.Errorf("bind credential envelopes to owners: %w", err)
	}
	if rewrapped.Secrets+rewrapped.Connections > 0 {
		config.Logger.Printf("bound %d secret and %d connection envelopes to their owners",
			rewrapped.Secrets, rewrapped.Connections)
	}
	if rewrapped.Unreadable > 0 {
		config.Logger.Printf("%d credential envelopes do not open with the installation key and were left unchanged",
			rewrapped.Unreadable)
	}
	transcriptKeyPath := config.TranscriptKeyFile
	if transcriptKeyPath == "" {
		transcriptKeyPath = transcripts.DefaultKeyPath(config.DataDir)
	}
	transcriptKey, err := transcripts.OpenOrCreateKey(transcriptKeyPath)
	if err != nil {
		_ = httpServer.Close()
		_ = previewHTTPServer.Close()
		return fmt.Errorf("initialize transcript encryption: %w", err)
	}
	defer transcriptKey.Zero()
	artifactStore, err := artifacts.Open(config.DataDir)
	if err != nil {
		_ = httpServer.Close()
		_ = previewHTTPServer.Close()
		return err
	}

	var provider ports.CapsuleProvider
	var closeProvider func() error
	var gatewayListener net.Listener
	var gatewayOrigin string
	if testProvider != nil {
		var err error
		provider, closeProvider, err = testProvider(ctx, config)
		if err != nil {
			_ = httpServer.Close()
			_ = previewHTTPServer.Close()
			return err
		}
		if closeProvider == nil {
			closeProvider = func() error { return nil }
		}
	} else {
		switch config.Provider {
		case "docker":
			if config.Docker.StateDir == "" {
				config.Docker.StateDir = filepath.Join(config.DataDir, "docker-provider")
			}
			if config.DockerCPUs > 0 {
				config.Docker.NanoCPUs = int64(config.DockerCPUs * 1_000_000_000)
			}
			dockerProvider, err := dockerprovider.New(config.Docker)
			if err != nil {
				_ = httpServer.Close()
				_ = previewHTTPServer.Close()
				return fmt.Errorf("initialize Docker provider: %w", err)
			}
			provider = dockerProvider
			closeProvider = dockerProvider.Close
			if config.ProviderGatewayURL == "" && config.ProviderGatewayListen != LocalGatewayOff {
				gatewayListener, gatewayOrigin, err = openLocalGateway(ctx, config.ProviderGatewayListen, dockerProvider)
				switch {
				case err != nil && config.ProviderGatewayListen != "":
					_ = dockerProvider.Close()
					_ = httpServer.Close()
					_ = previewHTTPServer.Close()
					return err
				case err != nil:
					config.Logger.Printf(
						"WARNING: provider connections are unavailable: no local provider gateway (%v); "+
							"set --provider-gateway-listen or --provider-gateway-url", err,
					)
				default:
					defer gatewayListener.Close()
				}
			}
		case "agentsandbox":
			agentSandboxProvider, err := agentsandboxprovider.New(config.AgentSandbox)
			if err != nil {
				_ = httpServer.Close()
				_ = previewHTTPServer.Close()
				return fmt.Errorf("initialize Agent Sandbox provider: %w", err)
			}
			provider = agentSandboxProvider
			closeProvider = agentSandboxProvider.Close
		}
	}
	defer closeProvider()
	providerStarted := time.Now()
	capabilities, err := provider.Capabilities(ctx)
	metrics.ProviderOperation(config.Provider, "capabilities", daemonOperationResult(err), time.Since(providerStarted))
	if err != nil {
		_ = httpServer.Close()
		_ = previewHTTPServer.Close()
		return fmt.Errorf("initialize provider: %w", err)
	}
	clock := system.Clock{}
	ids := system.IDs{}
	reconciler := app.NewReconciler(store, provider, clock, ids)
	reconciler.ConfigureSecrets(secretKey)
	reconciler.ConfigureObserver(metrics, config.Provider)
	var snapshotter ports.WorkspaceSnapshotter
	if capabilities.Snapshot {
		snapshotter, _ = provider.(ports.WorkspaceSnapshotter)
		if snapshotter == nil {
			_ = httpServer.Close()
			_ = previewHTTPServer.Close()
			return errors.New("provider advertises Snapshot without implementing it")
		}
		reconciler.ConfigureTemporal(snapshotter, artifactStore)
	}
	var runtime ports.CapsuleRuntime
	if capabilities.Run || capabilities.Git || capabilities.Attach {
		runtime, _ = provider.(ports.CapsuleRuntime)
	}
	service := app.NewService(store, clock, ids, reconciler, runtime)
	service.ConfigureSecrets(secretKey)
	reconciler.ConfigureRuntimeRecovery(service.FailStoppedRuntimeRuns)
	reconciler.OnCapsuleSettled(func(ctx context.Context, capsule domain.Capsule) {
		_ = service.AdvanceProjectDeletion(ctx, capsule.ProjectID)
	})
	var gatewayServer *http.Server
	if gatewayListener != nil {
		authority, err := gatewaytls.NewAuthority(nil, providergateway.LeaseLifetime)
		if err != nil {
			return fmt.Errorf("create provider gateway CA: %w", err)
		}
		if err := service.ConfigureLocalProviderGateway(gatewayOrigin, authority.Trust, config.ProviderGatewayAllowPrivateHTTP); err != nil {
			return err
		}
		gatewayServer = gatewaytls.Server(authority, service.ProviderGateway())
	} else if err := service.ConfigureProviderGateway(config.ProviderGatewayURL, config.ProviderGatewayAllowPrivateHTTP); err != nil {
		return err
	}
	defer service.CloseProviderGateway()
	gatewayServerErrors := make(chan error, 1)
	if gatewayServer != nil {
		go func() {
			err := gatewayServer.ServeTLS(gatewayListener, "", "")
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				gatewayServerErrors <- err
			}
			close(gatewayServerErrors)
		}()
		defer gatewayServer.Close()
		config.Logger.Printf(
			"provider gateway for Capsules at %s (listening on %s)",
			gatewayOrigin, gatewayListener.Addr(),
		)
	}
	service.ConfigureChatGPT(config.DataDir, nil)
	defer service.CloseChatGPT()
	service.ConfigureObserver(metrics, config.Provider)
	harnessPacks := domain.InstallationHarnessImages(installationDefaultImage(config), config.OfficialPackTag)
	service.ConfigureHarnessPacks(harnessPacks)
	if capabilities.Run {
		if runtime == nil {
			_ = httpServer.Close()
			_ = previewHTTPServer.Close()
			return errors.New("provider advertises Run without implementing it")
		}
		profiles, _ := provider.(ports.HarnessProfileRuntime)
		if profiles == nil {
			_ = httpServer.Close()
			_ = previewHTTPServer.Close()
			return errors.New("provider advertises Run without harness profile discovery")
		}
		service.ConfigureHarnessProfiles(profiles)
		reconciler.ConfigureLauncher(service.ReconcileCapsuleLauncher)
	}
	if capabilities.Structured {
		structuredRuntime, _ := provider.(ports.StructuredRuntime)
		if structuredRuntime == nil {
			_ = httpServer.Close()
			_ = previewHTTPServer.Close()
			return errors.New("provider advertises Structured without implementing it")
		}
		service.ConfigureThreads(structuredRuntime, transcriptKey)
		reconciler.ConfigureProjectThreads(service.ReconcileProjectThreadCapsule)
	}
	if capabilities.Preview {
		previewRuntime, _ := provider.(ports.PreviewRuntime)
		if previewRuntime == nil {
			_ = httpServer.Close()
			_ = previewHTTPServer.Close()
			return errors.New("provider advertises Preview without implementing it")
		}
		service.ConfigurePreview(previewRuntime)
	}
	if capabilities.Browse {
		browser, _ := provider.(ports.WorkspaceBrowser)
		if browser == nil {
			_ = httpServer.Close()
			_ = previewHTTPServer.Close()
			return errors.New("provider advertises Browse without implementing it")
		}
		service.ConfigureBrowse(browser)
	}
	if capabilities.Delivery {
		deliveryRuntime, _ := provider.(ports.DeliveryRuntime)
		if deliveryRuntime == nil {
			_ = httpServer.Close()
			_ = previewHTTPServer.Close()
			return errors.New("provider advertises Delivery without implementing it")
		}
		service.ConfigureDelivery(deliveryRuntime, app.NewGitHubHTTPClient(nil))
	}
	if capabilities.Coordination && capabilities.Structured {
		coordinationRuntime, _ := provider.(ports.CoordinationRuntime)
		if coordinationRuntime == nil {
			_ = httpServer.Close()
			_ = previewHTTPServer.Close()
			return errors.New("provider advertises Coordination without implementing it")
		}
		service.ConfigureCoordination(coordinationRuntime, config.MaxLiveCapsules)
	}
	if snapshotter != nil {
		service.ConfigureTemporal(snapshotter, artifactStore)
	}
	previewBaseURL := "http://" + previewListener.Addr().String()
	api := httpapi.NewWithPreview(service, capabilities, previewBaseURL, apiToken)
	api.SetHarnessPacks(harnessPacks)
	api.ConfigureObservability(metrics)
	handler.set(api)
	previewHandler.set(api.PreviewHandler())
	// Startup recovery and the background loops below are daemon-internal
	// work and run as the system principal (ADR 0046). The API never sees
	// this context: requests get their principal from internal/httpapi.
	systemCtx := principal.WithSystem(ctx)
	recoveryStarted := time.Now()
	if capabilities.Structured {
		if err := service.RecoverProjectThreads(systemCtx); err != nil {
			metrics.StartupRecovery("project_threads", "error", time.Since(recoveryStarted))
			_ = httpServer.Close()
			_ = previewHTTPServer.Close()
			return fmt.Errorf("Project Thread startup recovery: %w", err)
		}
		metrics.StartupRecovery("project_threads", "ok", time.Since(recoveryStarted))
	}
	reconciler.Start(systemCtx)
	defer reconciler.Close()
	recoveryStarted = time.Now()
	if err := reconciler.Recover(systemCtx); err != nil {
		metrics.StartupRecovery("capsules", "error", time.Since(recoveryStarted))
		_ = httpServer.Close()
		_ = previewHTTPServer.Close()
		return fmt.Errorf("startup recovery: %w", err)
	}
	metrics.StartupRecovery("capsules", "ok", time.Since(recoveryStarted))
	recoveryStarted = time.Now()
	if err := service.RecoverProjectDeletions(systemCtx); err != nil {
		metrics.StartupRecovery("projects", "error", time.Since(recoveryStarted))
		_ = httpServer.Close()
		_ = previewHTTPServer.Close()
		return fmt.Errorf("project deletion recovery: %w", err)
	}
	metrics.StartupRecovery("projects", "ok", time.Since(recoveryStarted))
	recoveryStarted = time.Now()
	if err := service.RecoverThreads(systemCtx); err != nil {
		metrics.StartupRecovery("threads", "error", time.Since(recoveryStarted))
		_ = httpServer.Close()
		_ = previewHTTPServer.Close()
		return fmt.Errorf("Thread startup recovery: %w", err)
	}
	metrics.StartupRecovery("threads", "ok", time.Since(recoveryStarted))
	recoveryStarted = time.Now()
	if err := service.RecoverRuns(systemCtx); err != nil {
		metrics.StartupRecovery("runs", "error", time.Since(recoveryStarted))
		_ = httpServer.Close()
		_ = previewHTTPServer.Close()
		return fmt.Errorf("Run startup recovery: %w", err)
	}
	metrics.StartupRecovery("runs", "ok", time.Since(recoveryStarted))
	if capabilities.Delivery {
		recoveryStarted = time.Now()
		if err := service.RecoverDeliveries(systemCtx); err != nil {
			metrics.StartupRecovery("deliveries", "error", time.Since(recoveryStarted))
			_ = httpServer.Close()
			_ = previewHTTPServer.Close()
			return fmt.Errorf("Delivery startup recovery: %w", err)
		}
		metrics.StartupRecovery("deliveries", "ok", time.Since(recoveryStarted))
	}
	if config.IdlePause > 0 {
		if config.IdleScanInterval <= 0 {
			return fmt.Errorf("idle scan interval must be positive when idle pause is enabled")
		}
		if err := reconciler.StartIdleScanner(systemCtx, config.IdleScanInterval, config.IdlePause); err != nil {
			return fmt.Errorf("start idle Capsule scanner: %w", err)
		}
	}
	if config.RuntimeRefresh > 0 {
		refreshCtx, stopRefresh := context.WithCancel(systemCtx)
		refreshDone := make(chan struct{})
		go func() {
			defer close(refreshDone)
			ticker := time.NewTicker(config.RuntimeRefresh)
			defer ticker.Stop()
			for {
				select {
				case <-refreshCtx.Done():
					return
				case <-ticker.C:
					_ = service.RefreshLiveRuntime(refreshCtx)
				}
			}
		}()
		defer func() {
			stopRefresh()
			<-refreshDone
		}()
	}
	if service.CoordinationSupported() {
		coordinationCtx, stopCoordination := context.WithCancel(systemCtx)
		coordinationDone := make(chan struct{})
		go func() {
			defer close(coordinationDone)
			service.RunCoordination(coordinationCtx)
		}()
		defer func() {
			stopCoordination()
			<-coordinationDone
		}()
	}
	api.SetReady(true)
	config.Logger.Printf(
		"meridiand ready on %s (previews %s) with %s provider",
		listener.Addr(), previewListener.Addr(), config.Provider,
	)

	select {
	case <-ctx.Done():
	case err := <-lease.Lost():
		// Another owner now writes here; stop at once rather than shut down
		// gracefully, so this daemon writes nothing more.
		return fmt.Errorf("stop using data directory: %w", err)
	case err := <-serverErrors:
		if err != nil {
			return fmt.Errorf("serve API: %w", err)
		}
	case err := <-previewServerErrors:
		if err != nil {
			return fmt.Errorf("serve previews: %w", err)
		}
	case err := <-metricsServerErrors:
		if err != nil {
			return fmt.Errorf("serve metrics: %w", err)
		}
	case err := <-gatewayServerErrors:
		if err != nil {
			return fmt.Errorf("serve provider gateway: %w", err)
		}
	}
	api.SetReady(false)
	metrics.SetHealthy(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown API: %w", err)
	}
	if err := previewHTTPServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown previews: %w", err)
	}
	if metricsListener != nil {
		if err := metricsHTTPServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown metrics: %w", err)
		}
	}
	if gatewayServer != nil {
		// Streams may be open; revocation on CloseProviderGateway ends them.
		service.CloseProviderGateway()
		if err := gatewayServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown provider gateway: %w", err)
		}
	}
	return nil
}

func daemonOperationResult(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, domain.ErrNotFound):
		return "not_found"
	case errors.Is(err, domain.ErrUnsupported):
		return "unsupported"
	default:
		return "error"
	}
}

func ValidatePreviewListenAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid preview listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("preview listener must use a literal loopback IP address")
	}
	return nil
}

func ValidateAPIListenAddress(address string, allowNonLoopback, authConfigured bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid API listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	if !authConfigured {
		return errors.New("non-loopback API listener requires configured authentication")
	}
	if !allowNonLoopback {
		return errors.New("non-loopback API listener requires --allow-non-loopback-listen")
	}
	return nil
}

type switchHandler struct {
	mu      sync.RWMutex
	current http.Handler
}

func newSwitchHandler() *switchHandler {
	return &switchHandler{current: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/healthz":
			writer.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ok"})
		case "/readyz":
			writer.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(writer).Encode(map[string]string{"status": "not_ready"})
		default:
			http.NotFound(writer, request)
		}
	})}
}

func installationDefaultImage(config Config) string {
	switch config.Provider {
	case "agentsandbox":
		if strings.TrimSpace(config.AgentSandbox.Image) != "" {
			return config.AgentSandbox.Image
		}
	default:
		if strings.TrimSpace(config.Docker.Image) != "" {
			return config.Docker.Image
		}
	}
	return domain.LocalCapsuleImage
}

func (h *switchHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	h.mu.RLock()
	current := h.current
	h.mu.RUnlock()
	current.ServeHTTP(writer, request)
}

func (h *switchHandler) set(handler http.Handler) {
	h.mu.Lock()
	h.current = handler
	h.mu.Unlock()
}
