package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
)

const maxThreadInputBytes = 128 << 10

func newThreadCommand(config *cliConfig) *cobra.Command {
	thread := &cobra.Command{Use: "thread", Short: "Manage encrypted structured agent Threads"}
	thread.AddCommand(
		newThreadSpawnCommand(config),
		newThreadCreateCommand(config), newThreadListCommand(config), newThreadGetCommand(config),
		newThreadSessionCommand(config, "start"), newThreadSessionCommand(config, "resume"),
		newThreadSendCommand(config), newThreadRespondCommand(config),
		newThreadSessionCommand(config, "cancel"), newThreadFollowCommand(config),
		newThreadArchiveCommand(config), newThreadDeleteCommand(config),
	)
	return thread
}

func newThreadSpawnCommand(config *cliConfig) *cobra.Command {
	var harness, prompt, name, key string
	var promptStdin, follow bool
	command := &cobra.Command{
		Use: "spawn PROJECT_ID", Short: "Provision a fresh Capsule and start its first Thread",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if harness == "" {
				return errors.New("--harness is required")
			}
			content, supplied, err := readOptionalThreadInput(
				command, config.stdin, prompt, promptStdin, "prompt")
			if err != nil {
				return err
			}
			if !supplied || content == "" {
				return errors.New("provide a non-empty --prompt or --prompt-stdin")
			}
			idempotency, err := idempotencyKey(key)
			if err != nil {
				return err
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			input := &client.CreateProjectThreadRequest{Harness: harness, Prompt: content}
			if name != "" {
				input.Name = client.NewOptString(name)
			}
			response, err := api.CreateProjectThread(
				command.Context(), input,
				client.CreateProjectThreadParams{
					ProjectId: args[0], IdempotencyKey: idempotency,
				},
			)
			if err != nil {
				return apiCallError(err)
			}
			success, ok := response.(*client.ProjectThreadIntentHeaders)
			if !ok {
				return responseError(response)
			}
			intent := success.Response
			if err := config.writeProjectThreadIntent(intent); err != nil {
				return err
			}
			if !follow {
				return nil
			}
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for intent.State == client.ProjectThreadIntentStateProvisioning {
				select {
				case <-command.Context().Done():
					return command.Context().Err()
				case <-ticker.C:
				}
				current, err := api.GetProjectThreadIntent(
					command.Context(),
					client.GetProjectThreadIntentParams{IntentId: intent.ID},
				)
				if err != nil {
					return apiCallError(err)
				}
				headers, ok := current.(*client.ProjectThreadIntentHeaders)
				if !ok {
					return responseError(current)
				}
				intent = headers.Response
			}
			if intent.State == client.ProjectThreadIntentStateFailed {
				message, _ := intent.FailureMessage.Get()
				if message == "" {
					message = "Project Thread provisioning failed"
				}
				return errors.New(message)
			}
			return followThread(command, config, intent.ThreadId, 0)
		},
	}
	command.Flags().StringVar(&harness, "harness", "", "structured harness profile name")
	command.Flags().StringVar(&prompt, "prompt", "", "first prompt (may be retained in shell history; prefer --prompt-stdin)")
	command.Flags().BoolVar(&promptStdin, "prompt-stdin", false, "read the first prompt from stdin")
	command.Flags().StringVar(&name, "name", "", "optional fresh Capsule name")
	command.Flags().BoolVar(&follow, "follow", false, "wait for provisioning and follow the Thread")
	command.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	return command
}

func newThreadCreateCommand(config *cliConfig) *cobra.Command {
	var capsuleID, harness, prompt, key string
	var promptStdin, start bool
	command := &cobra.Command{
		Use: "create", Short: "Create an encrypted structured Thread", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if capsuleID == "" || harness == "" {
				return errors.New("--capsule and --harness are required")
			}
			content, supplied, err := readOptionalThreadInput(
				command, config.stdin, prompt, promptStdin, "prompt")
			if err != nil {
				return err
			}
			if start && !supplied {
				return errors.New("--start requires --prompt or --prompt-stdin")
			}
			idempotency, err := idempotencyKey(key)
			if err != nil {
				return err
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			input := &client.CreateThreadRequest{Harness: harness, Start: client.NewOptBool(start)}
			if supplied {
				input.FirstMessage = client.NewOptString(content)
			}
			result, err := api.CreateThread(command.Context(), input, client.CreateThreadParams{
				CapsuleId: capsuleID, IdempotencyKey: idempotency,
			})
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.ThreadMutationResultHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeThreadMutation(success.Response)
		},
	}
	command.Flags().StringVar(&capsuleID, "capsule", "", "Capsule ID")
	command.Flags().StringVar(&harness, "harness", "", "structured harness profile name")
	command.Flags().StringVar(&prompt, "prompt", "", "first message (may be retained in shell history; prefer --prompt-stdin)")
	command.Flags().BoolVar(&promptStdin, "prompt-stdin", false, "read the first message from stdin")
	command.Flags().BoolVar(&start, "start", false, "atomically create the first Run intent and start the adapter")
	command.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	return command
}

