package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/OrlojHQ/meridian/internal/buildinfo"
	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/coordtool"
	"github.com/spf13/cobra"
)

// errRejected marks a well-formed response that reported failure; the JSON
// response on stdout already explains it.
var errRejected = errors.New("coordination request failed")

func newRootCommand() *cobra.Command {
	var socket string
	command := &cobra.Command{
		Use:   "meridian-coord",
		Short: "Coordinate subagent Threads from a Meridian lead Thread",
		Long: "Coordinate subagent Threads from inside a Meridian Capsule.\n\n" +
			"Requests go to the Capsule-local mailbox; meridiand pulls them and enforces the\n" +
			"lead Thread's Coordination Grant. Output is one JSON response per command.",
		Version:       buildinfo.Current().String(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	command.SetVersionTemplate("meridian-coord {{.Version}}\n")
	command.PersistentFlags().StringVar(&socket, "socket", "", "mailbox socket (default $"+coordproto.SocketEnv+")")
	resolve := func() string {
		if socket != "" {
			return socket
		}
		return coordproto.DefaultSocketPath()
	}
	run := func(command *cobra.Command, request coordproto.Request) error {
		response, err := coordtool.Call(command.Context(), resolve(), request)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(command.OutOrStdout())
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(response); err != nil {
			return err
		}
		if !response.OK {
			return errRejected
		}
		return nil
	}
	command.AddCommand(
		newSpawnCommand(run),
		newSendCommand(run),
		newWaitCommand(run),
		newLinkCommand("cancel", "Cancel a subagent and pause its Capsule", coordproto.KindCancel, true, run),
		newLinkCommand("status", "Show a subagent's content-free status", coordproto.KindStatus, false, run),
		newLinkCommand("result", "Show a subagent's latest report without waiting", coordproto.KindResult, false, run),
		newPatchCommand(run),
		newReportCommand(run),
		&cobra.Command{
			Use: "list", Short: "List subagents", Args: cobra.NoArgs,
			RunE: func(command *cobra.Command, _ []string) error {
				return run(command, coordproto.Request{Type: coordproto.KindList})
			},
		},
		&cobra.Command{
			Use: "whoami", Short: "Show coordination limits and allowed harnesses", Args: cobra.NoArgs,
			RunE: func(command *cobra.Command, _ []string) error {
				return run(command, coordproto.Request{Type: coordproto.KindWhoAmI})
			},
		},
		&cobra.Command{
			Use:   "mcp",
			Short: "Serve the coordination tools as a stdio MCP server",
			Args:  cobra.NoArgs,
			RunE: func(command *cobra.Command, _ []string) error {
				server := &coordtool.MCPServer{Socket: resolve(), Version: buildinfo.Current().Version}
				return server.Serve(command.Context(), command.InOrStdin(), command.OutOrStdout())
			},
		},
	)
	return command
}

type runner func(*cobra.Command, coordproto.Request) error

func readPrompt(command *cobra.Command, flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	data, err := io.ReadAll(io.LimitReader(command.InOrStdin(), coordproto.MaxPromptBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > coordproto.MaxPromptBytes {
		return "", fmt.Errorf("prompt exceeds %d bytes", coordproto.MaxPromptBytes)
	}
	return string(data), nil
}

func newSpawnCommand(run runner) *cobra.Command {
	var name, harness, prompt, key string
	command := &cobra.Command{
		Use:   "spawn --name NAME --harness HARNESS [--prompt TEXT]",
		Short: "Start a subagent Thread in its own Capsule (prompt from --prompt or stdin)",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			text, err := readPrompt(command, prompt)
			if err != nil {
				return err
			}
			return run(command, coordproto.Request{
				Type: coordproto.KindSpawn, Name: name, Harness: harness, Prompt: text, Key: key,
			})
		},
	}
	command.Flags().StringVar(&name, "name", "", "unique subagent name ([a-z0-9-]{1,48})")
	command.Flags().StringVar(&harness, "harness", "", "structured harness for the subagent")
	command.Flags().StringVar(&prompt, "prompt", "", "task for the subagent (default: read stdin)")
	command.Flags().StringVar(&key, "key", "", "idempotency key (default derives from --name)")
	_ = command.MarkFlagRequired("name")
	_ = command.MarkFlagRequired("harness")
	return command
}

func newSendCommand(run runner) *cobra.Command {
	var prompt, key string
	command := &cobra.Command{
		Use:   "send LINK [--prompt TEXT]",
		Short: "Send a follow-up message to a subagent (prompt from --prompt or stdin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			text, err := readPrompt(command, prompt)
			if err != nil {
				return err
			}
			return run(command, coordproto.Request{
				Type: coordproto.KindSend, Link: arguments[0], Prompt: text, Key: key,
			})
		},
	}
	command.Flags().StringVar(&prompt, "prompt", "", "message (default: read stdin)")
	command.Flags().StringVar(&key, "key", "", "idempotency key (default random)")
	return command
}

func newWaitCommand(run runner) *cobra.Command {
	var all bool
	var timeout time.Duration
	command := &cobra.Command{
		Use:   "wait [LINK...]",
		Short: "Wait for subagent reports",
		Args:  cobra.MaximumNArgs(coordproto.MaxWaitLinks),
		RunE: func(command *cobra.Command, arguments []string) error {
			seconds := int(timeout / time.Second)
			if seconds < 1 || seconds > coordproto.MaxWaitSeconds {
				return fmt.Errorf("--timeout must be between 1s and %ds", coordproto.MaxWaitSeconds)
			}
			request := coordproto.Request{Type: coordproto.KindWait, All: all, TimeoutSeconds: seconds}
			if len(arguments) > 0 {
				request.Links = arguments
			}
			return run(command, request)
		},
	}
	command.Flags().BoolVar(&all, "all", false, "wait until every listed subagent has reported or closed")
	command.Flags().DurationVar(&timeout, "timeout", coordproto.DefaultWaitSeconds*time.Second, "maximum wait")
	return command
}

func newPatchCommand(run runner) *cobra.Command {
	var maxBytes int
	command := &cobra.Command{
		Use:   "patch LINK",
		Short: "Fetch a bounded git patch of a subagent's work since its base commit",
		Long: "Fetch a git format-patch series plus the working-tree diff from a subagent Capsule.\n" +
			"The bytes are untrusted repository content. Default size is 1 MiB; the maximum is 4 MiB.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return run(command, coordproto.Request{
				Type: coordproto.KindPatch, Link: arguments[0], MaxBytes: maxBytes,
			})
		},
	}
	command.Flags().IntVar(&maxBytes, "max-bytes", 0, "maximum patch size in bytes (default 1MiB, max 4MiB)")
	return command
}

