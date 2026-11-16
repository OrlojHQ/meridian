package docker

import (
	"context"
	"time"

	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/ports"
)

func (p *Provider) RunChecks(ctx context.Context, resourceID string, request ports.CheckRequest) (ports.CheckExecution, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.CheckExecution{}, err
	}
	input := capsuleproto.CheckRunRequest{OutputLimit: request.OutputLimit}
	argv := make(map[string][]string, len(request.Commands))
	for _, command := range request.Commands {
		seconds := int(command.Timeout / time.Second)
		if seconds <= 0 {
			seconds = 1
		}
		input.Checks = append(input.Checks, capsuleproto.CheckCommand{
			Name: command.Name, Argv: command.Argv, TimeoutSeconds: seconds,
		})
		argv[command.Name] = command.Argv
	}
	result, err := supervisor.RunChecks(ctx, input)
	if err != nil {
		return ports.CheckExecution{}, mapRuntimeError(err)
	}
	execution := ports.CheckExecution{
		HeadBefore: result.HeadBefore, TreeBefore: result.TreeBefore,
		HeadAfter: result.HeadAfter, TreeAfter: result.TreeAfter, Stale: result.Stale,
	}
	for _, item := range result.Results {
		execution.Results = append(execution.Results, ports.CheckCommandResult{
			Name: item.Name, Argv: append([]string{}, argv[item.Name]...),
			ExitCode: item.ExitCode, Duration: time.Duration(item.Duration) * time.Millisecond,
			TimedOut: item.TimedOut, Truncated: item.Truncated, Output: []byte(item.Output),
		})
	}
	return execution, nil
}

func (p *Provider) SuggestChecks(ctx context.Context, resourceID string) (ports.CheckSuggestion, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.CheckSuggestion{}, err
	}
	result, err := supervisor.SuggestChecks(ctx)
	if err != nil {
		return ports.CheckSuggestion{}, mapRuntimeError(err)
	}
	suggestion := ports.CheckSuggestion{Ignored: result.Ignored}
	for _, check := range result.Checks {
		suggestion.Checks = append(suggestion.Checks, ports.CheckCommand{
			Name: check.Name, Argv: append([]string{}, check.Argv...),
			Timeout: time.Duration(check.TimeoutSeconds) * time.Second,
		})
	}
	return suggestion, nil
}
