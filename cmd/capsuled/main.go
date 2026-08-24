package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/OrlojHQ/meridian/internal/buildinfo"
	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/spf13/cobra"
)

func newRootCommand() *cobra.Command {
	command := &cobra.Command{
		Use:           "capsuled",
		Short:         "Run the Meridian Capsule supervisor",
		Version:       buildinfo.Current().String(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	command.SetVersionTemplate("capsuled {{.Version}}\n")
	command.AddCommand(newServeCommand(), newHealthcheckCommand())
	return command
}

func newServeCommand() *cobra.Command {
	var listen, workspace, tokenFile string
	var setupTimeout time.Duration
	command := &cobra.Command{
		Use:   "serve",
		Short: "Serve the private Capsule protocol",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if os.Geteuid() == 0 {
				return errors.New("capsuled refuses to run as root")
			}
			token, err := loadToken(tokenFile)
			if err != nil {
				return err
			}
			server, err := capsuleproto.NewServer(capsuleproto.ServerConfig{
				Token: token, Workspace: workspace, SetupTimeout: setupTimeout,
			})
			if err != nil {
				return err
			}
			httpServer := &http.Server{
				Addr:              listen,
				Handler:           server.Handler(),
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       0,
				WriteTimeout:      0,
				IdleTimeout:       30 * time.Second,
				MaxHeaderBytes:    16 << 10,
				ErrorLog:          log.New(io.Discard, "", 0),
			}
			errorsChannel := make(chan error, 1)
			go func() {
				err := httpServer.ListenAndServe()
				if err != nil && !errors.Is(err, http.ErrServerClosed) {
					errorsChannel <- err
				}
				close(errorsChannel)
			}()
			select {
			case <-command.Context().Done():
			case err := <-errorsChannel:
				if err != nil {
					return fmt.Errorf("serve Capsule protocol: %w", err)
				}
			}
			shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return httpServer.Shutdown(shutdownContext)
		},
	}
	command.Flags().StringVar(&listen, "listen", ":7777", "private protocol listen address")
	command.Flags().StringVar(&workspace, "workspace", "/workspace", "Capsule workspace path")
	command.Flags().StringVar(&tokenFile, "token-file", "", "read protocol token from a private file")
	command.Flags().DurationVar(&setupTimeout, "setup-timeout", 10*time.Minute, "maximum clone and setup duration")
	return command
}

func newHealthcheckCommand() *cobra.Command {
	var url string
	command := &cobra.Command{
		Use:   "healthcheck",
		Short: "Check the local supervisor health endpoint",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			request, err := http.NewRequestWithContext(command.Context(), http.MethodGet, url, nil)
			if err != nil {
				return err
			}
			client := &http.Client{Timeout: 2 * time.Second}
			response, err := client.Do(request)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return fmt.Errorf("health status %d", response.StatusCode)
			}
			return nil
		},
	}
	command.Flags().StringVar(&url, "url", "http://127.0.0.1:7777/healthz", "health URL")
	return command
}

func loadToken(path string) (string, error) {
	if path == "" {
		token := os.Getenv("MERIDIAN_CAPSULE_TOKEN")
		_ = os.Unsetenv("MERIDIAN_CAPSULE_TOKEN")
		if token == "" {
			return "", errors.New("MERIDIAN_CAPSULE_TOKEN or --token-file is required")
		}
		return token, nil
	}
	resolvedPath := path
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect token file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolvedPath, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", fmt.Errorf("resolve token file: %w", err)
		}
		mountDirectory, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return "", fmt.Errorf("resolve token mount directory: %w", err)
		}
		relative, err := filepath.Rel(mountDirectory, resolvedPath)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", errors.New("token file symlink leaves its mount directory")
		}
		info, err = os.Lstat(resolvedPath)
		if err != nil {
			return "", fmt.Errorf("inspect resolved token file: %w", err)
		}
	}
	// Docker uses 0600. Kubernetes Secret projection with an fsGroup widens a
	// requested 0400 mode to 0440 so the non-root process can read the
	// root-owned file. Accept only these private read shapes; never permit
	// other access, execution, or group write.
	switch info.Mode().Perm() {
	case 0o400, 0o440, 0o600, 0o640:
	default:
		return "", errors.New("token file must be regular and privately readable")
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("token file must be regular and privately readable")
	}
	value, err := os.ReadFile(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	return string(value), nil
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
