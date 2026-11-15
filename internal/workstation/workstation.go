// Package workstation is the laptop side of a workstation profile import
// (ADR 0049): which harness configurations and which git identity this
// computer has, how they differ from the saved profile, and the one atomic
// request that saves them. The terminal command and the desktop app's hidden
// import mode both use it, so they detect, sanitize, and compare the same way.
package workstation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/pkg/client"
)

// Scan is what this computer has, after the importer's allowlist, deny list,
// and sanitizer. Nothing in it has left the laptop.
type Scan struct {
	Harnesses []harnesssetup.Preview `json:"harnesses"`
	// NotImported lists harnesses whose configuration exists but cannot be
	// imported, such as one over the bundle limit.
	NotImported []harnesssetup.Issue `json:"notImported"`
	Git         *GitSource           `json:"git,omitempty"`
	// home is where the scan ran, so reports can be redacted for this
	// computer's actual home directory. It is never serialized.
	home string
}

// ScanComputer walks only each pack manifest's roots and the global git
// config. It never walks the rest of home and executes nothing.
func ScanComputer(home string, env func(string) string) (Scan, error) {
	scan := Scan{Harnesses: []harnesssetup.Preview{}, NotImported: []harnesssetup.Issue{}, home: home}
	for _, harness := range harnesssetup.Harnesses() {
		preview, err := harnesssetup.Discover(home, harness, env)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			scan.NotImported = append(scan.NotImported, harnesssetup.Issue{Path: harness, Reason: err.Error()})
			continue
		}
		if len(preview.Files) == 0 {
			continue
		}
		scan.Harnesses = append(scan.Harnesses, preview)
	}
	git, err := DiscoverGitIdentity(home, env)
	if err != nil {
		return Scan{}, err
	}
	scan.Git = git
	return scan, nil
}

