package main

import (
	"encoding/json"
	"os"

	"github.com/OrlojHQ/meridian/internal/workstation"
	"github.com/spf13/cobra"
)

// newDesktopImportCommand is the desktop app's import window backend (ADR
// 0049). The app ships this binary as its gateway sidecar and runs this mode
// in the background, so desktop users never run a command. It is hidden from
// help. It is a client of a gateway: it starts no listener, opens no data
// directory, and loads no key. It reads only each pack manifest's roots and
// the global git config, with the same importer as `meridian import`.
func newDesktopImportCommand() *cobra.Command {
	var check bool
	command := &cobra.Command{
		Use:          "desktop-import",
		Short:        "Desktop app import window backend",
		Hidden:       true,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(command *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			scan := func() (workstation.Scan, error) { return workstation.ScanComputer(home, os.Getenv) }
			if check {
				// The change check: a digest of the sanitized result and
				// nothing else, with no session and no network request.
				result, err := scan()
				if err != nil {
					return err
				}
				return json.NewEncoder(command.OutOrStdout()).Encode(workstation.CheckDigest{Digest: result.Digest()})
			}
			return workstation.RunDesktopImport(command.Context(), command.InOrStdin(), command.OutOrStdout(), scan,
				func(hello workstation.DesktopHello) (workstation.DesktopAPI, error) {
					return workstation.NewDesktopClient(hello)
				})
		},
	}
	command.Flags().BoolVar(&check, "check", false, "print only the digest of this computer's sanitized setup")
	return command
}
