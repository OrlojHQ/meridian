package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/githubauth"
	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
)

func newGitHubCommand(config *cliConfig) *cobra.Command {
	github := &cobra.Command{
		Use:   "github",
		Short: "Connect GitHub for clone, push, and pull requests",
	}
	github.AddCommand(newGitHubConnect(config), newGitHubStatus(config), newGitHubDisconnect(config))
	return github
}

func newGitHubConnect(config *cliConfig) *cobra.Command {
	var yes, deviceOnly, fromStdin bool
	var ghPath string
	command := &cobra.Command{
		Use:   "connect",
		Short: "Connect GitHub without pasting a token into a Project",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			current, err := api.GetGitHubConnection(command.Context())
			if err != nil {
				return apiCallError(err)
			}
			expected, _ := current.ResourceVersion.Get()
			if !current.Connected {
				expected = 0
			}
			if fromStdin {
				token, err := io.ReadAll(io.LimitReader(config.stdin, 4096))
				if err != nil {
					return fmt.Errorf("read token: %w", err)
				}
				defer clearBytes(token)
				key, err := idempotencyKey("")
				if err != nil {
					return err
				}
				return storeGitHubToken(command, config, api, strings.TrimSpace(string(token)), expected, key)
			}
			if !deviceOnly {
				host, err := api.GetHostGitHubLogin(command.Context())
				if err != nil {
					return apiCallError(err)
				}
				if host.GetAvailable() && host.GetState() == client.HostGitHubLoginStateSignedIn {
					login, _ := host.GetLogin().Get()
					if login != "" {
						accepted, err := confirmGitHub(config, yes, fmt.Sprintf("Use your GitHub CLI login (@%s)?", login))
						if err != nil {
							return err
						}
						if !accepted {
							_, err = fmt.Fprintln(config.stdout, "GitHub was not connected")
							return err
						}
						key, err := idempotencyKey("")
						if err != nil {
							return err
						}
						result, err := api.UseHostGitHubLogin(command.Context(), &client.UseHostGitHubRequest{
							ExpectedResourceVersion: expected,
						}, client.UseHostGitHubLoginParams{IdempotencyKey: key})
						if err != nil {
							return apiCallError(err)
						}
						connected, ok := result.(*client.GitHubConnection)
						if !ok {
							return responseError(result)
						}
						return printGitHubConnection(config, connected)
					}
				}
				local := (githubauth.HostCommands{Path: ghPath}).Probe(command.Context())
				if local.State == githubauth.StateSignedIn && local.Login != "" {
					accepted, err := confirmGitHub(config, yes, fmt.Sprintf("Use your GitHub CLI login (@%s)?", local.Login))
					if err != nil {
						return err
					}
					if !accepted {
						_, err = fmt.Fprintln(config.stdout, "GitHub was not connected")
						return err
					}
					token, err := (githubauth.HostCommands{Path: ghPath}).Token(command.Context())
					if err != nil {
						return errors.New(local.Path + " is signed in, but its token could not be read")
					}
					key, err := idempotencyKey("")
					if err != nil {
						return err
					}
					return storeGitHubToken(command, config, api, token, expected, key)
				}
				if local.Detail != "" {
					_, _ = fmt.Fprintln(command.ErrOrStderr(), local.Detail)
				} else if detail, ok := host.GetDetail().Get(); ok && detail != "" {
					_, _ = fmt.Fprintln(command.ErrOrStderr(), detail)
				}
				if !current.GetDeviceFlow() {
					return errors.New("this Station has no GitHub OAuth client id, so a device code is unavailable")
				}
			}
			return runGitHubDevice(command, config, api, expected)
		},
	}
	command.Flags().BoolVar(&yes, "yes", false, "accept the GitHub login already found on this computer")
	command.Flags().BoolVar(&deviceOnly, "device", false, "skip an existing gh login and use a device code")
	command.Flags().BoolVar(&fromStdin, "stdin", false, "read a token from stdin instead of gh or a device code")
	command.Flags().StringVar(&ghPath, "gh-path", os.Getenv("MERIDIAN_GH_PATH"), "absolute path to the GitHub CLI (default: PATH, then the usual install locations)")
	return command
}

func newGitHubStatus(config *cliConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the connected GitHub login",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			current, err := api.GetGitHubConnection(command.Context())
			if err != nil {
				return apiCallError(err)
			}
			return printGitHubConnection(config, current)
		},
	}
}