func newThreadListCommand(config *cliConfig) *cobra.Command {
	var capsuleID, cursor string
	var limit int
	command := &cobra.Command{
		Use: "list", Short: "List a Capsule's retained Threads", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if capsuleID == "" {
				return errors.New("--capsule is required")
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			params := client.ListThreadsParams{
				CapsuleId: capsuleID, Limit: client.NewOptInt(limit),
			}
			if cursor != "" {
				params.Cursor = client.NewOptString(cursor)
			}
			result, err := api.ListThreads(command.Context(), params)
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.ThreadPage)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, success)
			}
			for _, item := range success.Items {
				if err := config.writeThread(item); err != nil {
					return err
				}
			}
			if next, ok := success.NextCursor.Get(); ok {
				_, err = fmt.Fprintf(config.stdout, "next-cursor=%s\n", next)
			}
			return err
		},
	}
	command.Flags().StringVar(&capsuleID, "capsule", "", "Capsule ID")
	command.Flags().StringVar(&cursor, "cursor", "", "pagination cursor")
	command.Flags().IntVar(&limit, "limit", 50, "page size")
	return command
}

func newThreadGetCommand(config *cliConfig) *cobra.Command {
	return &cobra.Command{
		Use: "get THREAD_ID", Short: "Get Thread lifecycle and status", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.GetThread(command.Context(), client.GetThreadParams{ThreadId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.ThreadHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeThread(success.Response)
		},
	}
}

func newThreadSessionCommand(config *cliConfig, operation string) *cobra.Command {
	var expected int64
	var key string
	command := &cobra.Command{
		Use: operation + " THREAD_ID", Short: strings.ToUpper(operation[:1]) + operation[1:] + " a Thread session",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if expected <= 0 {
				return errors.New("--expected-version must be greater than zero")
			}
			idempotency, err := idempotencyKey(key)
			if err != nil {
				return err
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			input := &client.LifecycleMutationRequest{ExpectedResourceVersion: expected}
			var result any
			switch operation {
			case "start":
				result, err = api.StartThread(command.Context(), input, client.StartThreadParams{
					ThreadId: args[0], IdempotencyKey: idempotency,
				})
			case "resume":
				result, err = api.ResumeThread(command.Context(), input, client.ResumeThreadParams{
					ThreadId: args[0], IdempotencyKey: idempotency,
				})
			case "cancel":
				result, err = api.CancelThread(command.Context(), input, client.CancelThreadParams{
					ThreadId: args[0], IdempotencyKey: idempotency,
				})
			}
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.ThreadSessionMutationHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeThreadMutation(success.Response)
		},
	}
	command.Flags().Int64Var(&expected, "expected-version", 0, "required current Thread resource version")
	command.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	return command
}

func newThreadSendCommand(config *cliConfig) *cobra.Command {
	var expected int64
	var prompt, key string
	var promptStdin bool
	command := &cobra.Command{
		Use: "send THREAD_ID", Short: "Durably append and send a user message", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if expected <= 0 {
				return errors.New("--expected-version must be greater than zero")
			}
			content, supplied, err := readOptionalThreadInput(
				command, config.stdin, prompt, promptStdin, "prompt")
			if err != nil {
				return err
			}
			if !supplied || content == "" {
				return errors.New("provide a non-empty --prompt or --prompt-stdin")
			}
			idempotency, err := idempotencyKey(key)
			if err != nil {
				return err
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.SendThreadMessage(command.Context(), &client.SendThreadMessageRequest{
				ExpectedResourceVersion: expected, Content: content,
			}, client.SendThreadMessageParams{ThreadId: args[0], IdempotencyKey: idempotency})
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.ThreadSessionMutationHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeThreadMutation(success.Response)
		},
	}
	command.Flags().Int64Var(&expected, "expected-version", 0, "required current Thread resource version")
	command.Flags().StringVar(&prompt, "prompt", "", "message (may be retained in shell history; prefer --prompt-stdin)")
	command.Flags().BoolVar(&promptStdin, "prompt-stdin", false, "read the message from stdin")
	command.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	return command
}

func newThreadRespondCommand(config *cliConfig) *cobra.Command {
	var expected int64
	var responseTo, choice, input, key string
	var inputStdin bool
	command := &cobra.Command{
		Use: "respond THREAD_ID", Short: "Answer a permission or input request", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if expected <= 0 || responseTo == "" {
				return errors.New("--expected-version and --response-to are required")
			}
			value, inputSupplied, err := readOptionalThreadInput(
				command, config.stdin, input, inputStdin, "input")
			if err != nil {
				return err
			}
			if (choice == "") == !inputSupplied {
				return errors.New("provide exactly one of --choice, --input, or --input-stdin")
			}
			idempotency, err := idempotencyKey(key)
			if err != nil {
				return err
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			request := &client.ThreadResponseRequest{
				ExpectedResourceVersion: expected, ResponseTo: responseTo,
			}
			if choice != "" {
				request.Choice = client.NewOptString(choice)
			} else {
				request.Input = client.NewOptString(value)
			}
			result, err := api.RespondThread(command.Context(), request, client.RespondThreadParams{
				ThreadId: args[0], IdempotencyKey: idempotency,
			})
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.ThreadSessionMutationHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeThreadMutation(success.Response)
		},
	}
	command.Flags().Int64Var(&expected, "expected-version", 0, "required current Thread resource version")
	command.Flags().StringVar(&responseTo, "response-to", "", "permission/input request ID")
	command.Flags().StringVar(&choice, "choice", "", "permission choice")
	command.Flags().StringVar(&input, "input", "", "input response (may be retained in shell history; prefer --input-stdin)")
	command.Flags().BoolVar(&inputStdin, "input-stdin", false, "read the input response from stdin")
	command.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	return command
}

func newThreadArchiveCommand(config *cliConfig) *cobra.Command {
	var expected int64
	var key string
	command := &cobra.Command{
		Use: "archive THREAD_ID", Short: "Archive a terminal Thread", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if expected <= 0 {
				return errors.New("--expected-version must be greater than zero")
			}
			idempotency, err := idempotencyKey(key)
			if err != nil {
				return err
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.ArchiveThread(command.Context(),
				&client.LifecycleMutationRequest{ExpectedResourceVersion: expected},
				client.ArchiveThreadParams{ThreadId: args[0], IdempotencyKey: idempotency})
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.ThreadMutationHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeThread(success.Response)
		},
	}
	command.Flags().Int64Var(&expected, "expected-version", 0, "required current Thread resource version")
	command.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	return command
}

func newThreadDeleteCommand(config *cliConfig) *cobra.Command {
	var expected int64
	var key string
	var confirm bool
	command := &cobra.Command{
		Use: "delete THREAD_ID", Short: "Permanently crypto-shred a Thread transcript", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if expected <= 0 {
				return errors.New("--expected-version must be greater than zero")
			}
			if !confirm {
				return errors.New("--confirm-crypto-shred is required; deletion is irreversible")
			}
			idempotency, err := idempotencyKey(key)
			if err != nil {
				return err
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.DeleteThread(command.Context(), &client.DeleteThreadRequest{
				ExpectedResourceVersion: expected,
				Confirmation:            client.DeleteThreadRequestConfirmationCryptoShred,
			}, client.DeleteThreadParams{ThreadId: args[0], IdempotencyKey: idempotency})
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.ThreadMutationHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeThread(success.Response)
		},
	}
	command.Flags().Int64Var(&expected, "expected-version", 0, "required current Thread resource version")
	command.Flags().BoolVar(&confirm, "confirm-crypto-shred", false, "confirm irreversible wrapped-key deletion")
	command.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	return command
}

func newThreadFollowCommand(config *cliConfig) *cobra.Command {
	var after int64
	command := &cobra.Command{
		Use: "follow THREAD_ID", Short: "Follow ordered decrypted Thread blocks", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return followThread(command, config, args[0], after)
		},
	}
	command.Flags().Int64Var(&after, "after", 0, "resume after durable message sequence")
	return command
}

