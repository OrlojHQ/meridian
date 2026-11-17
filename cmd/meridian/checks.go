package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
)

func newProjectChecksCommand(config *cliConfig) *cobra.Command {
	checks := &cobra.Command{Use: "checks", Short: "Show or set the checks a Project runs"}
	show := &cobra.Command{
		Use:   "show PROJECT_ID",
		Short: "Show Project checks",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.GetProjectChecks(command.Context(), client.GetProjectChecksParams{ProjectId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			return writeProjectChecks(config, result)
		},
	}
	var specs []string
	var attempts int
	var auto bool
	set := &cobra.Command{
		Use:   "set PROJECT_ID",
		Short: "Replace Project checks",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			current, err := api.GetProjectChecks(command.Context(), client.GetProjectChecksParams{ProjectId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			parsed, err := parseCheckSpecs(specs)
			if err != nil {
				return err
			}
			key, err := idempotencyKey("")
			if err != nil {
				return err
			}
			updated, err := api.UpdateProjectChecks(command.Context(), &client.ProjectChecksUpdate{
				Checks: parsed, AutoRework: auto, MaxReworkAttempts: attempts,
				ExpectedResourceVersion: current.ResourceVersion,
			}, client.UpdateProjectChecksParams{ProjectId: args[0], IdempotencyKey: key})
			if err != nil {
				return apiCallError(err)
			}
			return writeProjectChecks(config, updated)
		},
	}
	set.Flags().StringArrayVar(&specs, "check", nil, "name=command, repeatable; for example test=make test")
	set.Flags().IntVar(&attempts, "max-attempts", 3, "total tries, including the first; 0 runs once and does not retry")
	set.Flags().BoolVar(&auto, "auto-rework", true, "send a failing check back to the agent")
	checks.AddCommand(show, set)
	return checks
}

func newCapsuleChecksCommand(config *cliConfig) *cobra.Command {
	var run bool
	var follow bool
	command := &cobra.Command{
		Use:   "checks CAPSULE_ID",
		Short: "Show or run Capsule checks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			if !run {
				page, err := api.ListCapsuleCheckRuns(cmd.Context(), client.ListCapsuleCheckRunsParams{CapsuleId: args[0]})
				if err != nil {
					return apiCallError(err)
				}
				return writeCheckRuns(config, page)
			}
			key, err := idempotencyKey("")
			if err != nil {
				return err
			}
			created, err := api.CreateCapsuleCheckRun(cmd.Context(), &client.CheckRunRequest{}, client.CreateCapsuleCheckRunParams{
				CapsuleId: args[0], IdempotencyKey: key,
			})
			if err != nil {
				return apiCallError(err)
			}
			if !follow {
				return writeCheckRun(config, created)
			}
			deadline := time.Now().Add(61 * time.Minute)
			current := created
			for !checkTerminal(current.State) && time.Now().Before(deadline) {
				time.Sleep(time.Second)
				current, err = api.GetCheckRun(cmd.Context(), client.GetCheckRunParams{CheckRunId: current.ID})
				if err != nil {
					return apiCallError(err)
				}
			}
			return writeCheckRun(config, current)
		},
	}
	command.Flags().BoolVar(&run, "run", false, "run the Project checks now")
	command.Flags().BoolVar(&follow, "follow", false, "wait until the run finishes")
	return command
}

func parseCheckSpecs(specs []string) ([]client.CheckCommand, error) {
	commands := make([]client.CheckCommand, 0, len(specs))
	for _, spec := range specs {
		name, command, ok := strings.Cut(spec, "=")
		name = strings.TrimSpace(name)
		argv := strings.Fields(command)
		if !ok || name == "" || len(argv) == 0 {
			return nil, fmt.Errorf("check must be name=command, for example test=make test")
		}
		commands = append(commands, client.CheckCommand{Name: name, Argv: argv, TimeoutSeconds: 600})
	}
	return commands, nil
}

func writeProjectChecks(config *cliConfig, checks *client.ProjectChecks) error {
	if config.json {
		return writeJSON(config.stdout, checks)
	}
	if _, err := fmt.Fprintf(config.stdout, "auto-rework=%t\tmax-attempts=%d\tversion=%d\n",
		checks.AutoRework, checks.MaxReworkAttempts, checks.ResourceVersion); err != nil {
		return err
	}
	if len(checks.Checks) == 0 {
		_, err := fmt.Fprintln(config.stdout, "(no checks)")
		return err
	}
	for _, check := range checks.Checks {
		if _, err := fmt.Fprintf(config.stdout, "%s\t%s\ttimeout=%ds\n", check.Name, strings.Join(check.Argv, " "), check.TimeoutSeconds); err != nil {
			return err
		}
	}
	return nil
}

func writeCheckRuns(config *cliConfig, page *client.CheckRunPage) error {
	if config.json {
		return writeJSON(config.stdout, page)
	}
	if len(page.Items) == 0 {
		_, err := fmt.Fprintln(config.stdout, "(no check runs)")
		return err
	}
	for index := range page.Items {
		if err := writeCheckRun(config, &page.Items[index]); err != nil {
			return err
		}
	}
	return nil
}

func writeCheckRun(config *cliConfig, run *client.CheckRun) error {
	if config.json {
		return writeJSON(config.stdout, run)
	}
	phase := ""
	if value, ok := run.Phase.Get(); ok {
		phase = string(value)
	}
	if _, err := fmt.Fprintf(config.stdout, "%s\t%s\tattempt=%d/%d\tphase=%s\n", run.ID, run.State, run.Attempt, run.MaxAttempts, phase); err != nil {
		return err
	}
	for _, result := range run.Results {
		status := "passed"
		if result.TimedOut || (result.ExitCode.Set && result.ExitCode.Value != 0) {
			status = "failed"
		}
		if _, err := fmt.Fprintf(config.stdout, "  %s\t%s\t%s\n", status, result.Name, strings.Join(result.Argv, " ")); err != nil {
			return err
		}
	}
	return nil
}

func checkTerminal(state client.CheckRunState) bool {
	switch state {
	case client.CheckRunStatePassed, client.CheckRunStateFailed, client.CheckRunStateError,
		client.CheckRunStateCancelled, client.CheckRunStateStale:
		return true
	default:
		return false
	}
}
