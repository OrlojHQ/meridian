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
}

func (f *leadFlags) register(command *cobra.Command) {
	flags := command.Flags()
	flags.BoolVar(&f.lead, "lead", false,
		"let the Thread start helper Threads in their own Capsules (bounded by the flags below)")
	flags.IntVar(&f.maxChildren, "max-children", 0, "helpers the lead may start (default 4, at most 16)")
	flags.IntVar(&f.maxDescendants, "max-descendants", 0, "helpers started over the grant's lifetime (at most 32)")
	flags.IntVar(&f.maxLiveCapsules, "max-live-capsules", 0, "helper Capsules running at once (at most 16)")
	flags.IntVar(&f.maxMessagesPerLink, "max-messages-per-link", 0, "lead messages per helper (default 50)")
	flags.StringArrayVar(&f.allowHarnesses, "allow-harness", nil,
		"structured harness helpers may use; repeatable (default: the lead's harness)")
	flags.DurationVar(&f.deadline, "lead-deadline", 0, "how long the lead may coordinate (default 8h, 5m to 72h)")
	flags.StringVar(&f.onDeadline, "on-deadline", "",
		"at the deadline: cancel_and_pause (default) or stop_new_work")
}

func (f *leadFlags) policy(command *cobra.Command) (client.OptCoordinationPolicy, error) {
	if !f.lead {
		for _, name := range []string{
			"max-children", "max-descendants", "max-live-capsules", "max-messages-per-link",
			"allow-harness", "lead-deadline", "on-deadline",
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
	switch f.onDeadline {
	case "":
	case string(client.CoordinationPolicyOnDeadlineCancelAndPause), string(client.CoordinationPolicyOnDeadlineStopNewWork):
		policy.OnDeadline = client.NewOptCoordinationPolicyOnDeadline(client.CoordinationPolicyOnDeadline(f.onDeadline))
	default:
		return client.OptCoordinationPolicy{}, errors.New("--on-deadline must be cancel_and_pause or stop_new_work")
	}
	return client.NewOptCoordinationPolicy(policy), nil
}

func newThreadLinksCommand(config *cliConfig) *cobra.Command {
	return &cobra.Command{
		Use: "links THREAD_ID", Short: "List the helper Threads a lead Thread started",
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
				if _, err := fmt.Fprintf(config.stdout,
					"%s\t%s\t%s\tphase=%s\treports=%d\tthread=%s\tcapsule=%s\n",
					link.ID, link.Name, link.Harness, link.Phase, link.ReportSeq,
					link.ChildThreadId, link.ChildCapsuleId,
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
		Long: "List every Capsule by status, needs-you first. Helpers fold under their lead's\n" +
			"rollup unless they need you; --all lists every helper.",
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
	command.Flags().BoolVar(&all, "all", false, "list every helper, not only the ones that need you")
	command.Flags().StringVar(&expand, "expand", "", "helpers to list: attention (default), all or none")
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
				name += " (helper)"
			}
		}
		thread, harness := "-", item.Capsule.Harness.Or("-")
		if value, ok := item.Thread.Get(); ok {
			thread, harness = value.ID, value.Harness
		}
		line := fmt.Sprintf("%s\t%s\t%s\tcapsule=%s\tthread=%s\tsince=%s",
			status, name, harness, item.Capsule.ID, thread, item.Since.Format(time.RFC3339))
		if rollup, ok := item.ChildRollup.Get(); ok {
			line += fmt.Sprintf("\thelpers=%d working=%d idle=%d done=%d needs_you=%d failed=%d",
				rollup.Total, rollup.Working, rollup.Idle, rollup.Done, rollup.NeedsYou, rollup.Failed)
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
	root := &cobra.Command{Use: "grant", Short: "Inspect or revoke a lead Thread's coordination grant"}
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
		Use: "revoke GRANT_ID", Short: "Stop a lead from starting helpers and cancel its running helpers",
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
	revoke.Flags().BoolVar(&keep, "keep-helpers", false, "leave running helpers alone instead of cancelling and pausing them")
	revoke.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	root.AddCommand(show, revoke)
	return root
}

func (c *cliConfig) writeCoordinationGrant(grant client.CoordinationGrant) error {
	if c.json {
		return writeJSON(c.stdout, &grant)
	}
	_, err := fmt.Fprintf(c.stdout,
		"%s\tstate=%s\tlead=%s\thelpers=%d/%d\tchildren=%d\tlive=%d\tharnesses=%s\tdeadline=%s\tversion=%d\n",
		grant.ID, grant.State, grant.RootThreadId, grant.DescendantsCreated, grant.MaxDescendants,
		grant.MaxChildren, grant.MaxLiveCapsules, strings.Join(grant.AllowedHarnesses, ","),
		grant.DeadlineAt.Format(time.RFC3339), grant.ResourceVersion,
	)
	return err
}