func followThread(
	command *cobra.Command,
	config *cliConfig,
	threadID string,
	after int64,
) error {
	endpoint := strings.TrimRight(config.server, "/") + "/threads/" +
		url.PathEscape(threadID) + "/blocks/stream?after=" + strconv.FormatInt(after, 10)
	request, err := http.NewRequestWithContext(command.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	token, err := apiauth.ReadTokenFile(config.tokenFile)
	if err != nil {
		return fmt.Errorf("read API token: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "text/event-stream")
	httpClient := &http.Client{Timeout: 0}
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("follow Thread: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var envelope client.ErrorEnvelope
		if json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&envelope) == nil {
			return fmt.Errorf("%s: %s", envelope.Error.Code, envelope.Error.Message)
		}
		return fmt.Errorf("follow Thread returned %s", response.Status)
	}
	return streamThreadSSE(response.Body, config)
}

func streamThreadSSE(reader io.Reader, config *cliConfig) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var data bytes.Buffer
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data.Len() == 0 {
				continue
			}
			raw := bytes.TrimSuffix(data.Bytes(), []byte{'\n'})
			var block client.ThreadBlock
			if err := json.Unmarshal(raw, &block); err != nil {
				return fmt.Errorf("decode Thread stream event: %w", err)
			}
			if config.json {
				if err := writeJSON(config.stdout, &block); err != nil {
					return err
				}
			} else if content, ok := block.Content.Get(); ok {
				if _, err := fmt.Fprintln(config.stdout, content); err != nil {
					return err
				}
			} else if event, ok := block.Event.Get(); ok {
				if _, err := fmt.Fprintf(config.stdout, "%d\t%s\n",
					block.MessageSequence, event.Type); err != nil {
					return err
				}
			}
			data.Reset()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			data.WriteByte('\n')
		}
	}
	return scanner.Err()
}

