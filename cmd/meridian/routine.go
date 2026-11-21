package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
)

// Routines (ADR 0057).

type routineFlags struct {
	name, harness, model, effort, prompt, promptFile, cadence, timeZone string
	maxRun, catchUp                                                     time.Duration
	maxKept                                                             int
	enabled, includeNotes                                               bool
}

func (f *routineFlags) bind(command *cobra.Command, create bool) {
	flags := command.Flags()
	flags.StringVar(&f.name, "name", "", "routine name: lowercase letters, digits, and dashes")
	flags.StringVar(&f.harness, "harness", "", "structured harness on the Project allowlist")
	flags.StringVar(&f.model, "model", "", "model option ID the harness reports (empty uses its default)")
	flags.StringVar(&f.effort, "effort", "", "reasoning effort option ID (empty uses the default)")
	flags.StringVar(&f.prompt, "prompt", "", "prompt the run starts with")
	flags.StringVar(&f.promptFile, "prompt-file", "", "read the prompt from this file, or - for stdin")
	flags.StringVar(&f.cadence, "cron", "", `five-field cron expression, for example "0 6 * * 1"`)
	flags.StringVar(&f.timeZone, "time-zone", "", "IANA time zone (default: this machine's zone)")
	flags.DurationVar(&f.maxRun, "max-run", 2*time.Hour, "stop a run after this long (5m to 24h)")
	flags.DurationVar(&f.catchUp, "catch-up", time.Hour, "after downtime, run a missed fire this late at most (0 to 24h)")
	flags.IntVar(&f.maxKept, "max-kept", 5, "stop firing while this many runs with results wait for you (1 to 20)")
	flags.BoolVar(&f.enabled, "enabled", true, "fire on the clock")
	flags.BoolVar(&f.includeNotes, "include-notes", false, "give each scheduled Thread the Project's notes as files")
	if create {
		_ = command.MarkFlagRequired("name")
		_ = command.MarkFlagRequired("harness")
		_ = command.MarkFlagRequired("cron")
	}
}

