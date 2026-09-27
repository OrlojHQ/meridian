package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/OrlojHQ/meridian/internal/buildinfo"
	"github.com/OrlojHQ/meridian/internal/daemon"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/spf13/cobra"
)

func newRootCommand() *cobra.Command {
	var config daemon.Config
	command := &cobra.Command{
		Use:           "meridiand",
		Short:         "Run the local Meridian control plane",
		Version:       buildinfo.Current().String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, _ []string) error {
			config.Logger = log.New(command.ErrOrStderr(), "", log.LstdFlags)
			return daemon.Run(command.Context(), config)
		},
	}
	command.SetVersionTemplate("meridiand {{.Version}}\n")
	command.Flags().StringVar(&config.ProviderGatewayURL, "provider-gateway-url", "", "HTTPS Meridian origin reachable from Capsules; enables reusable API connections")
	command.Flags().StringVar(&config.Provider, "provider", "fake", "Capsule provider (fake, docker, or agentsandbox)")
	command.Flags().StringVar(&config.Listen, "listen", "127.0.0.1:8080", "HTTP listen address")
	command.Flags().BoolVar(
		&config.AllowNonLoopback,
		"allow-non-loopback-listen",
		false,
		"explicitly allow authenticated API listening on a non-loopback address",
	)
	command.Flags().StringVar(
		&config.Observability.MetricsListen,
		"metrics-listen",
		"",
		"separate Prometheus metrics listen address (disabled when empty; literal loopback required by default)",
	)
	command.Flags().BoolVar(
		&config.Observability.AllowUnsafeMetricsBind,
		"allow-unsafe-metrics-listen",
		false,
		"explicitly allow non-loopback unauthenticated metrics exposure",
	)
	command.Flags().StringVar(
		&config.Observability.OTLPEndpoint,
		"otel-otlp-endpoint",
		"",
		"OTLP/HTTP trace endpoint host:port (disabled when empty)",
	)
	command.Flags().BoolVar(
		&config.Observability.OTLPInsecure,
		"otel-otlp-insecure",
		false,
		"allow plaintext OTLP/HTTP transport to the configured endpoint",
	)
	command.Flags().StringVar(
		&config.PreviewListen,
		"preview-listen",
		"127.0.0.1:8081",
		"loopback-only preview HTTP listen address",
	)
	command.Flags().StringVar(&config.DataDir, "data-dir", defaultDataDir(), "local state directory")
	command.Flags().StringVar(
		&config.APITokenFile,
		"api-token-file",
		"",
		"installation API token file (default under data-dir)",
	)
	command.Flags().StringVar(
		&config.TranscriptKeyFile,
		"transcript-key-file",
		"",
		"installation transcript key file (default under data-dir; excluded from backups)",
	)
	command.Flags().StringVar(
		&config.SecretKeyFile,
		"secret-key-file",
		"",
		"installation credential key file (default under data-dir; excluded from backups)",
	)
	command.Flags().DurationVar(
		&config.IdlePause,
		"capsule-idle-pause",
		30*time.Minute,
		"pause Ready inactive Capsules after this duration (0 disables)",
	)
	command.Flags().DurationVar(
		&config.IdleScanInterval,
		"capsule-idle-scan-interval",
		time.Minute,
		"interval for bounded inactive Capsule scans",
	)
	command.Flags().DurationVar(
		&config.RuntimeRefresh,
		"runtime-refresh-interval",
		5*time.Second,
		"refresh live Run state and structured Thread output while unwatched (0 disables)",
	)
	command.Flags().StringVar(&config.Docker.Host, "docker-host", "", "Docker endpoint (empty uses official client environment)")
	defaultCapsule := domain.DefaultCapsuleImage(buildinfo.Version)
	command.Flags().StringVar(&config.Docker.Image, "docker-image", defaultCapsule, "default Capsule image (release binaries use the matching GHCR tag)")
	command.Flags().StringVar(
		&config.OfficialPackTag,
		"official-pack-tag",
		"",
		"tag advertised for official harness packs (default: tag from the installation Capsule image)",
	)
	command.Flags().StringVar(&config.Docker.StateDir, "docker-state-dir", "", "protected provider state directory (default under data-dir)")
	command.Flags().StringVar(&config.Docker.NamePrefix, "docker-name-prefix", "meridian", "owned Docker resource name prefix")
	command.Flags().StringVar(&config.Docker.LabelPrefix, "docker-label-prefix", "io.orloj.meridian", "owned Docker label prefix")
	command.Flags().Int64Var(&config.Docker.MemoryBytes, "docker-memory-bytes", 2<<30, "Capsule memory limit in bytes")
	command.Flags().Float64Var(&config.DockerCPUs, "docker-cpus", 2, "Capsule CPU limit")
	command.Flags().Int64Var(&config.Docker.PIDs, "docker-pids", 512, "Capsule process limit")
	command.Flags().DurationVar(&config.Docker.OperationTimeout, "docker-operation-timeout", 30*time.Second, "Docker operation timeout")
	command.Flags().DurationVar(&config.Docker.SetupTimeout, "docker-setup-timeout", 10*time.Minute, "Capsule clone and setup timeout")
	command.Flags().StringVar(&config.AgentSandbox.Kubeconfig, "agentsandbox-kubeconfig", "", "Kubernetes kubeconfig path (empty uses standard loading)")
	command.Flags().StringVar(&config.AgentSandbox.Context, "agentsandbox-context", "", "Kubernetes kubeconfig context")
	command.Flags().BoolVar(&config.AgentSandbox.InCluster, "agentsandbox-in-cluster", false, "use in-cluster Kubernetes credentials")
	command.Flags().StringVar(&config.AgentSandbox.Namespace, "agentsandbox-namespace", "meridian", "existing namespace for all Meridian Sandboxes")
	command.Flags().StringVar(&config.AgentSandbox.NamePrefix, "agentsandbox-name-prefix", "meridian", "owned Sandbox DNS name prefix")
	command.Flags().StringVar(&config.AgentSandbox.Class, "agentsandbox-class", "", "Meridian workload class annotation (policy metadata only)")
	command.Flags().StringVar(&config.AgentSandbox.Template, "agentsandbox-template", "", "reserved SandboxTemplate name (unsupported by direct v1beta1 Sandbox)")
	command.Flags().StringVar(&config.AgentSandbox.Image, "agentsandbox-image", defaultCapsule, "default non-root Capsule image")
	command.Flags().StringVar(&config.AgentSandbox.RuntimeClass, "agentsandbox-runtime-class", "", "Kubernetes RuntimeClass name (gVisor/Kata recommended)")
	command.Flags().StringVar(&config.AgentSandbox.StorageClass, "agentsandbox-storage-class", "", "workspace StorageClass (empty uses cluster default)")
	command.Flags().StringVar(&config.AgentSandbox.VolumeSize, "agentsandbox-volume-size", "10Gi", "workspace PVC request")
	command.Flags().DurationVar(&config.AgentSandbox.TTL, "agentsandbox-ttl", 24*time.Hour, "Sandbox lifetime before deletion")
	command.Flags().StringVar(&config.AgentSandbox.MemoryLimit, "agentsandbox-memory-limit", "2Gi", "Capsule memory limit")
	command.Flags().StringVar(&config.AgentSandbox.CPULimit, "agentsandbox-cpu-limit", "2", "Capsule CPU limit")
	command.Flags().StringVar(&config.AgentSandbox.EphemeralLimit, "agentsandbox-ephemeral-limit", "2Gi", "Capsule ephemeral storage limit")
	command.Flags().DurationVar(&config.AgentSandbox.OperationTimeout, "agentsandbox-operation-timeout", 2*time.Minute, "Kubernetes lifecycle operation timeout")
	command.Flags().DurationVar(&config.AgentSandbox.SetupTimeout, "agentsandbox-setup-timeout", 10*time.Minute, "Capsule clone and setup timeout")
	var healthAddress string
	healthcheck := &cobra.Command{
		Use:          "healthcheck",
		Short:        "Check the local daemon health endpoint",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(command *cobra.Command, _ []string) error {
			request, err := http.NewRequestWithContext(command.Context(), http.MethodGet, healthAddress+"/healthz", nil)
			if err != nil {
				return err
			}
			client := &http.Client{Timeout: 2 * time.Second}
			response, err := client.Do(request)
			if err != nil {
				return fmt.Errorf("health request failed: %w", err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return fmt.Errorf("health endpoint returned %s", response.Status)
			}
			return nil
		},
	}
	healthcheck.Flags().StringVar(&healthAddress, "address", "http://127.0.0.1:8080", "daemon base URL")
	var initDataPath string
	initializeData := &cobra.Command{
		Use:          "init-data-dir",
		Short:        "Initialize a private data directory for a runtime volume",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			if initDataPath == "" {
				return errors.New("--path is required")
			}
			if err := os.MkdirAll(initDataPath, 0o700); err != nil {
				return fmt.Errorf("create data directory: %w", err)
			}
			info, err := os.Lstat(initDataPath)
			if err != nil {
				return err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("data path must be a real directory")
			}
			return os.Chmod(initDataPath, 0o700)
		},
	}
	initializeData.Flags().StringVar(&initDataPath, "path", "", "data directory path to create")
	command.AddCommand(healthcheck, initializeData)
	return command
}

func defaultDataDir() string {
	directory, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(".", "data")
	}
	return filepath.Join(directory, "meridian")
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	command := newRootCommand()
	command.SetContext(ctx)
	if err := command.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
