package main

import (
	"context"
	"crypto/tls"
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
	"github.com/OrlojHQ/meridian/internal/coordproto"
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
	var listen, workspace, tokenFile, coordinationSocket, sessionDir string
	var tlsListen, tlsCertFile, tlsKeyFile, tlsClientCertFile string
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
			tlsConfig, err := loadPodTLS(tlsListen, tlsCertFile, tlsKeyFile, tlsClientCertFile)
			if err != nil {
				return err
			}
			// toFile output must stay outside the workspace; an image whose
			// home is inside it gets no coordination files.
			coordinationFiles := capsuleproto.CoordinationFilesDir(coordinationSocket)
			if coordinationFiles != "" && !capsuleproto.OutsideWorkspace(workspace, coordinationFiles) {
				fmt.Fprintln(os.Stderr, "capsuled: coordination files disabled: directory is inside the workspace")
				coordinationFiles = ""
			}
			// Notes attached to a spawn are read from the lead's home, never
			// from the workspace the repository controls.
			coordinationNotes := capsuleproto.CoordinationNotesDir(coordinationSocket)
			if coordinationNotes != "" && !capsuleproto.OutsideWorkspace(workspace, coordinationNotes) {
				fmt.Fprintln(os.Stderr, "capsuled: spawn contextFiles disabled: notes directory is inside the workspace")
				coordinationNotes = ""
			}
			// Project notes and working notes are written under the home,
			// never into the workspace a Delivery or a Moment captures.
			notesBase := capsuleproto.NotesBaseDir(coordinationSocket)
			if notesBase != "" && (!capsuleproto.OutsideWorkspace(workspace, notesBase) ||
				!capsuleproto.OutsideWorkspace(workspace, filepath.Join(notesBase, coordproto.ProjectNotesDirName)) ||
				!capsuleproto.OutsideWorkspace(workspace, filepath.Join(notesBase, coordproto.WorkingNotesDirName))) {
				fmt.Fprintln(os.Stderr, "capsuled: notes delivery disabled: directory is inside the workspace")
				notesBase = ""
			}
			// A Kubernetes workspace volume root stays owned by root under
			// fsGroup, and git then refuses the repository in it. Set before
			// any child starts, so every one inherits it.
			if err := capsuleproto.TrustWorkspaceForGit(workspace); err != nil {
				fmt.Fprintln(os.Stderr, "capsuled: git workspace trust disabled:", err)
			}
			server, err := capsuleproto.NewServer(capsuleproto.ServerConfig{
				Token: token, Workspace: workspace, SessionDir: sessionDir, SetupTimeout: setupTimeout,
				CoordinationFiles: coordinationFiles, CoordinationNotes: coordinationNotes,
				NotesBase: notesBase,
			})
			if err != nil {
				return err
			}
			if coordinationSocket != "" {
				listener, err := capsuleproto.ListenCoordination(coordinationSocket)
				if err != nil {
					fmt.Fprintln(os.Stderr, "capsuled: coordination mailbox unavailable:", err)
				} else {
					defer listener.Close()
					if err := os.Setenv(coordproto.SocketEnv, coordinationSocket); err != nil {
						return err
					}
					go func() { _ = server.ServeCoordination(listener) }()
				}
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
			servers := []*http.Server{httpServer}
			if tlsConfig != nil {
				// ADR 0065: the pod-network listener serves the same handler,
				// but only to the one client certificate the control plane
				// holds. HTTP/2 stays off so attach upgrades keep working.
				podServer := &http.Server{
					Addr:              tlsListen,
					Handler:           server.Handler(),
					TLSConfig:         tlsConfig,
					TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
					ReadHeaderTimeout: httpServer.ReadHeaderTimeout,
					IdleTimeout:       httpServer.IdleTimeout,
					MaxHeaderBytes:    httpServer.MaxHeaderBytes,
					ErrorLog:          httpServer.ErrorLog,
				}
				servers = append(servers, podServer)
			}
			errorsChannel := make(chan error, len(servers))
			for _, each := range servers {
				go func() {
					var err error
					if each.TLSConfig != nil {
						err = each.ListenAndServeTLS("", "")
					} else {
						err = each.ListenAndServe()
					}
					if err != nil && !errors.Is(err, http.ErrServerClosed) {
						errorsChannel <- err
					}
				}()
			}
			var serveErr error
			select {
			case <-command.Context().Done():
			case serveErr = <-errorsChannel:
			}
			server.Close()
			shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var shutdownErr error
			for _, each := range servers {
				shutdownErr = errors.Join(shutdownErr, each.Shutdown(shutdownContext))
			}
			if serveErr != nil {
				return fmt.Errorf("serve Capsule protocol: %w", serveErr)
			}
			return shutdownErr
		},
	}
	command.Flags().StringVar(&listen, "listen", ":7777", "private protocol listen address")
	command.Flags().StringVar(&tlsListen, "tls-listen", "", "pod-network mutual TLS listen address (ADR 0065; empty disables)")
	command.Flags().StringVar(&tlsCertFile, "tls-cert-file", "", "pod-network listener certificate")
	command.Flags().StringVar(&tlsKeyFile, "tls-key-file", "", "pod-network listener private key")
	command.Flags().StringVar(&tlsClientCertFile, "tls-client-cert-file", "", "the one client certificate the pod-network listener accepts")
	command.Flags().StringVar(&workspace, "workspace", "/workspace", "Capsule workspace path")
	command.Flags().StringVar(&tokenFile, "token-file", "", "read protocol token from a private file")
	command.Flags().DurationVar(&setupTimeout, "setup-timeout", 10*time.Minute, "maximum clone and setup duration")
	command.Flags().StringVar(
		&coordinationSocket, "coordination-socket", defaultCoordinationSocket(),
		"Thread coordination mailbox socket (empty disables)",
	)
	command.Flags().StringVar(
		&sessionDir, "session-dir", "",
		"ephemeral directory for short-lived session credentials and git helpers",
	)
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

