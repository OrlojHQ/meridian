package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/OrlojHQ/meridian/internal/ptyattach"
	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
)

func newRunCommand(config *cliConfig) *cobra.Command {
	run := &cobra.Command{Use: "run", Short: "Manage harness Runs"}
	run.AddCommand(
		newRunStartCommand(config), newRunGetCommand(config), newRunListCommand(config),
		newRunCancelCommand(config), newRunEventsCommand(config), newRunAttachCommand(config),
	)
	return run
}

func newRunStartCommand(config *cliConfig) *cobra.Command {
	var capsuleID, harnessName, prompt, key string
	var promptStdin bool
	command := &cobra.Command{
		Use:   "start",
		Short: "Start a harness Run",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if capsuleID == "" || harnessName == "" {
				return errors.New("--capsule and --harness are required")
			}
			if promptStdin {
				if command.Flags().Changed("prompt") {
					return errors.New("--prompt and --prompt-stdin are mutually exclusive")
				}
				value, err := io.ReadAll(io.LimitReader(config.stdin, (256<<10)+1))
				if err != nil {
					return errors.New("read prompt from stdin")
				}
				if len(value) > 256<<10 {
					return errors.New("prompt exceeds size limit")
				}
				prompt = string(value)
			} else if !command.Flags().Changed("prompt") {
				return errors.New("provide --prompt or --prompt-stdin")
			}
			idempotency, err := idempotencyKey(key)
			if err != nil {
				return err
			}
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			result, err := api.StartRun(command.Context(), &client.StartRunRequest{
				Harness: harnessName, Prompt: prompt,
			}, client.StartRunParams{CapsuleId: capsuleID, IdempotencyKey: idempotency})
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.RunHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeRun(success.Response)
		},
	}
	command.Flags().StringVar(&capsuleID, "capsule", "", "Capsule ID")
	command.Flags().StringVar(&harnessName, "harness", "", "harness profile name")
	command.Flags().StringVar(&prompt, "prompt", "", "transient Run prompt")
	command.Flags().BoolVar(&promptStdin, "prompt-stdin", false, "read transient prompt from stdin")
	command.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	return command
}

func newRunGetCommand(config *cliConfig) *cobra.Command {
	return &cobra.Command{
		Use: "get RUN_ID", Short: "Get a Run", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			result, err := api.GetRun(command.Context(), client.GetRunParams{RunId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.RunHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeRun(success.Response)
		},
	}
}

func newRunListCommand(config *cliConfig) *cobra.Command {
	var capsuleID, cursor string
	var limit int
	command := &cobra.Command{
		Use: "list", Short: "List a Capsule's Runs", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if capsuleID == "" {
				return errors.New("--capsule is required")
			}
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			params := client.ListRunsParams{CapsuleId: capsuleID, Limit: client.NewOptInt(limit)}
			if cursor != "" {
				params.Cursor = client.NewOptString(cursor)
			}
			result, err := api.ListRuns(command.Context(), params)
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.RunPage)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, success)
			}
			for _, item := range success.Items {
				if err := config.writeRun(item); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&capsuleID, "capsule", "", "Capsule ID")
	command.Flags().StringVar(&cursor, "cursor", "", "pagination cursor")
	command.Flags().IntVar(&limit, "limit", 50, "page size")
	return command
}

func newRunCancelCommand(config *cliConfig) *cobra.Command {
	var expected int64
	var key string
	command := &cobra.Command{
		Use: "cancel RUN_ID", Short: "Cancel a Run", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if expected <= 0 {
				return errors.New("--expected-version must be greater than zero")
			}
			idempotency, err := idempotencyKey(key)
			if err != nil {
				return err
			}
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			result, err := api.CancelRun(command.Context(),
				&client.LifecycleMutationRequest{ExpectedResourceVersion: expected},
				client.CancelRunParams{RunId: args[0], IdempotencyKey: idempotency})
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.RunHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeRun(success.Response)
		},
	}
	command.Flags().Int64Var(&expected, "expected-version", 0, "required current resource version")
	command.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	return command
}

func newRunEventsCommand(config *cliConfig) *cobra.Command {
	var after int64
	var limit int
	command := &cobra.Command{
		Use: "events RUN_ID", Short: "Replay ordered Run events", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			result, err := api.ListRunEvents(command.Context(), client.ListRunEventsParams{
				RunId: args[0], After: client.NewOptInt64(after), Limit: client.NewOptInt(limit),
			})
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.RunEventPage)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, success)
			}
			for _, event := range success.Items {
				if _, err := fmt.Fprintf(config.stdout, "%d\t%s\t%s\n",
					event.Sequence, event.Timestamp.Format("2006-01-02T15:04:05.999999999Z07:00"), event.Type); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().Int64Var(&after, "after", 0, "stable event sequence cursor")
	command.Flags().IntVar(&limit, "limit", 100, "event page size")
	return command
}

func newCapsuleGitCommand(config *cliConfig, diff bool) *cobra.Command {
	name := "git-status"
	if diff {
		name = "diff"
	}
	return &cobra.Command{
		Use: name + " CAPSULE_ID", Short: "Get Capsule Git " + strings.TrimPrefix(name, "git-"),
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			var result any
			if diff {
				result, err = api.GetCapsuleGitDiff(
					command.Context(), client.GetCapsuleGitDiffParams{CapsuleId: args[0]})
			} else {
				result, err = api.GetCapsuleGitStatus(
					command.Context(), client.GetCapsuleGitStatusParams{CapsuleId: args[0]})
			}
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.GitResult)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, success)
			}
			_, err = io.WriteString(config.stdout, success.Content)
			return err
		},
	}
}

func newRunAttachCommand(config *cliConfig) *cobra.Command {
	var cursor uint64
	command := &cobra.Command{
		Use: "attach RUN_ID", Short: "Attach to a Run PTY", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if config.json {
				return errors.New("--json is not supported for PTY attachment")
			}
			input, inputOK := config.stdin.(*os.File)
			output, outputOK := config.stdout.(*os.File)
			if !inputOK || !outputOK {
				return errors.New("PTY attachment requires terminal stdin and stdout")
			}
			return ptyattach.Run(command.Context(), ptyattach.Options{
				Server: config.server, RunID: args[0], After: cursor,
				Stdin: input, Stdout: output, Stderr: command.ErrOrStderr(),
			})
		},
	}
	command.Flags().Uint64Var(&cursor, "after", 0, "replay output after cursor")
	return command
}

func (c *cliConfig) writeRun(run client.Run) error {
	if c.json {
		return writeJSON(c.stdout, run)
	}
	_, err := fmt.Fprintf(c.stdout, "%s\t%s\tstate=%s\tversion=%d\n",
		run.ID, run.Harness, run.State, run.ResourceVersion)
	return err
}
