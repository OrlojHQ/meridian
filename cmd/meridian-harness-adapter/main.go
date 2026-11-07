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
		newACPCommand(),
		newClaudeCommand(),
		newCodexCommand(),
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

func parseMCPServers(values []string) ([]harnessadapter.MCPServer, error) {
	servers := make([]harnessadapter.MCPServer, 0, len(values))
	for _, value := range values {
		server, err := harnessadapter.ParseMCPServer(value)
		if err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}
	return servers, nil
}

const mcpServerUsage = "register a stdio MCP server as NAME=/absolute/executable[,argument...]; repeatable"

func newClaudeCommand() *cobra.Command {
	var permissions bool
	var mcpServers []string
	command := &cobra.Command{
		Use:   "claude-stream -- <claude-executable> [arguments...]",
		Short: "Bridge Claude Code stream-json mode to Meridian",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			servers, err := parseMCPServers(mcpServers)
			if err != nil {
				return err
			}
			return harnessadapter.RunClaudeStream(
				command.Context(), command.InOrStdin(), command.OutOrStdout(),
				harnessadapter.ClaudeConfig{
					Command: arguments, Permissions: permissions, MCPServers: servers,
				},
			)
		},
	}
	command.Flags().BoolVar(
		&permissions, "permissions", true,
		"route Claude Code tool permission prompts to Meridian instead of disabling them",
	)
	command.Flags().StringArrayVar(&mcpServers, "mcp-server", nil, mcpServerUsage)
	return command
}

func newCodexCommand() *cobra.Command {
	var approvalPolicy, sandbox string
	var mcpServers []string
	command := &cobra.Command{
		Use:   "codex-app-server -- <codex-executable> [arguments...]",
		Short: "Bridge the Codex app-server protocol to Meridian",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			servers, err := parseMCPServers(mcpServers)
			if err != nil {
				return err
			}
			return harnessadapter.RunCodexAppServer(
				command.Context(), command.InOrStdin(), command.OutOrStdout(),
				harnessadapter.CodexConfig{
					Command: arguments, ApprovalPolicy: approvalPolicy, Sandbox: sandbox,
					MCPServers: servers,
				},
			)
		},
	}
	command.Flags().StringVar(
		&approvalPolicy, "approval-policy", "",
		"Codex approval policy: untrusted, on-request, or never (default: Codex configuration)",
	)
	command.Flags().StringVar(
		&sandbox, "sandbox", "",
		"Codex sandbox: read-only, workspace-write, or danger-full-access (default: Codex configuration)",
	)
	command.Flags().StringArrayVar(&mcpServers, "mcp-server", nil, mcpServerUsage)
	return command
}

func newACPCommand() *cobra.Command {
	var permissions bool
	var mcpServers, secretEnv []string
	command := &cobra.Command{
		Use:   "acp -- <agent-executable> [arguments...]",
		Short: "Bridge an Agent Client Protocol agent over stdio to Meridian",
		Long: "Launch a command that speaks the Agent Client Protocol (ACP) version 1 over stdio and map\n" +
			"its sessions, updates, tool calls, plans, permission requests, and commands to Meridian frames.\n" +
			"The client offers the agent no file system, terminal, or authentication capability.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			servers, err := parseMCPServers(mcpServers)
			if err != nil {
				return err
			}
			return harnessadapter.RunACP(
				command.Context(), command.InOrStdin(), command.OutOrStdout(),
				harnessadapter.ACPConfig{
					Command: arguments, Permissions: permissions, MCPServers: servers,
					SecretEnv: secretEnv,
				},
			)
		},
	}
	command.Flags().BoolVar(
		&permissions, "permissions", true,
		"route ACP permission requests to Meridian instead of rejecting them",
	)
	command.Flags().StringArrayVar(&mcpServers, "mcp-server", nil, mcpServerUsage)
	command.Flags().StringArrayVar(
		&secretEnv, "secret-env", nil,
		"set this environment variable to a fresh random value for each launch; repeatable",
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
