package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
)

// noCredential is the security source for operations that need none, such
// as starting meridian login before any token exists.
type noCredential struct{}

func (noCredential) BearerAuth(context.Context, client.OperationName) (client.BearerAuth, error) {
	return client.BearerAuth{}, errors.New("this command runs without a credential")
}

// defaultMemberTokenFile is where meridian login keeps a member's token.
func defaultMemberTokenFile() string {
	directory, err := os.UserConfigDir()
	if err != nil {
		directory = filepath.Join(".", "data")
	} else {
		directory = filepath.Join(directory, "meridian")
	}
	return apiauth.DefaultMemberTokenPath(directory)
}

func newLoginCommand(config *cliConfig) *cobra.Command {
	var output string
	command := &cobra.Command{
		Use:   "login",
		Short: "Sign in to a team mode installation and save a personal access token",
		Long: "Sign in with the installation's identity provider using a device code (ADR 0046).\n" +
			"Open the address shown, enter the code, and approve. The resulting personal access\n" +
			"token is saved with mode 0600 and used by later commands. If the identity provider\n" +
			"has no device sign-in, mint a token under Account in the web UI instead.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			api, err := client.NewClient(config.server, noCredential{})
			if err != nil {
				return err
			}
			started, err := api.BeginDeviceLogin(command.Context())
			if err != nil {
				return apiCallError(err)
			}
			login, ok := started.(*client.DeviceLogin)
			if !ok {
				return responseError(started)
			}
			address := login.VerificationUri
			if complete, ok := login.VerificationUriComplete.Get(); ok && complete != "" {
				address = complete
			}
			if _, err := fmt.Fprintf(command.ErrOrStderr(), "Open %s and enter the code %s\n", address, login.UserCode); err != nil {
				return err
			}
			interval := time.Duration(login.IntervalSeconds) * time.Second
			for {
				if interval < time.Second {
					interval = 5 * time.Second
				}
				select {
				case <-command.Context().Done():
					return command.Context().Err()
				case <-time.After(interval):
				}
				polled, err := api.PollDeviceLogin(command.Context(), &client.PollDeviceLoginReq{Handle: login.Handle})
				if err != nil {
					return apiCallError(err)
				}
				switch result := polled.(type) {
				case *client.PollDeviceLoginAccepted:
					interval = time.Duration(result.IntervalSeconds) * time.Second
				case *client.PollDeviceLoginOK:
					if err := apiauth.WritePersonalTokenFile(output, result.Token); err != nil {
						return fmt.Errorf("save personal access token: %w", err)
					}
					_, err := fmt.Fprintf(command.ErrOrStderr(), "Signed in. Token saved to %s\n", output)
					return err
				case *client.PollDeviceLoginForbidden:
					return errors.New("sign-in was refused or the code expired")
				default:
					return responseError(polled)
				}
			}
		},
	}
	command.Flags().StringVar(&output, "output", defaultMemberTokenFile(), "where to save the personal access token")
	return command
}
