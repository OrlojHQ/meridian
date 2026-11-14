package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
)

type leadFlags struct {
	lead               bool
	maxChildren        int
	maxDescendants     int
	maxLiveCapsules    int
	maxMessagesPerLink int
	allowHarnesses     []string
	deadline           time.Duration
	onDeadline         string
	archiveAfter       time.Duration
	spawnApproval      string
}

func (f *leadFlags) register(command *cobra.Command) {
	flags := command.Flags()
	flags.BoolVar(&f.lead, "lead", false,
		"let the Thread start subagent Threads in their own Capsules (bounded by the flags below)")
	flags.IntVar(&f.maxChildren, "max-children", 0, "subagents the lead may start (default 4, at most 16)")
	flags.IntVar(&f.maxDescendants, "max-descendants", 0, "subagents started over the grant's lifetime (at most 32)")
	flags.IntVar(&f.maxLiveCapsules, "max-live-capsules", 0, "subagent Capsules running at once (at most 16)")
	flags.IntVar(&f.maxMessagesPerLink, "max-messages-per-link", 0, "lead messages per subagent (default 50)")
	flags.StringArrayVar(&f.allowHarnesses, "allow-harness", nil,
		"structured harness subagents may use; repeatable (default: the lead's harness)")
	flags.DurationVar(&f.deadline, "lead-deadline", 0, "how long the lead may coordinate (default 24h, 5m to 72h)")
	flags.StringVar(&f.onDeadline, "on-deadline", "",
		"at the deadline: cancel_and_pause (default) or stop_new_work")
	flags.DurationVar(&f.archiveAfter, "archive-after", 0,
		"idle time before a finished subagent is archived and, if nothing is unsaved, its Capsule deleted (default 1h, 1m to 720h; 0 disables)")
	flags.StringVar(&f.spawnApproval, "spawn-approval", "",
		"auto (default) starts each spawn; operator queues it until you approve")
}

func (f *leadFlags) policy(command *cobra.Command) (client.OptCoordinationPolicy, error) {
	if !f.lead {
		for _, name := range []string{
			"max-children", "max-descendants", "max-live-capsules", "max-messages-per-link",
			"allow-harness", "lead-deadline", "on-deadline", "archive-after", "spawn-approval",
		} {
			if command.Flags().Changed(name) {
				return client.OptCoordinationPolicy{}, fmt.Errorf("--%s requires --lead", name)
			}
		}
		return client.OptCoordinationPolicy{}, nil
	}
	policy := client.CoordinationPolicy{AllowedHarnesses: f.allowHarnesses}
	optional := func(value int) client.OptInt {
		if value == 0 {
			return client.OptInt{}
		}
		return client.NewOptInt(value)
	}
	policy.MaxChildren = optional(f.maxChildren)
	policy.MaxDescendants = optional(f.maxDescendants)
	policy.MaxLiveCapsules = optional(f.maxLiveCapsules)
	policy.MaxMessagesPerLink = optional(f.maxMessagesPerLink)
	if f.deadline != 0 {
		if f.deadline%time.Second != 0 {
			return client.OptCoordinationPolicy{}, errors.New("--lead-deadline must be whole seconds")
		}
		policy.DeadlineSeconds = client.NewOptInt(int(f.deadline / time.Second))
	}
	if command.Flags().Changed("archive-after") {
		if f.archiveAfter < 0 || f.archiveAfter%time.Second != 0 {
			return client.OptCoordinationPolicy{}, errors.New("--archive-after must be 0 or whole seconds")
		}
		policy.ArchiveAfterSeconds = client.NewOptInt(int(f.archiveAfter / time.Second))
	}
	switch f.onDeadline {
	case "":
	case string(client.CoordinationPolicyOnDeadlineCancelAndPause), string(client.CoordinationPolicyOnDeadlineStopNewWork):
		policy.OnDeadline = client.NewOptCoordinationPolicyOnDeadline(client.CoordinationPolicyOnDeadline(f.onDeadline))
	default:
		return client.OptCoordinationPolicy{}, errors.New("--on-deadline must be cancel_and_pause or stop_new_work")
	}
	switch f.spawnApproval {
	case "":
	case "auto", "operator":
		policy.SpawnApproval = client.NewOptCoordinationPolicySpawnApproval(client.CoordinationPolicySpawnApproval(f.spawnApproval))
	default:
		return client.OptCoordinationPolicy{}, errors.New("--spawn-approval must be auto or operator")
	}
	return client.NewOptCoordinationPolicy(policy), nil
}

