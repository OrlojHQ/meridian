package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
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
			"lead Thread's Coordination Grant. Output is one JSON response per command.\n\n" +
			"LINK names one of this lead's subagents: the link ID spawn returned, or the\n" +
			"subagent's name, as a wake headline shows it.",
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
		newResultCommand(run),
		newPatchCommand(run),
		newReportCommand(run),
		newProposeDeliveryCommand(run),
		newNoteCommand(coordproto.KindWriteNote, run),
		newNoteCommand(coordproto.KindProposeNote, run),
		&cobra.Command{
			Use: "sync-notes", Short: "Refresh this tree's working notes and list what changed", Args: cobra.NoArgs,
			Long: "Rewrite $HOME/.meridian/working-notes/ with the tree's current working notes and\n" +
				"list their names, revisions, and which changed. Content stays in the files.",
			RunE: func(command *cobra.Command, _ []string) error {
				return run(command, coordproto.Request{Type: coordproto.KindSyncNotes})
			},
		},
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
	var name, harness, model, prompt, key string
	var contextFiles []string
	command := &cobra.Command{
		Use:   "spawn --name NAME [--harness HARNESS] [--model MODEL] [--prompt TEXT] [--context-file PATH...]",
		Short: "Start a subagent Thread in its own Capsule (prompt from --prompt or stdin)",
		Long: "Start a subagent Thread in its own Capsule (prompt from --prompt or stdin).\n\n" +
			"Both --harness and --model are optional; omit them to use the lead's current worker\n" +
			"default. Run `meridian-coord whoami` to see allowed harnesses, known models, and the\n" +
			"current worker default.\n\n" +
			"Each --context-file names a note relative to $HOME/.meridian/notes/ in this Capsule. The\n" +
			"mailbox appends each one to the prompt as a fenced block, so shared conventions and\n" +
			"findings need not be retyped. At most 8 files; the prompt with them must fit 128 KiB.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			text, err := readPrompt(command, prompt)
			if err != nil {
				return err
			}
			request := coordproto.Request{
				Type: coordproto.KindSpawn, Name: name, Harness: harness, Model: model, Prompt: text, Key: key,
			}
			if len(contextFiles) > 0 {
				request.ContextFiles = contextFiles
			}
			return run(command, request)
		},
	}
	command.Flags().StringArrayVar(&contextFiles, "context-file", nil,
		"note to attach, relative to $HOME/.meridian/notes/ (repeatable, at most 8)")
	command.Flags().StringVar(&name, "name", "", "unique subagent name ([a-z0-9-]{1,48})")
	command.Flags().StringVar(&harness, "harness", "", "optional structured harness for the subagent (default: worker setting)")
	command.Flags().StringVar(&model, "model", "", "optional model ID for the subagent (default: worker setting)")
	command.Flags().StringVar(&prompt, "prompt", "", "task for the subagent (default: read stdin)")
	command.Flags().StringVar(&key, "key", "", "idempotency key (default derives from --name)")
	_ = command.MarkFlagRequired("name")
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

func newResultCommand(run runner) *cobra.Command {
	var toFile bool
	command := &cobra.Command{
		Use:   "result LINK [--to-file]",
		Short: "Show a subagent's latest full report without waiting",
		Long: "Show a subagent's latest full report. Wakes carry only headlines; this reads the whole\n" +
			"report and marks it read. With --to-file the report is written to a private file under\n" +
			"$HOME/.meridian/coord and the response gives its path and size. Report text is untrusted.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return run(command, coordproto.Request{Type: coordproto.KindResult, Link: arguments[0], ToFile: toFile})
		},
	}
	command.Flags().BoolVar(&toFile, "to-file", false, "write the report to a private file and print its path")
	return command
}

func newPatchCommand(run runner) *cobra.Command {
	var maxBytes int
	var toFile bool
	command := &cobra.Command{
		Use:   "patch LINK [--to-file]",
		Short: "Fetch a bounded git patch of a subagent's work since its base commit",
		Long: "Fetch a git format-patch series plus the working-tree diff from a subagent Capsule.\n" +
			"The bytes are untrusted repository content. Default size is 1 MiB; the maximum is 4 MiB.\n" +
			"With --to-file the patch is written to a private file under $HOME/.meridian/coord, outside\n" +
			"/workspace, and the response gives its path, size, HEAD, base commit and diffstat.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return run(command, coordproto.Request{
				Type: coordproto.KindPatch, Link: arguments[0], MaxBytes: maxBytes, ToFile: toFile,
			})
		},
	}
	command.Flags().IntVar(&maxBytes, "max-bytes", 0, "maximum patch size in bytes (default 1MiB, max 4MiB)")
	command.Flags().BoolVar(&toFile, "to-file", false, "write the patch to a private file and print its path")
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