func readOptionalThreadInput(
	command *cobra.Command, reader io.Reader, flagValue string, fromStdin bool, flagName string,
) (string, bool, error) {
	if fromStdin && command.Flags().Changed(flagName) {
		return "", false, fmt.Errorf("--%s and --%s-stdin are mutually exclusive", flagName, flagName)
	}
	if fromStdin {
		value, err := io.ReadAll(io.LimitReader(reader, maxThreadInputBytes+1))
		if err != nil {
			return "", false, fmt.Errorf("read %s from stdin: %w", flagName, err)
		}
		if len(value) > maxThreadInputBytes {
			return "", false, fmt.Errorf("%s exceeds size limit", flagName)
		}
		return string(value), true, nil
	}
	if command.Flags().Changed(flagName) {
		return flagValue, true, nil
	}
	return "", false, nil
}

func (c *cliConfig) writeThread(thread client.Thread) error {
	if c.json {
		return writeJSON(c.stdout, &thread)
	}
	_, err := fmt.Fprintf(c.stdout, "%s\t%s\tstate=%s\tmessages=%d\tversion=%d\n",
		thread.ID, thread.Harness, thread.State, thread.MessageCount, thread.ResourceVersion)
	return err
}

func (c *cliConfig) writeThreadMutation(result client.ThreadMutationResult) error {
	if c.json {
		return writeJSON(c.stdout, &result)
	}
	if err := c.writeThread(result.Thread); err != nil {
		return err
	}
	if run, ok := result.CurrentRun.Get(); ok {
		_, err := fmt.Fprintf(c.stdout, "run=%s\tstate=%s\n", run.ID, run.State)
		return err
	}
	return nil
}

func (c *cliConfig) writeProjectThreadIntent(intent client.ProjectThreadIntent) error {
	if c.json {
		return writeJSON(c.stdout, &intent)
	}
	_, err := fmt.Fprintf(
		c.stdout, "%s\tstate=%s\tcapsule=%s\tthread=%s\trun=%s\n",
		intent.ID, intent.State, intent.CapsuleId, intent.ThreadId, intent.RunId,
	)
	return err
}