func newThreadLinksCommand(config *cliConfig) *cobra.Command {
	return &cobra.Command{
		Use: "links THREAD_ID", Short: "List the subagent Threads a lead Thread started",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.ListThreadLinks(command.Context(), client.ListThreadLinksParams{ThreadId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			page, ok := result.(*client.ThreadLinkPage)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, page)
			}
			for _, link := range page.Items {
				state := string(link.State)
				if reason, ok := link.CloseReason.Get(); ok {
					state += "(" + string(reason) + ")"
				}
				if link.PromotedAt.Set {
					state += "\tpromoted"
				}
				if link.ArchivedAt.Set {
					state += "\tarchived"
				}
				if _, err := fmt.Fprintf(config.stdout,
					"%s\t%s\t%s\tphase=%s\treports=%d\tthread=%s\tcapsule=%s\tstate=%s\n",
					link.ID, link.Name, link.Harness, link.Phase, link.ReportSeq,
					link.ChildThreadId, link.ChildCapsuleId, state,
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newBoardCommand(config *cliConfig) *cobra.Command {
	var projectID, expand string
	var all bool
	command := &cobra.Command{
		Use:   "board",
		Short: "List every Capsule by status, needs-you first",
		Long: "List every Capsule by status, needs-you first. Subagents fold under their lead's\n" +
			"rollup unless they need you; --all lists every subagent.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			params := client.GetBoardParams{}
			if projectID != "" {
				params.ProjectId = client.NewOptString(projectID)
			}
			if all {
				if command.Flags().Changed("expand") {
					return errors.New("--all and --expand are mutually exclusive")
				}
				expand = string(client.GetBoardExpandAll)
			}
			switch expand {
			case "":
			case string(client.GetBoardExpandAttention), string(client.GetBoardExpandAll), string(client.GetBoardExpandNone):
				params.Expand = client.NewOptGetBoardExpand(client.GetBoardExpand(expand))
			default:
				return errors.New("--expand must be attention, all or none")
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.GetBoard(command.Context(), params)
			if err != nil {
				return apiCallError(err)
			}
			page, ok := result.(*client.BoardPage)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, page)
			}
			return writeBoard(config, page)
		},
	}
	command.Flags().StringVar(&projectID, "project", "", "only this Project's Capsules")
	command.Flags().BoolVar(&all, "all", false, "list every subagent, not only the ones that need you")
	command.Flags().StringVar(&expand, "expand", "", "subagents to list: attention (default), all or none")
	return command
}

func writeBoard(config *cliConfig, page *client.BoardPage) error {
	for _, item := range page.Items {
		status := string(item.Status)
		if reason, ok := item.NeedsYou.Get(); ok {
			status += "(" + string(reason) + ")"
		}
		name := item.Capsule.Name
		if link, ok := item.Link.Get(); ok {
			name = link.Name
			if item.Surfaced {
				name += " (subagent)"
			}
		}
		thread, harness := "-", item.Capsule.Harness.Or("-")
		if value, ok := item.Thread.Get(); ok {
			thread, harness = value.ID, value.Harness
		}
		line := fmt.Sprintf("%s\t%s\t%s\tcapsule=%s\tthread=%s\tsince=%s",
			status, name, harness, item.Capsule.ID, thread, item.Since.Format(time.RFC3339))
		if rollup, ok := item.ChildRollup.Get(); ok {
			line += fmt.Sprintf("\tsubagents=%d working=%d idle=%d done=%d needs_you=%d failed=%d archived=%d",
				rollup.Total, rollup.Working, rollup.Idle, rollup.Done, rollup.NeedsYou, rollup.Failed, rollup.Archived)
		}
		if delivery, ok := item.LatestDelivery.Get(); ok {
			line += "\tdelivery=" + string(delivery.State)
			if url, ok := delivery.PullRequestUrl.Get(); ok {
				line += " " + url.String()
			}
		}
		if _, err := fmt.Fprintln(config.stdout, line); err != nil {
			return err
		}
	}
	if page.Truncated {
		_, err := fmt.Fprintln(config.stdout, "(truncated: more Capsules than one board read covers)")
		return err
	}
	return nil
}

func newGrantCommand(config *cliConfig) *cobra.Command {
	root := &cobra.Command{Use: "grant", Short: "Inspect, update, or revoke a lead Thread's coordination grant"}
	show := &cobra.Command{
		Use: "show GRANT_ID", Short: "Show a coordination grant and its limits", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.GetCoordinationGrant(command.Context(), client.GetCoordinationGrantParams{GrantId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.CoordinationGrantHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeCoordinationGrant(success.Response)
		},
	}
	var expected int64
	var keep bool
	var key string
	revoke := &cobra.Command{
		Use: "revoke GRANT_ID", Short: "Stop a lead from starting subagents and cancel its running subagents",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if expected <= 0 {
				return errors.New("--expected-version is required")
			}
			idempotency, err := idempotencyKey(key)
			if err != nil {
				return err
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.RevokeCoordinationGrant(command.Context(),
				&client.RevokeCoordinationGrantRequest{
					ExpectedResourceVersion: expected,
					CancelDescendants:       client.NewOptBool(!keep),
				},
				client.RevokeCoordinationGrantParams{GrantId: args[0], IdempotencyKey: idempotency},
			)
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.CoordinationGrantHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeCoordinationGrant(success.Response)
		},
	}
	revoke.Flags().Int64Var(&expected, "expected-version", 0, "current grant resource version")
	revoke.Flags().BoolVar(&keep, "keep-subagents", false, "leave running subagents alone instead of cancelling and pausing them")
	revoke.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	root.AddCommand(show, newGrantUpdateCommand(config), revoke)
	return root
}

func newGrantUpdateCommand(config *cliConfig) *cobra.Command {
	var expected int64
	var key, spawnApproval, onDeadline string
	var maxChildren, maxDescendants, maxLive, maxMessages int
	var allowHarnesses []string
	var deadline, archiveAfter time.Duration
	command := &cobra.Command{
		Use: "update GRANT_ID", Short: "Change an active grant's limits or spawn approval",
		Long: "Change an active grant's limits, harnesses, deadline, or spawn approval.\n" +
			"You may loosen or tighten limits. Switching to auto does not start spawns\n" +
			"already waiting for approval. --lead-deadline is remaining time from now.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if expected <= 0 {
				return errors.New("--expected-version is required")
			}
			body := &client.UpdateCoordinationGrantRequest{ExpectedResourceVersion: expected}
			changed := false
			if command.Flags().Changed("spawn-approval") {
				switch spawnApproval {
				case "auto", "operator":
					body.SpawnApproval = client.NewOptUpdateCoordinationGrantRequestSpawnApproval(
						client.UpdateCoordinationGrantRequestSpawnApproval(spawnApproval))
					changed = true
				default:
					return errors.New("--spawn-approval must be auto or operator")
				}
			}
			setInt := func(name string, value int, set func(client.OptInt)) error {
				if !command.Flags().Changed(name) {
					return nil
				}
				if value < 1 {
					return fmt.Errorf("--%s must be at least 1", name)
				}
				set(client.NewOptInt(value))
				changed = true
				return nil
			}
			if err := setInt("max-children", maxChildren, func(v client.OptInt) { body.MaxChildren = v }); err != nil {
				return err
			}
			if err := setInt("max-descendants", maxDescendants, func(v client.OptInt) { body.MaxDescendants = v }); err != nil {
				return err
			}
			if err := setInt("max-live-capsules", maxLive, func(v client.OptInt) { body.MaxLiveCapsules = v }); err != nil {
				return err
			}
			if err := setInt("max-messages-per-link", maxMessages, func(v client.OptInt) { body.MaxMessagesPerLink = v }); err != nil {
				return err
			}
			if command.Flags().Changed("allow-harness") {
				body.AllowedHarnesses = allowHarnesses
				changed = true
			}
			if command.Flags().Changed("lead-deadline") {
				if deadline%time.Second != 0 {
					return errors.New("--lead-deadline must be whole seconds")
				}
				body.DeadlineSeconds = client.NewOptInt(int(deadline / time.Second))
				changed = true
			}
			if command.Flags().Changed("archive-after") {
				if archiveAfter < 0 || archiveAfter%time.Second != 0 {
					return errors.New("--archive-after must be 0 or whole seconds")
				}
				body.ArchiveAfterSeconds = client.NewOptInt(int(archiveAfter / time.Second))
				changed = true
			}
			if command.Flags().Changed("on-deadline") {
				switch onDeadline {
				case string(client.UpdateCoordinationGrantRequestOnDeadlineCancelAndPause),
					string(client.UpdateCoordinationGrantRequestOnDeadlineStopNewWork):
					body.OnDeadline = client.NewOptUpdateCoordinationGrantRequestOnDeadline(
						client.UpdateCoordinationGrantRequestOnDeadline(onDeadline))
					changed = true
				default:
					return errors.New("--on-deadline must be cancel_and_pause or stop_new_work")
				}
			}
			if !changed {
				return errors.New("provide at least one field to change")
			}
			idempotency, err := idempotencyKey(key)
			if err != nil {
				return err
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.UpdateCoordinationGrant(command.Context(), body,
				client.UpdateCoordinationGrantParams{GrantId: args[0], IdempotencyKey: idempotency})
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.CoordinationGrantHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeCoordinationGrant(success.Response)
		},
	}
	command.Flags().Int64Var(&expected, "expected-version", 0, "current grant resource version")
	command.Flags().StringVar(&spawnApproval, "spawn-approval", "", "auto or operator")
	command.Flags().IntVar(&maxChildren, "max-children", 0, "subagents the lead may start")
	command.Flags().IntVar(&maxDescendants, "max-descendants", 0, "subagents started over the grant's lifetime")
	command.Flags().IntVar(&maxLive, "max-live-capsules", 0, "subagent Capsules running at once")
	command.Flags().IntVar(&maxMessages, "max-messages-per-link", 0, "lead messages per subagent")
	command.Flags().StringArrayVar(&allowHarnesses, "allow-harness", nil, "replace the allowed harness list")
	command.Flags().DurationVar(&deadline, "lead-deadline", 0, "remaining time the lead may coordinate")
	command.Flags().StringVar(&onDeadline, "on-deadline", "", "cancel_and_pause or stop_new_work")
	command.Flags().DurationVar(&archiveAfter, "archive-after", 0, "idle time before a finished subagent is archived")
	command.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	return command
}

func newApprovalsCommand(config *cliConfig) *cobra.Command {
	root := &cobra.Command{Use: "approvals", Short: "List, approve, or deny queued subagent spawns"}
	list := &cobra.Command{
		Use: "list", Short: "List spawn requests waiting for approval", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.ListCoordinationRequests(command.Context(), client.ListCoordinationRequestsParams{
				State: client.NewOptCoordinationRequestState(client.CoordinationRequestStatePendingApproval),
			})
			if err != nil {
				return apiCallError(err)
			}
			page, ok := result.(*client.CoordinationRequestPage)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, page)
			}
			for _, item := range page.Items {
				if _, err := fmt.Fprintf(config.stdout, "%s\t%s\t%s\tlink=%s\tlead=%s\tversion=%d\n",
					item.ID, item.Kind, item.State, item.LinkId, item.CapsuleId, item.ResourceVersion,
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
	root.AddCommand(list, newApprovalDecideCommand(config, true), newApprovalDecideCommand(config, false))
	return root
}

func newApprovalDecideCommand(config *cliConfig, approve bool) *cobra.Command {
	use, short := "deny REQUEST_ID", "Refuse a queued subagent spawn"
	if approve {
		use, short = "approve REQUEST_ID", "Start a queued subagent spawn"
	}
	var expected int64
	var key string
	command := &cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if expected <= 0 {
				return errors.New("--expected-version is required")
			}
			idempotency, err := idempotencyKey(key)
			if err != nil {
				return err
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			body := &client.DecideCoordinationRequest{ExpectedResourceVersion: expected}
			params := client.ApproveCoordinationRequestParams{RequestId: args[0], IdempotencyKey: idempotency}
			var result any
			if approve {
				result, err = api.ApproveCoordinationRequest(command.Context(), body, params)
			} else {
				result, err = api.DenyCoordinationRequest(command.Context(), body, client.DenyCoordinationRequestParams{
					RequestId: args[0], IdempotencyKey: idempotency,
				})
			}
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.CoordinationRequestHeaders)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, &success.Response)
			}
			item := success.Response
			_, err = fmt.Fprintf(config.stdout, "%s\t%s\t%s\tlink=%s\tversion=%d\n",
				item.ID, item.Kind, item.State, item.LinkId, item.ResourceVersion)
			return err
		},
	}
	command.Flags().Int64Var(&expected, "expected-version", 0, "current request resource version")
	command.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	return command
}