func (f *routineFlags) readPrompt(stdin io.Reader) (string, error) {
	if f.promptFile == "" {
		return f.prompt, nil
	}
	if f.prompt != "" {
		return "", fmt.Errorf("use --prompt or --prompt-file, not both")
	}
	var raw []byte
	var err error
	if f.promptFile == "-" {
		raw, err = io.ReadAll(io.LimitReader(stdin, 32<<10+1))
	} else {
		raw, err = os.ReadFile(f.promptFile)
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func localTimeZone() string {
	if zone := os.Getenv("TZ"); zone != "" {
		return zone
	}
	if name := time.Local.String(); name != "" && name != "Local" {
		return name
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, zone, ok := strings.Cut(target, "zoneinfo/"); ok {
			return zone
		}
	}
	return "UTC"
}

func newRoutineCommand(config *cliConfig) *cobra.Command {
	routine := &cobra.Command{
		Use:   "routine",
		Short: "Start Project Threads on a schedule",
		Long: "A routine starts one fresh Capsule and Thread with a fixed prompt on a cron clock.\n" +
			"A run with no changes deletes its Capsule. A run with changes, a question, or a\n" +
			"failure is kept for you. A routine never ships: Delivery still needs your approval.",
	}
	list := &cobra.Command{
		Use:   "list [PROJECT_ID]",
		Short: "List your routines, or one Project's",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			var page *client.RoutineList
			var ok bool
			if len(args) == 0 {
				result, err := api.ListRoutines(command.Context())
				if err != nil {
					return apiCallError(err)
				}
				if page, ok = result.(*client.RoutineList); !ok {
					return responseError(result)
				}
			} else {
				result, err := api.ListProjectRoutines(command.Context(), client.ListProjectRoutinesParams{ProjectId: args[0]})
				if err != nil {
					return apiCallError(err)
				}
				if page, ok = result.(*client.RoutineList); !ok {
					return responseError(result)
				}
			}
			if config.json {
				return writeJSON(config.stdout, page)
			}
			for _, item := range page.Items {
				if err := writeRoutineLine(config.stdout, item); err != nil {
					return err
				}
			}
			return nil
		},
	}
	get := &cobra.Command{
		Use:   "get ROUTINE_ID",
		Short: "Show a routine, prompt included",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.GetRoutine(command.Context(), client.GetRoutineParams{RoutineId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			item, ok := result.(*client.Routine)
			if !ok {
				return responseError(result)
			}
			return writeRoutine(config, item)
		},
	}
	var createFlags routineFlags
	create := &cobra.Command{
		Use:   "create PROJECT_ID",
		Short: "Save a routine on a Project",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			prompt, err := createFlags.readPrompt(config.stdin)
			if err != nil {
				return err
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			key, err := idempotencyKey("")
			if err != nil {
				return err
			}
			zone := createFlags.timeZone
			if zone == "" {
				zone = localTimeZone()
			}
			request := &client.CreateRoutineRequest{
				Name: createFlags.name, Harness: createFlags.harness, Prompt: prompt,
				Cadence: createFlags.cadence, TimeZone: zone,
				MaxRunSeconds:  client.NewOptInt(int(createFlags.maxRun / time.Second)),
				CatchUpSeconds: client.NewOptInt(int(createFlags.catchUp / time.Second)),
				MaxKept:        client.NewOptInt(createFlags.maxKept),
				Enabled:        client.NewOptBool(createFlags.enabled),
				IncludeNotes:   client.NewOptBool(createFlags.includeNotes),
			}
			if createFlags.model != "" {
				request.Model = client.NewOptString(createFlags.model)
			}
			if createFlags.effort != "" {
				request.Effort = client.NewOptString(createFlags.effort)
			}
			result, err := api.CreateProjectRoutine(command.Context(), request,
				client.CreateProjectRoutineParams{ProjectId: args[0], IdempotencyKey: key})
			if err != nil {
				return apiCallError(err)
			}
			item, ok := result.(*client.Routine)
			if !ok {
				return responseError(result)
			}
			return writeRoutine(config, item)
		},
	}
	createFlags.bind(create, true)
	var updateFlags routineFlags
	update := &cobra.Command{
		Use:   "update ROUTINE_ID",
		Short: "Change a routine; only the flags you pass change",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return changeRoutine(command, config, args[0], func(request *client.UpdateRoutineRequest) error {
				flags := command.Flags()
				setString := func(name string, value string, dst *client.OptString) {
					if flags.Changed(name) {
						*dst = client.NewOptString(value)
					}
				}
				setString("name", updateFlags.name, &request.Name)
				setString("harness", updateFlags.harness, &request.Harness)
				setString("model", updateFlags.model, &request.Model)
				setString("effort", updateFlags.effort, &request.Effort)
				setString("cron", updateFlags.cadence, &request.Cadence)
				setString("time-zone", updateFlags.timeZone, &request.TimeZone)
				if flags.Changed("prompt") || flags.Changed("prompt-file") {
					prompt, err := updateFlags.readPrompt(config.stdin)
					if err != nil {
						return err
					}
					request.Prompt = client.NewOptString(prompt)
				}
				if flags.Changed("max-run") {
					request.MaxRunSeconds = client.NewOptInt(int(updateFlags.maxRun / time.Second))
				}
				if flags.Changed("catch-up") {
					request.CatchUpSeconds = client.NewOptInt(int(updateFlags.catchUp / time.Second))
				}
				if flags.Changed("max-kept") {
					request.MaxKept = client.NewOptInt(updateFlags.maxKept)
				}
				if flags.Changed("enabled") {
					request.Enabled = client.NewOptBool(updateFlags.enabled)
				}
				if flags.Changed("include-notes") {
					request.IncludeNotes = client.NewOptBool(updateFlags.includeNotes)
				}
				return nil
			})
		},
	}
	updateFlags.bind(update, false)
	toggle := func(use, short string, enabled bool) *cobra.Command {
		return &cobra.Command{
			Use: use + " ROUTINE_ID", Short: short, Args: cobra.ExactArgs(1),
			RunE: func(command *cobra.Command, args []string) error {
				return changeRoutine(command, config, args[0], func(request *client.UpdateRoutineRequest) error {
					request.Enabled = client.NewOptBool(enabled)
					return nil
				})
			},
		}
	}
	run := &cobra.Command{
		Use:   "run ROUTINE_ID",
		Short: "Fire a routine now",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			key, err := idempotencyKey("")
			if err != nil {
				return err
			}
			result, err := api.RunRoutine(command.Context(), client.RunRoutineParams{RoutineId: args[0], IdempotencyKey: key})
			if err != nil {
				return apiCallError(err)
			}
			fire, ok := result.(*client.RoutineFire)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, fire)
			}
			return writeFireLine(config.stdout, *fire)
		},
	}
	fires := &cobra.Command{
		Use:   "fires ROUTINE_ID",
		Short: "List a routine's recent fires",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.ListRoutineFires(command.Context(), client.ListRoutineFiresParams{RoutineId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			page, ok := result.(*client.RoutineFirePage)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, page)
			}
			for _, item := range page.Items {
				if err := writeFireLine(config.stdout, item); err != nil {
					return err
				}
			}
			return nil
		},
	}
	deleteCommand := &cobra.Command{
		Use:   "delete ROUTINE_ID",
		Short: "Delete a routine and its fire history; kept Capsules stay",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			current, err := getRoutine(command, api, args[0])
			if err != nil {
				return err
			}
			key, err := idempotencyKey("")
			if err != nil {
				return err
			}
			result, err := api.DeleteRoutine(command.Context(),
				&client.LifecycleMutationRequest{ExpectedResourceVersion: current.ResourceVersion},
				client.DeleteRoutineParams{RoutineId: args[0], IdempotencyKey: key})
			if err != nil {
				return apiCallError(err)
			}
			if _, ok := result.(*client.DeleteRoutineNoContent); !ok {
				return responseError(result)
			}
			if config.json {
				deleted := map[string]string{"deleted": args[0]}
				return writeJSON(config.stdout, &deleted)
			}
			_, err = fmt.Fprintf(config.stdout, "deleted\t%s\n", args[0])
			return err
		},
	}
	routine.AddCommand(list, get, create, update,
		toggle("enable", "Turn a routine on", true), toggle("disable", "Turn a routine off", false),
		run, fires, deleteCommand)
	return routine
}

