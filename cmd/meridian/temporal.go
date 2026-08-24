package main

import (
	"errors"
	"fmt"

	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
)

func newMomentCommand(config *cliConfig) *cobra.Command {
	root := &cobra.Command{Use: "moment", Short: "Capture and inspect filesystem Moments"}
	var expected int64
	var key string
	capture := &cobra.Command{
		Use: "capture CAPSULE_ID NAME", Args: cobra.ExactArgs(2),
		Short: "Capture an immutable filesystem Moment",
		RunE: func(command *cobra.Command, args []string) error {
			if expected <= 0 {
				return errors.New("--expected-version must be greater than zero")
			}
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			replayKey, err := idempotencyKey(key)
			if err != nil {
				return err
			}
			response, err := api.CaptureMoment(
				command.Context(),
				&client.CaptureMomentRequest{Name: args[1], ExpectedResourceVersion: expected},
				client.CaptureMomentParams{CapsuleId: args[0], IdempotencyKey: replayKey},
			)
			if err != nil {
				return apiCallError(err)
			}
			result, ok := response.(*client.Moment)
			if !ok {
				return responseError(response)
			}
			return config.writeMoment(*result)
		},
	}
	capture.Flags().Int64Var(&expected, "expected-version", 0, "required current Capsule resource version")
	capture.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")

	var cursor string
	var limit int
	list := &cobra.Command{
		Use: "list CAPSULE_ID", Args: cobra.ExactArgs(1), Short: "List Timeline Moments",
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			params := client.ListMomentsParams{CapsuleId: args[0], Limit: client.NewOptInt(limit)}
			if cursor != "" {
				params.Cursor = client.NewOptString(cursor)
			}
			response, err := api.ListMoments(command.Context(), params)
			if err != nil {
				return apiCallError(err)
			}
			result, ok := response.(*client.MomentPage)
			if !ok {
				return responseError(response)
			}
			if config.json {
				return writeJSON(config.stdout, result)
			}
			for _, moment := range result.Items {
				if err := config.writeMoment(moment); err != nil {
					return err
				}
			}
			return nil
		},
	}
	list.Flags().StringVar(&cursor, "cursor", "", "pagination cursor")
	list.Flags().IntVar(&limit, "limit", 50, "page size")

	get := &cobra.Command{
		Use: "get MOMENT_ID", Args: cobra.ExactArgs(1), Short: "Get a Moment",
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			response, err := api.GetMoment(command.Context(), client.GetMomentParams{MomentId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			result, ok := response.(*client.Moment)
			if !ok {
				return responseError(response)
			}
			return config.writeMoment(*result)
		},
	}
	root.AddCommand(capture, list, get)
	return root
}

