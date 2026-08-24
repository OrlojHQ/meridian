package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/OrlojHQ/meridian/internal/buildinfo"
	"github.com/OrlojHQ/meridian/internal/harnessadapter"
	"github.com/spf13/cobra"
)

func newRootCommand() *cobra.Command {
	command := &cobra.Command{
		Use:           "meridian-harness-adapter",
		Short:         "Run a Meridian structured harness adapter",
		Version:       buildinfo.Current().String(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	command.SetVersionTemplate("meridian-harness-adapter {{.Version}}\n")
	command.AddCommand(
		newGenericCommand(),
		newPiCommand(),
		newOpenCodeCommand(),
		newMockCommand(),
	)
	return command
}

func newGenericCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "generic -- <adapter-executable> [arguments...]",
		Short: "Validate and supervise an adapter-protocol child",
		Long: "Validate both meridian.adapter.v1 directions and supervise an adapter-protocol child.\n" +
			"Capsuled can execute an adapter directly when this extra validation boundary is unnecessary.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return harnessadapter.RunGeneric(
				command.Context(), command.InOrStdin(), command.OutOrStdout(), arguments,
			)
		},
	}
}

func newPiCommand() *cobra.Command {
	var sessionName, sessionDirectory string
	command := &cobra.Command{
		Use:   "pi-rpc -- <pi-executable> [arguments...]",
		Short: "Bridge Pi RPC mode to Meridian",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return harnessadapter.RunPiRPC(
				command.Context(), command.InOrStdin(), command.OutOrStdout(),
				harnessadapter.PiConfig{
					Command: arguments, SessionName: sessionName, SessionDir: sessionDirectory,
				},
			)
		},
	}
	command.Flags().StringVar(&sessionName, "session-name", "", "initial Pi session display name")
	command.Flags().StringVar(&sessionDirectory, "session-dir", "", "Pi session storage directory")
	return command
}

func newOpenCodeCommand() *cobra.Command {
	var sessionName string
	var permissions bool
	command := &cobra.Command{
		Use:   "opencode-server -- <opencode-executable> [arguments...]",
		Short: "Bridge an authenticated OpenCode server to Meridian",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return harnessadapter.RunOpenCode(
				command.Context(), command.InOrStdin(), command.OutOrStdout(),
				harnessadapter.OpenCodeConfig{
					Command: arguments, SessionName: sessionName, Permissions: permissions,
				},
			)
		},
	}
	command.Flags().StringVar(&sessionName, "session-name", "", "initial OpenCode session title")
	command.Flags().BoolVar(
		&permissions, "permissions", true,
		"enable the documented legacy session permission response endpoint",
	)
	return command
}

func newMockCommand() *cobra.Command {
	var fault string
	command := &cobra.Command{
		Use:   "mock",
		Short: "Run the deterministic credential-free test adapter",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			switch fault {
			case "", "gap", "duplicate", "crash", "crash-start", "hang", "oversize", "malformed":
			default:
				return errors.New("invalid mock fault mode")
			}
			return harnessadapter.RunMock(
				command.Context(), command.InOrStdin(), command.OutOrStdout(),
				harnessadapter.MockConfig{Fault: fault},
			)
		},
	}
	command.Flags().StringVar(
		&fault, "fault", "",
		"inject gap, duplicate, crash, crash-start, hang, oversize, or malformed behavior",
	)
	return command
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