func newGitHubDisconnect(config *cliConfig) *cobra.Command {
	var confirm bool
	command := &cobra.Command{
		Use:   "disconnect",
		Short: "Delete the stored GitHub login",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if !confirm {
				return errors.New("--confirm is required; disconnect deletes the stored login")
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			current, err := api.GetGitHubConnection(command.Context())
			if err != nil {
				return apiCallError(err)
			}
			if !current.Connected {
				return printGitHubConnection(config, current)
			}
			version, ok := current.ResourceVersion.Get()
			if !ok {
				return errors.New("GitHub connection has no version")
			}
			key, err := idempotencyKey("")
			if err != nil {
				return err
			}
			result, err := api.DisconnectGitHub(command.Context(), &client.DisconnectGitHubRequest{
				ExpectedResourceVersion: version,
			}, client.DisconnectGitHubParams{IdempotencyKey: key})
			if err != nil {
				return apiCallError(err)
			}
			disconnected, ok := result.(*client.GitHubConnection)
			if !ok {
				return responseError(result)
			}
			return printGitHubConnection(config, disconnected)
		},
	}
	command.Flags().BoolVar(&confirm, "confirm", false, "confirm deletion of the stored login")
	return command
}

func storeGitHubToken(command *cobra.Command, config *cliConfig, api *client.Client, token string, expected int64, key string) error {
	result, err := api.ConnectGitHubToken(command.Context(), &client.ConnectGitHubTokenRequest{
		Token: token, ExpectedResourceVersion: expected,
	}, client.ConnectGitHubTokenParams{IdempotencyKey: key})
	if err != nil {
		return apiCallError(err)
	}
	connected, ok := result.(*client.GitHubConnection)
	if !ok {
		return responseError(result)
	}
	return printGitHubConnection(config, connected)
}

func runGitHubDevice(command *cobra.Command, config *cliConfig, api *client.Client, expected int64) error {
	result, err := api.BeginGitHubDevice(command.Context(), &client.BeginGitHubDeviceRequest{
		ExpectedResourceVersion: expected,
	})
	if err != nil {
		return apiCallError(err)
	}
	started, ok := result.(*client.GitHubDevice)
	if !ok {
		return responseError(result)
	}
	fmt.Fprintf(config.stdout, "Enter this code on GitHub: %s\n%s\n", started.UserCode, started.VerificationUri)
	openGitHub(started.VerificationUri)
	interval := time.Duration(started.IntervalSeconds) * time.Second
	if interval < time.Second {
		interval = 5 * time.Second
	}
	for {
		select {
		case <-command.Context().Done():
			return command.Context().Err()
		case <-time.After(interval):
		}
		polled, err := api.PollGitHubDevice(command.Context())
		if err != nil {
			return apiCallError(err)
		}
		status, ok := polled.(*client.GitHubDeviceStatus)
		if !ok {
			return responseError(polled)
		}
		switch status.Status {
		case client.GitHubDeviceStatusStatusPending:
			continue
		case client.GitHubDeviceStatusStatusConnected:
			connection, ok := status.Connection.Get()
			if !ok {
				return errors.New("GitHub connected without a login")
			}
			return printGitHubConnection(config, &connection)
		case client.GitHubDeviceStatusStatusDenied:
			return errors.New("GitHub authorization was declined")
		case client.GitHubDeviceStatusStatusExpired:
			return errors.New("GitHub code expired")
		default:
			return errors.New("GitHub authorization failed")
		}
	}
}

func printGitHubConnection(config *cliConfig, connection *client.GitHubConnection) error {
	if config.json {
		return writeJSON(config.stdout, connection)
	}
	if !connection.Connected {
		_, err := fmt.Fprintln(config.stdout, "GitHub is not connected")
		return err
	}
	login, _ := connection.Login.Get()
	status, _ := connection.Status.Get()
	_, err := fmt.Fprintf(config.stdout, "@%s\t%s\n", login, status)
	return err
}

func confirmGitHub(config *cliConfig, yes bool, prompt string) (bool, error) {
	if yes {
		return true, nil
	}
	file, ok := config.stdin.(*os.File)
	if !ok || !isTerminal(file) {
		return false, errors.New("pass --yes to use the GitHub login, or --device for a device code")
	}
	fmt.Fprintf(os.Stderr, "%s [Y/n] ", prompt)
	reader := bufio.NewReader(config.stdin)
	answer, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "" || answer == "y" || answer == "yes", nil
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func openGitHub(url string) {
	if !strings.HasPrefix(url, "https://github.com/") {
		return
	}
	_ = exec.Command("xdg-open", url).Start()
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