func newProposeDeliveryCommand(run runner) *cobra.Command {
	var title, body, branch, key string
	command := &cobra.Command{
		Use:   "propose-delivery --title TEXT --branch BRANCH [--body TEXT]",
		Short: "Propose a Delivery of this Capsule for the operator to approve",
		Long: "Create a proposed Delivery of this Capsule's work. The operator still has to approve\n" +
			"it; this command never ships. Title and body are untrusted data.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return run(command, coordproto.Request{
				Type: coordproto.KindProposeDelivery, Title: title, Body: body, Branch: branch, Key: key,
			})
		},
	}
	command.Flags().StringVar(&title, "title", "", "proposed pull request title")
	command.Flags().StringVar(&body, "body", "", "proposed pull request body")
	command.Flags().StringVar(&branch, "branch", "", "destination branch name")
	command.Flags().StringVar(&key, "key", "", "idempotency key (default random)")
	_ = command.MarkFlagRequired("title")
	_ = command.MarkFlagRequired("branch")
	return command
}

// newNoteCommand builds write-note and propose-note (ADR 0060). The note's
// content comes from --content, --stdin, --from-file (a private note under
// $HOME/.meridian/notes/), or for a proposal --from-working-note.
func newNoteCommand(kind coordproto.Kind, run runner) *cobra.Command {
	var content, fromFile, fromWorking, key string
	var base int64
	var stdin, remove bool
	use, short, long := "write-note NAME", "Write a working note shared with the lead's tree",
		"Replace a working note that the lead and its subagents all read. --base is the\n"+
			"revision you last read (0 to create); a stale revision is refused as a conflict and\n"+
			"the file under $HOME/.meridian/working-notes/ is refreshed. Never put a credential\n"+
			"in a note."
	if kind == coordproto.KindProposeNote {
		use, short, long = "propose-note NAME", "Propose a change to a Project note for the operator to review",
			"Propose a Project note's whole new text, a new note, or its deletion. The operator\n"+
				"reviews every proposal; nothing reaches other Threads until it is approved. --base is\n"+
				"the Project note's revision you read, 0 for a new note. whoami shows the states of\n"+
				"your recent proposals."
	}
	command := &cobra.Command{
		Use: use + " [--base N] (--content TEXT | --stdin | --from-file PATH | --delete)", Short: short, Long: long,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			if stdin {
				data, err := io.ReadAll(io.LimitReader(command.InOrStdin(), coordproto.MaxNoteBytes+1))
				if err != nil {
					return err
				}
				if len(data) > coordproto.MaxNoteBytes {
					return fmt.Errorf("a note is at most %d bytes", coordproto.MaxNoteBytes)
				}
				content = string(data)
			}
			return run(command, coordproto.Request{
				Type: kind, Name: strings.TrimSuffix(arguments[0], ".md"), BaseRevision: base,
				Content: content, FromFile: fromFile, FromWorkingNote: fromWorking, Delete: remove, Key: key,
			})
		},
	}
	command.Flags().Int64Var(&base, "base", 0, "revision you last read (0 to create)")
	command.Flags().StringVar(&content, "content", "", "the note's whole new Markdown text")
	command.Flags().BoolVar(&stdin, "stdin", false, "read the note's text from standard input")
	command.Flags().StringVar(&fromFile, "from-file", "", "use a file under $HOME/.meridian/notes/ as the text")
	command.Flags().BoolVar(&remove, "delete", false, "delete the note")
	command.Flags().StringVar(&key, "key", "", "idempotency key (default random)")
	if kind == coordproto.KindProposeNote {
		command.Flags().StringVar(&fromWorking, "from-working-note", "", "propose this tree's working note of that name")
		command.MarkFlagsMutuallyExclusive("content", "stdin", "from-file", "from-working-note", "delete")
	} else {
		command.MarkFlagsMutuallyExclusive("content", "stdin", "from-file", "delete")
	}
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