func defaultCoordinationSocket() string {
	home := os.Getenv("HOME")
	if !filepath.IsAbs(home) || home == "/" {
		return ""
	}
	return coordproto.SocketPathForHome(filepath.Clean(home))
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
	value, err := readPrivateFile(path, "token", 4<<10)
	return string(value), err
}

// loadPodTLS reads the pod-network listener material (ADR 0065). The four
// settings come together or not at all: there is no TLS mode without client
// certificate verification.
func loadPodTLS(listen, certFile, keyFile, clientCertFile string) (*tls.Config, error) {
	set := 0
	for _, value := range []string{listen, certFile, keyFile, clientCertFile} {
		if value != "" {
			set++
		}
	}
	if set == 0 {
		return nil, nil
	}
	if set != 4 {
		return nil, errors.New("--tls-listen, --tls-cert-file, --tls-key-file, and --tls-client-cert-file must be set together")
	}
	if strings.HasPrefix(listen, "127.") || strings.HasPrefix(listen, "localhost:") {
		return nil, errors.New("--tls-listen is the pod-network listener; loopback stays on --listen")
	}
	var files [3][]byte
	for index, path := range []string{certFile, keyFile, clientCertFile} {
		value, err := readPrivateFile(path, "TLS", 16<<10)
		if err != nil {
			return nil, err
		}
		files[index] = value
	}
	return capsuleproto.ServerTLSConfig(files[0], files[1], files[2])
}

// readPrivateFile reads a bounded, privately readable regular file. A
// Kubernetes Secret projection is a symlink into the mount's data directory;
// it may not resolve outside that mount.
func readPrivateFile(path, label string, limit int64) ([]byte, error) {
	resolvedPath := path
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect %s file: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolvedPath, err = filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("resolve %s file: %w", label, err)
		}
		mountDirectory, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return nil, fmt.Errorf("resolve %s mount directory: %w", label, err)
		}
		relative, err := filepath.Rel(mountDirectory, resolvedPath)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s file symlink leaves its mount directory", label)
		}
		info, err = os.Lstat(resolvedPath)
		if err != nil {
			return nil, fmt.Errorf("inspect resolved %s file: %w", label, err)
		}
	}
	// Docker uses 0600. Kubernetes Secret projection with an fsGroup widens a
	// requested 0400 mode to 0440 so the non-root process can read the
	// root-owned file. Accept only these private read shapes; never permit
	// other access, execution, or group write.
	switch info.Mode().Perm() {
	case 0o400, 0o440, 0o600, 0o640:
	default:
		return nil, fmt.Errorf("%s file must be regular and privately readable", label)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s file must be regular and privately readable", label)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s file exceeds %d bytes", label, limit)
	}
	file, err := os.Open(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("read %s file: %w", label, err)
	}
	defer file.Close()
	value, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s file: %w", label, err)
	}
	if int64(len(value)) > limit {
		return nil, fmt.Errorf("%s file exceeds %d bytes", label, limit)
	}
	return value, nil
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
