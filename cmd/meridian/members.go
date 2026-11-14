package main

import (
	"errors"
	"fmt"

	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
)

// newMemberCommand administers team mode membership. An admin uses a
// personal access token; with no admin left, the break-glass installation
// token works too (ADR 0046).
func newMemberCommand(config *cliConfig) *cobra.Command {
	member := &cobra.Command{Use: "member", Short: "Administer team mode members (admins and break-glass)"}

	list := &cobra.Command{
		Use: "list", Short: "List members", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.ListMembers(command.Context())
			if err != nil {
				return apiCallError(err)
			}
			page, ok := result.(*client.MemberPage)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, page)
			}
			for _, item := range page.Items {
				state := "enabled"
				if item.Disabled {
					state = "disabled"
				}
				name, _ := item.Email.Get()
				if display, ok := item.DisplayName.Get(); ok {
					name = display
				}
				if _, err := fmt.Fprintf(config.stdout, "%s\t%s\t%s\t%s\n", item.ID, item.Role, state, name); err != nil {
					return err
				}
			}
			return nil
		},
	}

	action := func(use, short string, run func(*cobra.Command, *client.Client, string) (any, error)) *cobra.Command {
		return &cobra.Command{
			Use: use + " USER_ID", Short: short, Args: cobra.ExactArgs(1),
			RunE: func(command *cobra.Command, args []string) error {
				api, err := newAPI(config)
				if err != nil {
					return err
				}
				result, err := run(command, api, args[0])
				if err != nil {
					return apiCallError(err)
				}
				switch result.(type) {
				case *client.DisableMemberNoContent, *client.EnableMemberNoContent,
					*client.RevokeMemberCredentialsNoContent, *client.SetMemberRoleNoContent:
					return nil
				}
				return responseError(result)
			},
		}
	}
	disable := action("disable", "Disable a member and revoke their sessions, tokens, and leases",
		func(command *cobra.Command, api *client.Client, id string) (any, error) {
			return api.DisableMember(command.Context(), client.DisableMemberParams{UserId: id})
		})
	enable := action("enable", "Re-enable a member",
		func(command *cobra.Command, api *client.Client, id string) (any, error) {
			return api.EnableMember(command.Context(), client.EnableMemberParams{UserId: id})
		})
	revoke := action("revoke-credentials", "Revoke every session and token of a member",
		func(command *cobra.Command, api *client.Client, id string) (any, error) {
			return api.RevokeMemberCredentials(command.Context(), client.RevokeMemberCredentialsParams{UserId: id})
		})
	var role string
	setRole := action("set-role", "Grant or remove the admin role",
		func(command *cobra.Command, api *client.Client, id string) (any, error) {
			if role != "member" && role != "admin" {
				return nil, errors.New("--role must be member or admin")
			}
			return api.SetMemberRole(command.Context(), &client.SetMemberRoleReq{Role: client.SetMemberRoleReqRole(role)},
				client.SetMemberRoleParams{UserId: id})
		})
	setRole.Flags().StringVar(&role, "role", "", "member or admin")

	var receiver string
	transfer := &cobra.Command{
		Use: "transfer USER_ID", Short: "Move a disabled member's work to another member (break-glass)",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if receiver == "" {
				return errors.New("--to is required")
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.TransferMemberObjects(command.Context(),
				&client.TransferMemberObjectsReq{ToUserId: receiver}, client.TransferMemberObjectsParams{UserId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			if _, ok := result.(*client.TransferMemberObjectsNoContent); !ok {
				return responseError(result)
			}
			return nil
		},
	}
	transfer.Flags().StringVar(&receiver, "to", "", "the member who receives the work")

	var confirm bool
	deleteObjects := &cobra.Command{
		Use: "delete-objects USER_ID", Short: "Delete a disabled member's work (break-glass, irreversible)",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if !confirm {
				return errors.New("--confirm is required; deletion is irreversible")
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.DeleteMemberObjects(command.Context(), client.DeleteMemberObjectsParams{UserId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			deleted, ok := result.(*client.DeleteMemberObjectsOK)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, deleted)
			}
			_, err = fmt.Fprintf(config.stdout, "projects=%d secrets=%d connections=%d setups=%d\n",
				deleted.Projects, deleted.Secrets, deleted.Connections, deleted.Setups)
			return err
		},
	}
	deleteObjects.Flags().BoolVar(&confirm, "confirm", false, "confirm irreversible deletion")
	member.AddCommand(list, disable, enable, revoke, setRole, transfer, deleteObjects)
	return member
}