func (c *cliConfig) writeCoordinationGrant(grant client.CoordinationGrant) error {
	if c.json {
		return writeJSON(c.stdout, &grant)
	}
	_, err := fmt.Fprintf(c.stdout,
		"%s\tstate=%s\tlead=%s\tapproval=%s\tsubagents=%d/%d\tchildren=%d\tlive=%d\tharnesses=%s\tdeadline=%s\tarchive-after=%s\tversion=%d\n",
		grant.ID, grant.State, grant.RootThreadId, grant.SpawnApproval, grant.DescendantsCreated, grant.MaxDescendants,
		grant.MaxChildren, grant.MaxLiveCapsules, strings.Join(grant.AllowedHarnesses, ","),
		grant.DeadlineAt.Format(time.RFC3339), archiveAfterText(grant.ArchiveAfterSeconds), grant.ResourceVersion,
	)
	return err
}

func archiveAfterText(seconds int) string {
	if seconds == 0 {
		return "off"
	}
	return (time.Duration(seconds) * time.Second).String()
}

func newCapsulePromoteCommand(config *cliConfig) *cobra.Command {
	var expected int64
	var detach bool
	var key string
	command := &cobra.Command{
		Use:   "promote CAPSULE_ID",
		Short: "Make a subagent Capsule a main Capsule",
		Long: "Make a subagent Capsule a main Capsule. It leaves the lead's auto-archive and\n" +
			"cancellation, and keeps its link so the lead can still message it and hear its\n" +
			"reports. --detach closes the link too; a promoted Capsule can be detached later.\n" +
			"Promotion is permanent and grants no coordination authority.",
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
			result, err := api.PromoteCapsule(command.Context(),
				&client.PromoteCapsuleRequest{ExpectedResourceVersion: expected, Detach: client.NewOptBool(detach)},
				client.PromoteCapsuleParams{CapsuleId: args[0], IdempotencyKey: idempotency},
			)
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.CapsuleHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeCapsule(success.Response)
		},
	}
	command.Flags().Int64Var(&expected, "expected-version", 0, "required current resource version")
	command.Flags().BoolVar(&detach, "detach", false, "also close the link to the lead")
	command.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	return command
}