func getRoutine(command *cobra.Command, api *client.Client, id string) (*client.Routine, error) {
	result, err := api.GetRoutine(command.Context(), client.GetRoutineParams{RoutineId: id})
	if err != nil {
		return nil, apiCallError(err)
	}
	item, ok := result.(*client.Routine)
	if !ok {
		return nil, responseError(result)
	}
	return item, nil
}

func changeRoutine(
	command *cobra.Command, config *cliConfig, id string, change func(*client.UpdateRoutineRequest) error,
) error {
	api, err := newAPI(config)
	if err != nil {
		return err
	}
	current, err := getRoutine(command, api, id)
	if err != nil {
		return err
	}
	request := &client.UpdateRoutineRequest{ExpectedResourceVersion: current.ResourceVersion}
	if err := change(request); err != nil {
		return err
	}
	key, err := idempotencyKey("")
	if err != nil {
		return err
	}
	result, err := api.UpdateRoutine(command.Context(), request,
		client.UpdateRoutineParams{RoutineId: id, IdempotencyKey: key})
	if err != nil {
		return apiCallError(err)
	}
	item, ok := result.(*client.Routine)
	if !ok {
		return responseError(result)
	}
	return writeRoutine(config, item)
}

func writeRoutine(config *cliConfig, item *client.Routine) error {
	if config.json {
		return writeJSON(config.stdout, item)
	}
	if err := writeRoutineLine(config.stdout, *item); err != nil {
		return err
	}
	_, err := fmt.Fprintf(config.stdout, "prompt:\n%s\n", item.Prompt)
	return err
}

func writeRoutineLine(out io.Writer, item client.Routine) error {
	state := "on"
	if !item.Enabled {
		state = "off"
		if reason, ok := item.DisabledReason.Get(); ok && reason != "" {
			state += "(" + reason + ")"
		}
	}
	next := "-"
	if at, ok := item.NextFireAt.Get(); ok {
		next = at.Local().Format(time.RFC3339)
	}
	model := "default"
	if value, ok := item.Model.Get(); ok && value != "" {
		model = value
	}
	_, err := fmt.Fprintf(out, "%s\t%s\t%s\t%q\t%s\tmodel=%s\tnext=%s\tkept=%d/%d\tversion=%d\n",
		item.ID, item.Name, state, item.Cadence, item.TimeZone, model, next,
		item.KeptCapsules, item.MaxKept, item.ResourceVersion)
	return err
}

func writeFireLine(out io.Writer, fire client.RoutineFire) error {
	result := string(fire.State)
	if outcome, ok := fire.Outcome.Get(); ok && outcome != "" {
		result += "/" + string(outcome)
	}
	if reason, ok := fire.Reason.Get(); ok && reason != "" {
		result += "(" + reason + ")"
	}
	capsule := "-"
	if id, ok := fire.CapsuleId.Get(); ok && id != "" {
		capsule = id
		if fire.CapsuleDeleted {
			capsule += "(deleted)"
		}
	}
	_, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\tcapsule=%s\n",
		fire.ID, fire.ScheduledFor.Local().Format(time.RFC3339), fire.Trigger, result, capsule)
	return err
}
