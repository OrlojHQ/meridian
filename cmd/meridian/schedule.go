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

// Scheduled Project Threads (ADR 0057).

type scheduleFlags struct {
	name, harness, model, effort, prompt, promptFile, cadence, timeZone string
	maxRun, catchUp                                                     time.Duration
	maxKept                                                             int
	enabled                                                             bool
}

func (f *scheduleFlags) bind(command *cobra.Command, create bool) {
	flags := command.Flags()
	flags.StringVar(&f.name, "name", "", "schedule name: lowercase letters, digits, and dashes")
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
	if create {
		_ = command.MarkFlagRequired("name")
		_ = command.MarkFlagRequired("harness")
		_ = command.MarkFlagRequired("cron")
	}
}

func (f *scheduleFlags) readPrompt(stdin io.Reader) (string, error) {
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

func newScheduleCommand(config *cliConfig) *cobra.Command {
	schedule := &cobra.Command{
		Use:   "schedule",
		Short: "Start Project Threads on a schedule",
		Long: "A schedule starts one fresh Capsule and Thread with a fixed prompt on a cron clock.\n" +
			"A run with no changes deletes its Capsule. A run with changes, a question, or a\n" +
			"failure is kept for you. A schedule never ships: Delivery still needs your approval.",
	}
	list := &cobra.Command{
		Use:   "list PROJECT_ID",
		Short: "List a Project's schedules",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.ListProjectSchedules(command.Context(), client.ListProjectSchedulesParams{ProjectId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			page, ok := result.(*client.ScheduleList)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, page)
			}
			for _, item := range page.Items {
				if err := writeScheduleLine(config.stdout, item); err != nil {
					return err
				}
			}
			return nil
		},
	}
	get := &cobra.Command{
		Use:   "get SCHEDULE_ID",
		Short: "Show a schedule, prompt included",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.GetSchedule(command.Context(), client.GetScheduleParams{ScheduleId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			item, ok := result.(*client.Schedule)
			if !ok {
				return responseError(result)
			}
			return writeSchedule(config, item)
		},
	}
	var createFlags scheduleFlags
	create := &cobra.Command{
		Use:   "create PROJECT_ID",
		Short: "Save a schedule on a Project",
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
			request := &client.CreateScheduleRequest{
				Name: createFlags.name, Harness: createFlags.harness, Prompt: prompt,
				Cadence: createFlags.cadence, TimeZone: zone,
				MaxRunSeconds:  client.NewOptInt(int(createFlags.maxRun / time.Second)),
				CatchUpSeconds: client.NewOptInt(int(createFlags.catchUp / time.Second)),
				MaxKept:        client.NewOptInt(createFlags.maxKept),
				Enabled:        client.NewOptBool(createFlags.enabled),
			}
			if createFlags.model != "" {
				request.Model = client.NewOptString(createFlags.model)
			}
			if createFlags.effort != "" {
				request.Effort = client.NewOptString(createFlags.effort)
			}
			result, err := api.CreateProjectSchedule(command.Context(), request,
				client.CreateProjectScheduleParams{ProjectId: args[0], IdempotencyKey: key})
			if err != nil {
				return apiCallError(err)
			}
			item, ok := result.(*client.Schedule)
			if !ok {
				return responseError(result)
			}
			return writeSchedule(config, item)
		},
	}
	createFlags.bind(create, true)
	var updateFlags scheduleFlags
	update := &cobra.Command{
		Use:   "update SCHEDULE_ID",
		Short: "Change a schedule; only the flags you pass change",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return changeSchedule(command, config, args[0], func(request *client.UpdateScheduleRequest) error {
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
				return nil
			})
		},
	}
	updateFlags.bind(update, false)
	toggle := func(use, short string, enabled bool) *cobra.Command {
		return &cobra.Command{
			Use: use + " SCHEDULE_ID", Short: short, Args: cobra.ExactArgs(1),
			RunE: func(command *cobra.Command, args []string) error {
				return changeSchedule(command, config, args[0], func(request *client.UpdateScheduleRequest) error {
					request.Enabled = client.NewOptBool(enabled)
					return nil
				})
			},
		}
	}
	run := &cobra.Command{
		Use:   "run SCHEDULE_ID",
		Short: "Fire a schedule now",
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
			result, err := api.RunSchedule(command.Context(), client.RunScheduleParams{ScheduleId: args[0], IdempotencyKey: key})
			if err != nil {
				return apiCallError(err)
			}
			fire, ok := result.(*client.ScheduleFire)
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
		Use:   "fires SCHEDULE_ID",
		Short: "List a schedule's recent fires",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.ListScheduleFires(command.Context(), client.ListScheduleFiresParams{ScheduleId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			page, ok := result.(*client.ScheduleFirePage)
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
		Use:   "delete SCHEDULE_ID",
		Short: "Delete a schedule and its fire history; kept Capsules stay",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			current, err := getSchedule(command, api, args[0])
			if err != nil {
				return err
			}
			key, err := idempotencyKey("")
			if err != nil {
				return err
			}
			result, err := api.DeleteSchedule(command.Context(),
				&client.LifecycleMutationRequest{ExpectedResourceVersion: current.ResourceVersion},
				client.DeleteScheduleParams{ScheduleId: args[0], IdempotencyKey: key})
			if err != nil {
				return apiCallError(err)
			}
			if _, ok := result.(*client.DeleteScheduleNoContent); !ok {
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
	schedule.AddCommand(list, get, create, update,
		toggle("enable", "Turn a schedule on", true), toggle("disable", "Turn a schedule off", false),
		run, fires, deleteCommand)
	return schedule
}

func getSchedule(command *cobra.Command, api *client.Client, id string) (*client.Schedule, error) {
	result, err := api.GetSchedule(command.Context(), client.GetScheduleParams{ScheduleId: id})
	if err != nil {
		return nil, apiCallError(err)
	}
	item, ok := result.(*client.Schedule)
	if !ok {
		return nil, responseError(result)
	}
	return item, nil
}

func changeSchedule(
	command *cobra.Command, config *cliConfig, id string, change func(*client.UpdateScheduleRequest) error,
) error {
	api, err := newAPI(config)
	if err != nil {
		return err
	}
	current, err := getSchedule(command, api, id)
	if err != nil {
		return err
	}
	request := &client.UpdateScheduleRequest{ExpectedResourceVersion: current.ResourceVersion}
	if err := change(request); err != nil {
		return err
	}
	key, err := idempotencyKey("")
	if err != nil {
		return err
	}
	result, err := api.UpdateSchedule(command.Context(), request,
		client.UpdateScheduleParams{ScheduleId: id, IdempotencyKey: key})
	if err != nil {
		return apiCallError(err)
	}
	item, ok := result.(*client.Schedule)
	if !ok {
		return responseError(result)
	}
	return writeSchedule(config, item)
}

func writeSchedule(config *cliConfig, item *client.Schedule) error {
	if config.json {
		return writeJSON(config.stdout, item)
	}
	if err := writeScheduleLine(config.stdout, *item); err != nil {
		return err
	}
	_, err := fmt.Fprintf(config.stdout, "prompt:\n%s\n", item.Prompt)
	return err
}

func writeScheduleLine(out io.Writer, item client.Schedule) error {
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

func writeFireLine(out io.Writer, fire client.ScheduleFire) error {
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
