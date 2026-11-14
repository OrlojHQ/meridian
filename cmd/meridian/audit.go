package main

import (
	"fmt"
	"time"

	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
)

func newAuditCommand(config *cliConfig) *cobra.Command {
	var after int64
	var limit int
	command := &cobra.Command{
		Use:   "audit",
		Short: "List audit records, oldest first",
		Long: "List append-only audit records after a cursor, oldest first. A member sees their own\n" +
			"records; an admin sees every record. Records name ids, never secret values or content.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			params := client.ListAuditRecordsParams{Limit: client.NewOptInt(limit)}
			if after > 0 {
				params.After = client.NewOptInt64(after)
			}
			result, err := api.ListAuditRecords(command.Context(), params)
			if err != nil {
				return apiCallError(err)
			}
			page, ok := result.(*client.AuditRecordPage)
			if !ok {
				return responseError(result)
			}
			return config.writeAuditPage(*page)
		},
	}
	command.Flags().Int64Var(&after, "after", 0, "list records after this sequence")
	command.Flags().IntVar(&limit, "limit", 50, "page size (1-500)")
	return command
}

func (c *cliConfig) writeAuditPage(page client.AuditRecordPage) error {
	if c.json {
		return writeJSON(c.stdout, &page)
	}
	for _, record := range page.Items {
		actor := string(record.Principal)
		if user, ok := record.UserId.Get(); ok {
			actor += ":" + user
		}
		if capsule, ok := record.CapsuleId.Get(); ok {
			actor += ":" + capsule
		}
		if _, err := fmt.Fprintf(c.stdout, "%d\t%s\t%s\t%s\t%s/%s\t%s\n",
			record.Sequence, record.RecordedAt.UTC().Format(time.RFC3339), actor,
			record.Action, record.ObjectType, record.ObjectId, record.Result); err != nil {
			return err
		}
	}
	if page.More {
		_, err := fmt.Fprintf(c.stdout, "more: --after %d\n", page.NextCursor)
		return err
	}
	return nil
}
