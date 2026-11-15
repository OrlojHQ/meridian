package main

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/workstation"
	"github.com/spf13/cobra"
)

// newImportCommand imports this computer's setup into the caller's
// workstation profile (ADR 0049): every detected harness and the git commit
// identity, compared with the saved profile and saved in one request.
func newImportCommand(config *cliConfig) *cobra.Command {
	var yes, acceptExclusions, previewOnly, noGit bool
	command := &cobra.Command{
		Use:   "import",
		Short: "Review this computer's setup and save it as your workstation profile",
		Long: `Detects the harness configurations and git identity on this computer,
shows what will be kept and what is left out, compares them with your saved
profile, and saves them in one step. Only each harness's configuration
directory and the global git config are read. Nothing is run.

New Capsules use the saved profile. Existing Capsules keep what they pinned
until you update them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			scan, err := workstation.ScanComputer(home, os.Getenv)
			if err != nil {
				return err
			}
			if previewOnly {
				if config.json {
					return writeJSON(config.stdout, &scan)
				}
				printScan(config, scan)
				return nil
			}
			if len(scan.Harnesses) == 0 && scan.Git == nil {
				printScan(config, scan)
				return errors.New("no portable harness configuration or git identity found")
			}
			endpoint, err := importEndpoint(config)
			if err != nil {
				return err
			}
			if config.json && !yes {
				return errors.New("--json requires --preview or --yes")
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			review, err := workstation.Compare(cmd.Context(), api, scan)
			if err != nil {
				return apiCallError(err)
			}
			includeGit := !noGit && review.Git.Changed
			if !config.json {
				printReview(config, review, includeGit, endpoint.Host)
			}
			changes := includeGit
			for _, harness := range review.Harnesses {
				changes = changes || harness.Status != "unchanged"
			}
			if !changes {
				if !config.json {
					_, _ = fmt.Fprintln(config.stdout, "Your saved profile is already current.")
				}
				return nil
			}
			exclusions := review.Exclusions()
			if yes {
				if exclusions > 0 && !acceptExclusions {
					return errors.New("review exclusions with --preview, then pass --accept-exclusions to leave them out")
				}
			} else {
				_, _ = fmt.Fprintf(config.stdout, "\nSave this to your profile on %s? Excluded items will not transfer. [y/N] ", endpoint.Host)
				answer, err := bufio.NewReader(config.stdin).ReadString('\n')
				if err != nil && answer == "" {
					return err
				}
				if strings.ToLower(strings.TrimSpace(answer)) != "y" {
					_, _ = fmt.Fprintln(config.stdout, "Nothing was saved.")
					return nil
				}
			}
			result, err := workstation.Save(cmd.Context(), api, workstation.Request(review, includeGit))
			if err != nil {
				return apiCallError(err)
			}
			if config.json {
				return writeJSON(config.stdout, result)
			}
			if yes && exclusions > 0 {
				_, _ = fmt.Fprintf(config.stdout, "Accepted %d exclusions; they were not saved.\n", exclusions)
			}
			saved := []string{}
			for _, setup := range result.Saved {
				saved = append(saved, setup.Harness)
			}
			if len(saved) > 0 {
				_, _ = fmt.Fprintf(config.stdout, "Saved %s.", strings.Join(saved, ", "))
			} else {
				_, _ = fmt.Fprint(config.stdout, "Saved.")
			}
			if len(result.Unchanged) > 0 {
				_, _ = fmt.Fprintf(config.stdout, " Unchanged: %s.", strings.Join(result.Unchanged, ", "))
			}
			_, _ = fmt.Fprintln(config.stdout, "\nNew Capsules use this profile. Existing Capsules keep their pins until you update them. Native account login is not imported.")
			return nil
		},
	}
	command.Flags().BoolVar(&yes, "yes", false, "save without an interactive prompt")
	command.Flags().BoolVar(&acceptExclusions, "accept-exclusions", false, "with --yes, explicitly leave out reported unsupported or sensitive items")
	command.Flags().BoolVar(&previewOnly, "preview", false, "inspect this computer without contacting Meridian")
	command.Flags().BoolVar(&noGit, "no-git", false, "do not import the git commit identity")
	return command
}

// importEndpoint refuses to send a profile anywhere but HTTPS or this
// computer.
func importEndpoint(config *cliConfig) (*url.URL, error) {
	endpoint, err := url.Parse(config.server)
	if err != nil {
		return nil, err
	}
	loopback := endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "localhost" || endpoint.Hostname() == "::1"
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && loopback) {
		return nil, errors.New("remote profile imports require HTTPS")
	}
	return endpoint, nil
}

func printScan(config *cliConfig, scan workstation.Scan) {
	out := config.stdout
	if len(scan.Harnesses) == 0 {
		_, _ = fmt.Fprintln(out, "No harness configuration found on this computer.")
	}
	for _, preview := range scan.Harnesses {
		_, _ = fmt.Fprintf(out, "\n%s: %d portable files\n", preview.Harness, len(preview.Files))
		for _, pkg := range preview.Dependencies {
			_, _ = fmt.Fprintf(out, "  Install %q inside the Capsule\n", pkg)
		}
		for _, p := range preview.Files {
			_, _ = fmt.Fprintf(out, "  Include %q\n", p)
		}
		for _, warning := range preview.Warnings {
			_, _ = fmt.Fprintf(out, "  Keep %q (warning): %s\n", warning.Path, warning.Reason)
		}
		printExclusions(config, preview.Issues)
	}
	for _, issue := range scan.NotImported {
		_, _ = fmt.Fprintf(out, "\n%s: not imported: %s\n", issue.Path, issue.Reason)
	}
	printGit(config, scan.Git)
	_, _ = fmt.Fprintln(out, "\nHooks, plugins, and MCP servers are code that runs inside the Capsule.")
}

func printGit(config *cliConfig, git *workstation.GitSource) {
	if git == nil {
		_, _ = fmt.Fprintln(config.stdout, "\ngit: no user.name and user.email found")
		return
	}
	if git.Identity.Name != "" {
		_, _ = fmt.Fprintf(config.stdout, "\ngit: use %q <%s> from %s as your commit identity\n", git.Identity.Name, git.Identity.Email, git.Path)
	}
	for _, note := range git.Notes {
		_, _ = fmt.Fprintf(config.stdout, "  %s\n", note)
	}
}

func printReview(config *cliConfig, review workstation.Review, includeGit bool, host string) {
	out := config.stdout
	_, _ = fmt.Fprintf(out, "Comparing this computer with your profile on %s\n", host)
	for _, harness := range review.Harnesses {
		preview := harness.Preview
		_, _ = fmt.Fprintf(out, "\n%s: %s\n", preview.Harness, harness.Status)
		for _, p := range harness.Added {
			_, _ = fmt.Fprintf(out, "  + %s\n", p)
		}
		for _, p := range harness.Changed {
			_, _ = fmt.Fprintf(out, "  ~ %s\n", p)
		}
		for _, p := range harness.Removed {
			_, _ = fmt.Fprintf(out, "  - %s\n", p)
		}
		for _, pkg := range preview.Dependencies {
			_, _ = fmt.Fprintf(out, "  Install %q inside the Capsule\n", pkg)
		}
		for _, warning := range preview.Warnings {
			_, _ = fmt.Fprintf(out, "  Keep %q (warning): %s\n", warning.Path, warning.Reason)
		}
		printExclusions(config, preview.Issues)
		if harness.Connection != "" {
			_, _ = fmt.Fprintf(out, "  Login: your default connection %q\n", harness.Connection)
		} else {
			_, _ = fmt.Fprintln(out, "  Login: no default connection; Threads need one (meridian harness connect)")
		}
	}
	for _, issue := range review.NotImported {
		_, _ = fmt.Fprintf(out, "\n%s: not imported: %s\n", issue.Path, issue.Reason)
	}
	switch {
	case includeGit:
		printGit(config, review.Git.Local)
	case review.Git.Local != nil && review.Git.Local.Identity.Name != "" && !review.Git.Changed:
		_, _ = fmt.Fprintln(out, "\ngit: commit identity unchanged")
	case review.Git.Local != nil:
		for _, note := range review.Git.Local.Notes {
			_, _ = fmt.Fprintf(out, "\ngit: %s\n", note)
		}
	}
	_, _ = fmt.Fprintln(out, "\nHooks, plugins, and MCP servers are code that runs inside the Capsule.")
}

// printExclusions lists what an import leaves out. A reason shared by more
// than three items, such as runtime caches and logs, prints once with a
// count; --json lists every item.
func printExclusions(config *cliConfig, issues []harnesssetup.Issue) {
	order := []string{}
	byReason := map[string][]string{}
	for _, issue := range issues {
		if _, ok := byReason[issue.Reason]; !ok {
			order = append(order, issue.Reason)
		}
		byReason[issue.Reason] = append(byReason[issue.Reason], issue.Path)
	}
	for _, reason := range order {
		paths := byReason[reason]
		if len(paths) <= 3 {
			for _, p := range paths {
				_, _ = fmt.Fprintf(config.stdout, "  Exclude %q: %s\n", p, reason)
			}
			continue
		}
		_, _ = fmt.Fprintf(config.stdout, "  Exclude %d items: %s (e.g. %q; --json lists all)\n", len(paths), reason, paths[0])
	}
}
