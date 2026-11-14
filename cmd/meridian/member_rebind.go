package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
)

func newMemberRebindCommand(config *cliConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "rebind-identity USER_ID",
		Short: "Move the bootstrap owner to a new OIDC identity (break-glass only)",
		Long: "Use the installation token to start recovery of the bootstrap owner\n" +
			"(usr_bootstrap_owner), then open the displayed URL and sign in as the\n" +
			"replacement identity. Only the bootstrap owner can be recovered; disable\n" +
			"and transfer anyone else. Recovery revokes the owner's personal Claude and\n" +
			"ChatGPT logins, preserves their objects and role, revokes their old sessions\n" +
			"and tokens, and is audited. The replacement identity must not already\n" +
			"belong to another member.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(command.Context(), 10*time.Minute)
			defer cancel()
			started, err := api.BeginMemberIdentityRebind(ctx, client.BeginMemberIdentityRebindParams{UserId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			recovery, ok := started.(*client.IdentityRebind)
			if !ok {
				return responseError(started)
			}
			if _, err := fmt.Fprintf(command.ErrOrStderr(), "Open %s and sign in as the replacement identity for member %s.\nWaiting for sign-in; recovery expires at %s.\n",
				recovery.SignInUrl.String(), args[0], recovery.ExpiresAt.Format(time.RFC3339)); err != nil {
				return err
			}
			for {
				completed, err := api.CompleteMemberIdentityRebind(ctx, &client.CompleteMemberIdentityRebindReq{Token: recovery.Token}, client.CompleteMemberIdentityRebindParams{UserId: args[0]})
				if err != nil {
					return apiCallError(err)
				}
				switch completed.(type) {
				case *client.CompleteMemberIdentityRebindNoContent:
					if config.json {
						return writeJSON(config.stdout, &struct {
							UserID  string `json:"userId"`
							Rebound bool   `json:"rebound"`
						}{args[0], true})
					}
					_, err := fmt.Fprintln(config.stdout, "Identity rebound. Old sessions and tokens are revoked; sign in again.")
					return err
				case *client.CompleteMemberIdentityRebindAccepted:
					if !time.Now().Before(recovery.ExpiresAt) {
						return errors.New("identity recovery expired; start again")
					}
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(2 * time.Second):
					}
				default:
					return responseError(completed)
				}
			}
		},
	}
}
