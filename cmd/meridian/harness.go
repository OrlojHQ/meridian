package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
	"io"
	"net/url"
	"os"
	"strings"
)

func newHarnessCommand(config *cliConfig) *cobra.Command {
	root := &cobra.Command{Use: "harness", Short: "Import and manage your personal harness setup"}
	var yes, acceptExclusions, previewOnly bool
	command := &cobra.Command{Use: "import [claude|codex|opencode|pi]", Short: "Preview and save local harness settings for future Capsules", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		names := []string{"claude", "codex", "opencode", "pi"}
		if len(args) > 0 {
			names = args
		}
		var previews []harnesssetup.Preview
		for _, name := range names {
			preview, err := harnesssetup.Discover(home, name, os.Getenv)
			if errors.Is(err, os.ErrNotExist) && len(args) == 0 {
				continue
			}
			if err != nil {
				return err
			}
			if config.json {
				if err := writeJSON(config.stdout, preview); err != nil {
					return err
				}
			} else {
				_, _ = fmt.Fprintf(config.stdout, "\n%s: %d portable files\n", name, len(preview.Files))
				for _, pkg := range preview.Dependencies {
					_, _ = fmt.Fprintf(config.stdout, "  Install %q inside the Capsule\n", pkg)
				}
				for _, p := range preview.Files {
					_, _ = fmt.Fprintf(config.stdout, "  Include %q\n", p)
				}
				for _, warning := range preview.Warnings {
					_, _ = fmt.Fprintf(config.stdout, "  Keep %q (warning): %s\n", warning.Path, warning.Reason)
				}
				for _, issue := range preview.Issues {
					_, _ = fmt.Fprintf(config.stdout, "  Exclude %q: %s\n", issue.Path, issue.Reason)
				}
			}
			if len(preview.Files) > 0 {
				previews = append(previews, preview)
			}
		}
		if previewOnly {
			return nil
		}
		if len(previews) == 0 {
			return errors.New("no portable harness configuration found")
		}
		endpoint, err := url.Parse(config.server)
		if err != nil {
			return err
		}
		if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && (endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "localhost" || endpoint.Hostname() == "::1")) {
			return errors.New("remote setup imports require HTTPS")
		}
		if config.json && !yes {
			return errors.New("--json requires --preview or --yes")
		}
		reader := bufio.NewReader(config.stdin)
		for _, preview := range previews {
			if yes && len(preview.Issues) > 0 && !acceptExclusions {
				return errors.New("review exclusions with --preview, then pass --accept-exclusions to omit them")
			}
			if !yes {
				_, _ = fmt.Fprintf(config.stdout, "Save %s setup to %s and use it for new Capsules? Excluded items will not transfer. [y/N] ", preview.Harness, endpoint.Host)
				answer, err := reader.ReadString('\n')
				if err != nil {
					return err
				}
				if strings.ToLower(strings.TrimSpace(answer)) != "y" {
					continue
				}
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			items, err := api.ListHarnessSetups(cmd.Context())
			if err != nil {
				return apiCallError(err)
			}
			input := &client.ImportHarnessSetupRequest{Name: "My " + preview.Harness + " setup", Default: true, Bundle: client.HarnessSetupBundle{Harness: client.HarnessSetupBundleHarness(preview.Harness), Dependencies: preview.Bundle.Dependencies}}
			for _, item := range items.Items {
				if item.Harness == preview.Harness && item.Default && !item.Deleted {
					input.ID = client.NewOptString(item.ID)
					input.Name = item.Name
					input.ExpectedResourceVersion = item.ResourceVersion
					revisions, err := api.ListHarnessSetupRevisions(cmd.Context(), client.ListHarnessSetupRevisionsParams{SetupId: item.ID})
					if err != nil {
						return apiCallError(err)
					}
					for _, revision := range revisions.Items {
						if revision.ID == item.Revision && revision.Digest == preview.Digest {
							input = nil
							break
						}
					}
					break
				}
			}
			if input == nil {
				_, _ = fmt.Fprintln(config.stdout, preview.Harness+": saved setup is already current")
				continue
			}
			for _, f := range preview.Bundle.Files {
				input.Bundle.Files = append(input.Bundle.Files, client.HarnessSetupFile{Path: f.Path, Content: f.Content, Executable: client.NewOptBool(f.Executable)})
			}
			key, err := idempotencyKey("")
			if err != nil {
				return err
			}
			saved, err := api.ImportHarnessSetup(cmd.Context(), input, client.ImportHarnessSetupParams{IdempotencyKey: key})
			if err != nil {
				return apiCallError(err)
			}
			if config.json {
				if err := writeJSON(config.stdout, saved); err != nil {
					return err
				}
			} else {
				_, _ = fmt.Fprintf(config.stdout, "Saved %q. New Capsules will use this setup. Native account login is not imported.\n", saved.Name)
			}
		}
		return nil
	}}
	command.Flags().BoolVar(&yes, "yes", false, "save the preview without an interactive prompt")
	command.Flags().BoolVar(&acceptExclusions, "accept-exclusions", false, "explicitly omit reported unsupported or sensitive items")
	command.Flags().BoolVar(&previewOnly, "preview", false, "inspect local configuration without contacting Meridian")
	root.AddCommand(newHarnessConnectCommand(config), command, &cobra.Command{Use: "list", Short: "List saved personal setups", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		api, err := newAPI(config)
		if err != nil {
			return err
		}
		items, err := api.ListHarnessSetups(cmd.Context())
		if err != nil {
			return apiCallError(err)
		}
		return writeJSON(config.stdout, items)
	}})
	return root
}