// Digest identifies a scan's sanitized result: each harness's bundle digest
// and the git identity. Equal digests mean nothing a Save would send has
// changed. It is safe to store locally; it reveals no file contents.
func (s Scan) Digest() string {
	parts := []string{}
	for _, preview := range s.Harnesses {
		parts = append(parts, "harness\x00"+preview.Harness+"\x00"+preview.Digest)
	}
	for _, issue := range s.NotImported {
		parts = append(parts, "not-imported\x00"+issue.Path)
	}
	if s.Git != nil {
		parts = append(parts, "git\x00"+s.Git.Identity.Name+"\x00"+s.Git.Identity.Email)
	}
	sort.Strings(parts)
	sum := sha256.New()
	for _, part := range parts {
		sum.Write([]byte(part))
		sum.Write([]byte{0xff})
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// API is the part of the Meridian client a review and save need.
type API interface {
	GetProfile(ctx context.Context) (client.GetProfileRes, error)
	GetHarnessSetupContents(ctx context.Context, params client.GetHarnessSetupContentsParams) (*client.ImportHarnessSetupRequest, error)
	ListProviderConnections(ctx context.Context) (*client.ProviderConnectionPage, error)
	ImportProfile(ctx context.Context, request *client.ImportProfileRequest, params client.ImportProfileParams) (client.ImportProfileRes, error)
}

// HarnessReview compares one harness on this computer with the saved
// profile.
type HarnessReview struct {
	Preview harnesssetup.Preview `json:"preview"`
	// Status is "new" (nothing saved yet), "changed", or "unchanged".
	Status  string   `json:"status"`
	Added   []string `json:"added"`
	Changed []string `json:"changed"`
	Removed []string `json:"removed"`
	// Connection is the name of the profile's default connection for this
	// harness, empty when it has none.
	Connection string `json:"connection"`
}

// GitReview compares the local identity with the saved one.
type GitReview struct {
	Local   *GitSource               `json:"local,omitempty"`
	Saved   harnesssetup.GitIdentity `json:"saved"`
	Changed bool                     `json:"changed"`
}

// Review is a scan compared with the caller's saved profile on one server.
type Review struct {
	Harnesses      []HarnessReview      `json:"harnesses"`
	NotImported    []harnesssetup.Issue `json:"notImported"`
	Git            GitReview            `json:"git"`
	ProfileVersion int64                `json:"profileVersion"`
	home           string
}

// Exclusions counts what the review leaves out, which an unattended save
// must accept explicitly.
func (r Review) Exclusions() int {
	count := 0
	for _, harness := range r.Harnesses {
		count += len(harness.Preview.Issues)
	}
	return count
}

// Changed reports whether saving would change the profile.
func (r Review) Changed() bool {
	if r.Git.Changed {
		return true
	}
	for _, harness := range r.Harnesses {
		if harness.Status != "unchanged" {
			return true
		}
	}
	return false
}

// Compare reads the caller's saved profile and the contents of its default
// setups, and diffs each against this computer. The fetched contents stay in
// memory and are not returned.
func Compare(ctx context.Context, api API, scan Scan) (Review, error) {
	response, err := api.GetProfile(ctx)
	if err != nil {
		return Review{}, err
	}
	profile, ok := response.(*client.Profile)
	if !ok {
		return Review{}, errors.New("profile could not be read")
	}
	names := map[string]string{}
	if len(profile.DefaultConnections) > 0 {
		page, err := api.ListProviderConnections(ctx)
		if err != nil {
			return Review{}, err
		}
		for _, connection := range page.Items {
			names[connection.ID] = connection.Name
		}
	}
	saved := map[string]client.HarnessSetup{}
	for _, setup := range profile.Setups {
		saved[setup.Harness] = setup
	}
	review := Review{
		Harnesses: []HarnessReview{}, NotImported: append([]harnesssetup.Issue{}, scan.NotImported...),
		ProfileVersion: profile.ResourceVersion, home: scan.home,
		Git: GitReview{Local: scan.Git, Saved: harnesssetup.GitIdentity{Name: profile.GitName, Email: profile.GitEmail}},
	}
	if scan.Git != nil && scan.Git.Identity.Name != "" && scan.Git.Identity != review.Git.Saved {
		review.Git.Changed = true
	}
	for _, preview := range scan.Harnesses {
		row := HarnessReview{Preview: preview, Status: "new", Added: []string{}, Changed: []string{}, Removed: []string{}}
		if id, ok := profile.DefaultConnections[preview.Harness]; ok {
			row.Connection = names[id]
			if row.Connection == "" {
				row.Connection = id
			}
		}
		setup, ok := saved[preview.Harness]
		if !ok {
			row.Added = append(row.Added, preview.Files...)
			review.Harnesses = append(review.Harnesses, row)
			continue
		}
		contents, err := api.GetHarnessSetupContents(ctx, client.GetHarnessSetupContentsParams{SetupId: setup.ID})
		if err != nil {
			return Review{}, err
		}
		row.Added, row.Changed, row.Removed = diffFiles(contents.Bundle.Files, preview.Bundle)
		row.Status = "changed"
		if len(row.Added)+len(row.Changed)+len(row.Removed) == 0 && sameDependencies(contents.Bundle.Dependencies, preview.Bundle.Dependencies) {
			row.Status = "unchanged"
		}
		review.Harnesses = append(review.Harnesses, row)
	}
	return review, nil
}

func diffFiles(saved []client.HarnessSetupFile, local harnesssetup.Bundle) ([]string, []string, []string) {
	before := map[string]client.HarnessSetupFile{}
	for _, file := range saved {
		before[file.Path] = file
	}
	added, changed, removed := []string{}, []string{}, []string{}
	seen := map[string]bool{}
	for _, file := range local.Files {
		seen[file.Path] = true
		old, ok := before[file.Path]
		switch {
		case !ok:
			added = append(added, file.Path)
		case old.Content != file.Content || old.Executable.Or(false) != file.Executable:
			changed = append(changed, file.Path)
		}
	}
	for _, file := range saved {
		if !seen[file.Path] {
			removed = append(removed, file.Path)
		}
	}
	sort.Strings(added)
	sort.Strings(changed)
	sort.Strings(removed)
	return added, changed, removed
}

func sameDependencies(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Request is the one atomic import a reviewed save sends. Unchanged
// harnesses are sent too; the server leaves them alone. The git identity is
// sent only when includeGit is set and it changed.
func Request(review Review, includeGit bool) *client.ImportProfileRequest {
	request := &client.ImportProfileRequest{Setups: []client.ProfileImportSetup{}, ExpectedResourceVersion: review.ProfileVersion}
	for _, harness := range review.Harnesses {
		preview := harness.Preview
		bundle := client.HarnessSetupBundle{
			Harness:      client.HarnessSetupBundleHarness(preview.Harness),
			Dependencies: preview.Bundle.Dependencies,
			Files:        []client.HarnessSetupFile{},
		}
		for _, file := range preview.Bundle.Files {
			bundle.Files = append(bundle.Files, client.HarnessSetupFile{Path: file.Path, Content: file.Content, Executable: client.NewOptBool(file.Executable)})
		}
		// Laptop home paths are redacted before the report leaves this
		// computer; the server redacts again before sealing.
		redacted := redactHome(preview.Report(), review.home).Redacted()
		report := client.HarnessSetupReport{Issues: []client.HarnessSetupIssue{}, Warnings: []client.HarnessSetupIssue{}}
		for _, issue := range redacted.Issues {
			report.Issues = append(report.Issues, client.HarnessSetupIssue{Path: issue.Path, Reason: issue.Reason})
		}
		for _, warning := range redacted.Warnings {
			report.Warnings = append(report.Warnings, client.HarnessSetupIssue{Path: warning.Path, Reason: warning.Reason})
		}
		request.Setups = append(request.Setups, client.ProfileImportSetup{Bundle: bundle, Report: report})
	}
	if includeGit && review.Git.Changed && review.Git.Local != nil {
		request.Git = client.NewOptGitIdentity(client.GitIdentity{Name: review.Git.Local.Identity.Name, Email: review.Git.Local.Identity.Email})
	}
	return request
}

// Save sends one reviewed import.
func Save(ctx context.Context, api API, request *client.ImportProfileRequest) (*client.ProfileImportResult, error) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	response, err := api.ImportProfile(ctx, request, client.ImportProfileParams{IdempotencyKey: "profile-import-" + hex.EncodeToString(key)})
	if err != nil {
		return nil, err
	}
	switch value := response.(type) {
	case *client.ProfileImportResult:
		return value, nil
	case *client.ImportProfileConflict:
		return nil, errors.New("your profile changed since this review; run the import again")
	case *client.ImportProfileUnprocessableEntity:
		return nil, fmt.Errorf("the server refused this import: %s", value.Error.Message)
	default:
		return nil, errors.New("the server refused this import")
	}
}

// redactHome replaces this computer's home directory, wherever it is, and
// everything after it up to the next location separator with "~/…".
func redactHome(report harnesssetup.Report, home string) harnesssetup.Report {
	if home == "" || home == "/" {
		return report
	}
	pattern := regexp.MustCompile(regexp.QuoteMeta(strings.TrimSuffix(home, "/")) + `(?:/[^:]*)?`)
	redact := func(items []harnesssetup.Issue) []harnesssetup.Issue {
		out := make([]harnesssetup.Issue, 0, len(items))
		for _, item := range items {
			out = append(out, harnesssetup.Issue{Path: pattern.ReplaceAllString(item.Path, "~/…"), Reason: pattern.ReplaceAllString(item.Reason, "~/…")})
		}
		return out
	}
	return harnesssetup.Report{Issues: redact(report.Issues), Warnings: redact(report.Warnings)}
}