func newTimelineCommand(config *cliConfig) *cobra.Command {
	root := &cobra.Command{Use: "timeline", Short: "Inspect Timeline lineage"}
	get := &cobra.Command{
		Use: "get TIMELINE_ID", Args: cobra.ExactArgs(1), Short: "Get a Timeline with ancestry",
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			response, err := api.GetTimeline(command.Context(), client.GetTimelineParams{TimelineId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			result, ok := response.(*client.TimelineView)
			if !ok {
				return responseError(response)
			}
			if config.json {
				return writeJSON(config.stdout, result)
			}
			for _, timeline := range result.Ancestry {
				fmt.Fprintf(config.stdout, "%s\treason=%s\tcapsule=%s\n", timeline.ID, timeline.Reason, timeline.CapsuleId)
			}
			return nil
		},
	}
	root.AddCommand(get)
	return root
}

func newShardCommand(config *cliConfig) *cobra.Command {
	root := &cobra.Command{Use: "shard", Short: "Create descendant Capsules"}
	var from, name, key string
	create := &cobra.Command{
		Use: "create", Args: cobra.NoArgs, Short: "Create a Shard from a Moment",
		RunE: func(command *cobra.Command, _ []string) error {
			if from == "" || name == "" {
				return errors.New("--from and --name are required")
			}
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			replayKey, err := idempotencyKey(key)
			if err != nil {
				return err
			}
			response, err := api.CreateShard(
				command.Context(), &client.CreateDescendantRequest{Name: name},
				client.CreateShardParams{MomentId: from, IdempotencyKey: replayKey},
			)
			if err != nil {
				return apiCallError(err)
			}
			result, ok := response.(*client.DescendantResult)
			if !ok {
				return responseError(response)
			}
			return config.writeDescendant(*result)
		},
	}
	create.Flags().StringVar(&from, "from", "", "source Moment ID")
	create.Flags().StringVar(&name, "name", "", "new Capsule name")
	create.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	root.AddCommand(create)
	return root
}

func newRewindCommand(config *cliConfig) *cobra.Command {
	var to, name, key string
	command := &cobra.Command{
		Use: "rewind CAPSULE_ID", Args: cobra.ExactArgs(1),
		Short: "Create a non-destructive rewind descendant",
		RunE: func(command *cobra.Command, args []string) error {
			if to == "" || name == "" {
				return errors.New("--to and --name are required")
			}
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			replayKey, err := idempotencyKey(key)
			if err != nil {
				return err
			}
			response, err := api.RewindCapsule(
				command.Context(), &client.RewindRequest{MomentId: to, Name: name},
				client.RewindCapsuleParams{CapsuleId: args[0], IdempotencyKey: replayKey},
			)
			if err != nil {
				return apiCallError(err)
			}
			result, ok := response.(*client.DescendantResult)
			if !ok {
				return responseError(response)
			}
			return config.writeDescendant(*result)
		},
	}
	command.Flags().StringVar(&to, "to", "", "source Moment ID")
	command.Flags().StringVar(&name, "name", "", "new Capsule name")
	command.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	return command
}

func newSealCommand(config *cliConfig) *cobra.Command {
	var expected int64
	var key string
	command := &cobra.Command{
		Use: "seal CAPSULE_ID", Args: cobra.ExactArgs(1), Short: "Capture final Moment and seal Capsule",
		RunE: func(command *cobra.Command, args []string) error {
			if expected <= 0 {
				return errors.New("--expected-version must be greater than zero")
			}
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			replayKey, err := idempotencyKey(key)
			if err != nil {
				return err
			}
			response, err := api.SealCapsule(
				command.Context(), &client.LifecycleMutationRequest{ExpectedResourceVersion: expected},
				client.SealCapsuleParams{CapsuleId: args[0], IdempotencyKey: replayKey},
			)
			if err != nil {
				return apiCallError(err)
			}
			result, ok := response.(*client.SealResult)
			if !ok {
				return responseError(response)
			}
			if config.json {
				return writeJSON(config.stdout, result)
			}
			fmt.Fprintf(config.stdout, "%s\tsealed\tmoment=%s\n", result.Capsule.ID, result.Moment.ID)
			return nil
		},
	}
	command.Flags().Int64Var(&expected, "expected-version", 0, "required current Capsule resource version")
	command.Flags().StringVar(&key, "idempotency-key", "", "mutation replay key (generated if omitted)")
	return command
}

func (c *cliConfig) writeMoment(moment client.Moment) error {
	if c.json {
		return writeJSON(c.stdout, moment)
	}
	_, err := fmt.Fprintf(c.stdout, "%s\t%s\tsha256=%s\tsize=%d\n", moment.ID, moment.Name, moment.ArchiveSha256, moment.ArchiveSize)
	return err
}

func (c *cliConfig) writeDescendant(result client.DescendantResult) error {
	if c.json {
		return writeJSON(c.stdout, result)
	}
	_, err := fmt.Fprintf(c.stdout, "%s\ttimeline=%s\treason=%s\n", result.Capsule.ID, result.Timeline.ID, result.Reason)
	return err
}