// Offer changes only in an interactive local client, never in automation or on
// the remote daemon. Import performs the same preview and explicit selection.
func offerHarnessSetupUpdate(ctx context.Context, config *cliConfig, harness string) error {
	if config.json || !harnesssetup.Supported(harness) {
		return nil
	}
	input, ok := config.stdin.(*os.File)
	if !ok {
		return nil
	}
	info, err := input.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return nil
	}
	api, err := newAPI(config)
	if err != nil {
		return err
	}
	items, err := api.ListHarnessSetups(ctx)
	if err != nil {
		return apiCallError(err)
	}
	for _, item := range items.Items {
		if !item.Default || item.Deleted || item.Harness != harness {
			continue
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		preview, err := harnesssetup.Discover(home, harness, os.Getenv)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		revisions, err := api.ListHarnessSetupRevisions(ctx, client.ListHarnessSetupRevisionsParams{SetupId: item.ID})
		if err != nil {
			return apiCallError(err)
		}
		for _, revision := range revisions.Items {
			if revision.ID == item.Revision && revision.Digest == preview.Digest {
				return nil
			}
		}
		_, _ = fmt.Fprintln(config.stdout, "Your local harness setup has changed. Review an update below, or answer N to keep the saved version.")
		command := newHarnessCommand(config)
		command.SetArgs([]string{"import", harness})
		return command.ExecuteContext(ctx)
	}
	return nil
}
func newHarnessConnectCommand(config *cliConfig) *cobra.Command {
	var stdin bool
	var name, id string
	var expected int64
	cmd := &cobra.Command{Use: "connect openai|anthropic", Short: "Save a reusable API connection from stdin", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		endpoint, err := url.Parse(config.server)
		if err != nil || endpoint.User != nil || endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && (endpoint.Hostname() == "localhost" || endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "::1")) {
			return errors.New("remote provider connections require HTTPS")
		}
		if !stdin {
			return errors.New("--stdin is required; API keys must not appear in flags or command history")
		}
		if args[0] != "openai" && args[0] != "anthropic" {
			return errors.New("supported providers: openai, anthropic")
		}
		raw, err := io.ReadAll(io.LimitReader(config.stdin, 4097))
		if err != nil {
			return err
		}
		defer func() {
			for i := range raw {
				raw[i] = 0
			}
		}()
		if len(raw) > 4096 {
			return errors.New("API key exceeds limit")
		}
		if name == "" {
			name = args[0] + " API"
		}
		api, err := newAPI(config)
		if err != nil {
			return err
		}
		input := &client.ProviderConnectionRequest{Name: name, Provider: client.ProviderConnectionRequestProvider(args[0]), ApiKey: strings.TrimSpace(string(raw)), ExpectedResourceVersion: expected}
		var saved *client.ProviderConnection
		if id == "" {
			saved, err = api.CreateProviderConnection(cmd.Context(), input)
		} else {
			saved, err = api.UpdateProviderConnection(cmd.Context(), input, client.UpdateProviderConnectionParams{ConnectionId: id})
		}
		if err != nil {
			return apiCallError(err)
		}
		return writeJSON(config.stdout, saved)
	}}
	cmd.Flags().BoolVar(&stdin, "stdin", false, "read the API key from stdin")
	cmd.Flags().StringVar(&name, "name", "", "connection display name")
	cmd.Flags().StringVar(&id, "id", "", "existing connection to replace")
	cmd.Flags().Int64Var(&expected, "expected-version", 0, "existing connection version")
	return cmd
}
