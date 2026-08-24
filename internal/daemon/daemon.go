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
	"path/filepath"
	"sync"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/artifacts"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/httpapi"
	"github.com/OrlojHQ/meridian/internal/observability"
	"github.com/OrlojHQ/meridian/internal/ports"
	agentsandboxprovider "github.com/OrlojHQ/meridian/internal/provider/agentsandbox"
	dockerprovider "github.com/OrlojHQ/meridian/internal/provider/docker"
	"github.com/OrlojHQ/meridian/internal/provider/fake"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/system"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

type Config struct {
	Provider          string
	Listen            string
	PreviewListen     string
	DataDir           string
	TranscriptKeyFile string
	Logger            *log.Logger
	Docker            dockerprovider.Config
	DockerCPUs        float64
	AgentSandbox      agentsandboxprovider.Config
	Observability     observability.Config
}

func Run(ctx context.Context, config Config) error {
	if config.Provider != "fake" && config.Provider != "docker" && config.Provider != "agentsandbox" {
		return fmt.Errorf("unsupported provider %q (supported: fake, docker, agentsandbox)", config.Provider)
	}
	if config.Listen == "" {
		return fmt.Errorf("listen address is required")
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
	switch config.Provider {
	case "fake":
		provider = fake.New(fake.Options{})
		closeProvider = func() error { return nil }
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
	reconciler.Start(ctx)
	defer reconciler.Close()
	var runtime ports.CapsuleRuntime
	if capabilities.Run || capabilities.Git || capabilities.Attach {
		runtime, _ = provider.(ports.CapsuleRuntime)
	}
	service := app.NewService(store, clock, ids, reconciler, runtime)
	service.ConfigureObserver(metrics, config.Provider)
	if capabilities.Structured {
		structuredRuntime, _ := provider.(ports.StructuredRuntime)
		if structuredRuntime == nil {
			_ = httpServer.Close()
			_ = previewHTTPServer.Close()
			return errors.New("provider advertises Structured without implementing it")
		}
		service.ConfigureThreads(structuredRuntime, transcriptKey)
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
	if snapshotter != nil {
		service.ConfigureTemporal(snapshotter, artifactStore)
	}
	previewBaseURL := "http://" + previewListener.Addr().String()
	api := httpapi.NewWithPreview(service, capabilities, previewBaseURL)
	api.ConfigureObservability(metrics)
	handler.set(api)
	previewHandler.set(api.PreviewHandler())
	recoveryStarted := time.Now()
	if err := reconciler.Recover(ctx); err != nil {
		metrics.StartupRecovery("capsules", "error", time.Since(recoveryStarted))
		_ = httpServer.Close()
		_ = previewHTTPServer.Close()
		return fmt.Errorf("startup recovery: %w", err)
	}
	metrics.StartupRecovery("capsules", "ok", time.Since(recoveryStarted))
	recoveryStarted = time.Now()
	if err := service.RecoverThreads(ctx); err != nil {
		metrics.StartupRecovery("threads", "error", time.Since(recoveryStarted))
		_ = httpServer.Close()
		_ = previewHTTPServer.Close()
		return fmt.Errorf("Thread startup recovery: %w", err)
	}
	metrics.StartupRecovery("threads", "ok", time.Since(recoveryStarted))
	recoveryStarted = time.Now()
	if err := service.RecoverRuns(ctx); err != nil {
		metrics.StartupRecovery("runs", "error", time.Since(recoveryStarted))
		_ = httpServer.Close()
		_ = previewHTTPServer.Close()
		return fmt.Errorf("Run startup recovery: %w", err)
	}
	metrics.StartupRecovery("runs", "ok", time.Since(recoveryStarted))
	api.SetReady(true)
	config.Logger.Printf(
		"meridiand ready on %s (previews %s) with %s provider",
		listener.Addr(), previewListener.Addr(), config.Provider,
	)

	select {
	case <-ctx.Done():
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