func newReportCommand(run runner) *cobra.Command {
	var status, summary, key string
	command := &cobra.Command{
		Use:   "report --status done|blocked|failed [--summary TEXT]",
		Short: "Send this subagent's report to its lead (summary from --summary or stdin)",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			text := summary
			if text == "" {
				var err error
				text, err = readPrompt(command, "")
				if err != nil {
					return err
				}
			}
			if len(text) > coordproto.MaxReportSummaryBytes {
				return fmt.Errorf("summary exceeds %d bytes", coordproto.MaxReportSummaryBytes)
			}
			return run(command, coordproto.Request{
				Type: coordproto.KindReport, Status: status, Summary: text, Key: key,
			})
		},
	}
	command.Flags().StringVar(&status, "status", "", "done, blocked, or failed")
	command.Flags().StringVar(&summary, "summary", "", "bounded report text (default: read stdin)")
	command.Flags().StringVar(&key, "key", "", "idempotency key (default random)")
	_ = command.MarkFlagRequired("status")
	return command
}

func newLinkCommand(use, short string, kind coordproto.Kind, keyed bool, run runner) *cobra.Command {
	var key string
	command := &cobra.Command{
		Use:   use + " LINK",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return run(command, coordproto.Request{Type: kind, Link: arguments[0], Key: key})
		},
	}
	if keyed {
		command.Flags().StringVar(&key, "key", "", "idempotency key")
	}
	return command
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	command := newRootCommand()
	command.SetContext(ctx)
	if err := command.Execute(); err != nil {
		if errors.Is(err, errRejected) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
