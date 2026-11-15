package app

import (
	"context"
	"sort"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// ProfileDiagnostic says why a Capsule differs from its owner's current
// profile (ADR 0049). Every field comes from a control-plane record: the
// pin, the saved revisions and their sealed reports, the Project's grants,
// and the profile. Nothing is asked of the Capsule, and no file contents or
// credential values are included.
type ProfileDiagnostic struct {
	CapsuleID domain.CapsuleID
	// NothingPinned is true when the Capsule took nothing from a profile:
	// it was created clean, or before the owner saved one.
	NothingPinned bool
	// MatchesProfile is true only when every harness is pinned to what a new
	// Capsule would pin, the identity matches, and no exclusion or warning
	// is outstanding. It never claims the Capsule matches the laptop.
	MatchesProfile bool
	Harnesses      []HarnessDiagnostic
	CommitAuthor   CommitAuthorDiagnostic
}

// SetupRevisionRef names one saved setup revision.
type SetupRevisionRef struct {
	SetupID   string
	SetupName string
	Revision  string
	SavedAt   time.Time
}

// HarnessDiagnostic compares one harness's pin with the current profile.
type HarnessDiagnostic struct {
	Harness string
	// Pinned is the revision the Capsule uses; nil when it pins none.
	Pinned *SetupRevisionRef
	// Current is what a new Capsule in this Project would pin; nil when
	// nothing would be pinned.
	Current *SetupRevisionRef
	// ProjectOverride is the Project's setup choice for this harness: empty
	// to follow the profile, "clean", or a setup id.
	ProjectOverride string
	FilesAdded      []string
	FilesRemoved    []string
	// Report is the pinned revision's import report. HasReport is false
	// when the revision was saved without one.
	HasReport  bool
	Exclusions []harnesssetup.Issue
	Warnings   []harnesssetup.Issue
	Connection ConnectionDiagnostic
}

// ConnectionDiagnostic says whether the Project grants a login to this
// harness, which is what the gateway checks.
type ConnectionDiagnostic struct {
	Granted      bool
	ConnectionID string
	Name         string
	Provider     string
	Revoked      bool
	NeedsSignIn  bool
}

// CommitAuthorDiagnostic says which identity a Delivery commit would use.
type CommitAuthorDiagnostic struct {
	// Source is "project", "capsule", or "none".
	Source       string
	Name         string
	Email        string
	ProfileName  string
	ProfileEmail string
}

// CapsuleProfileDiagnostic computes the diagnostic for one of the caller's
// Capsules.
func (s *Service) CapsuleProfileDiagnostic(ctx context.Context, id domain.CapsuleID) (ProfileDiagnostic, error) {
	if err := requirePrincipal(ctx); err != nil {
		return ProfileDiagnostic{}, err
	}
	if _, err := ownerOf(ctx); err != nil {
		return ProfileDiagnostic{}, err
	}
	if s.secretKey == nil {
		return ProfileDiagnostic{}, domain.ErrUnsupported
	}
	var result ProfileDiagnostic
	err := s.store.View(ctx, func(r ports.Reader) error {
		var err error
		result, err = s.profileDiagnostic(ctx, r, id)
		return err
	})
	return result, err
}

func (s *Service) profileDiagnostic(ctx context.Context, r ports.Reader, id domain.CapsuleID) (ProfileDiagnostic, error) {
	capsule, err := r.GetCapsule(ctx, id)
	if err != nil {
		return ProfileDiagnostic{}, err
	}
	project, err := r.GetProject(ctx, capsule.ProjectID)
	if err != nil {
		return ProfileDiagnostic{}, err
	}
	profile, err := r.GetUserProfile(ctx, capsule.OwnerUserID)
	if err != nil {
		return ProfileDiagnostic{}, err
	}
	items, err := r.ListHarnessSetups(ctx)
	if err != nil {
		return ProfileDiagnostic{}, err
	}
	overrides, err := r.ProjectHarnessSetups(ctx, capsule.ProjectID)
	if err != nil {
		return ProfileDiagnostic{}, err
	}
	pinnedByCapsule, err := r.ListPinnedHarnessSetups(ctx, []domain.CapsuleID{id})
	if err != nil {
		return ProfileDiagnostic{}, err
	}
	identity, hasIdentity, err := r.GetCapsuleProfilePin(ctx, id)
	if err != nil {
		return ProfileDiagnostic{}, err
	}
	// A deleted override fails a new Capsule's creation; here it just means
	// nothing current for that harness.
	current, err := resolveHarnessSetups(items, overrides)
	if err != nil {
		current, _ = resolveHarnessSetups(items, nil)
		for harness := range overrides {
			delete(current, harness)
		}
	}

	pinned := map[string]domain.PinnedHarnessSetup{}
	for _, pin := range pinnedByCapsule[id] {
		pinned[pin.Harness] = pin
	}
	harnesses := map[string]bool{}
	for harness := range pinned {
		harnesses[harness] = true
	}
	for harness := range current {
		harnesses[harness] = true
	}
	names := make([]string, 0, len(harnesses))
	for harness := range harnesses {
		names = append(names, harness)
	}
	sort.Strings(names)

	result := ProfileDiagnostic{
		CapsuleID:     id,
		NothingPinned: len(pinned) == 0 && !hasIdentity,
		Harnesses:     []HarnessDiagnostic{},
	}
	matches := !result.NothingPinned
	for _, harness := range names {
		row := HarnessDiagnostic{Harness: harness, ProjectOverride: overrides[harness], FilesAdded: []string{}, FilesRemoved: []string{},
			Exclusions: []harnesssetup.Issue{}, Warnings: []harnesssetup.Issue{}}
		var pinnedFiles, currentFiles []string
		if pin, ok := pinned[harness]; ok {
			revision, err := r.GetHarnessSetupRevision(ctx, pin.Revision)
			if err != nil {
				return ProfileDiagnostic{}, err
			}
			row.Pinned = &SetupRevisionRef{SetupID: pin.SetupID, SetupName: pin.Name, Revision: pin.Revision, SavedAt: pin.RevisedAt}
			pinnedFiles = revision.Files
			report, ok, err := s.openReport(ctx, r, revision)
			if err != nil {
				return ProfileDiagnostic{}, err
			}
			if ok {
				row.HasReport = true
				row.Exclusions = append(row.Exclusions, report.Issues...)
				row.Warnings = append(row.Warnings, report.Warnings...)
			}
		}
		if item, ok := current[harness]; ok {
			revision, err := r.GetHarnessSetupRevision(ctx, item.Revision)
			if err != nil {
				return ProfileDiagnostic{}, err
			}
			row.Current = &SetupRevisionRef{SetupID: item.ID, SetupName: item.Name, Revision: item.Revision, SavedAt: revision.CreatedAt}
			currentFiles = revision.Files
		}
		if row.Pinned != nil && row.Current != nil {
			row.FilesAdded, row.FilesRemoved = fileDifference(pinnedFiles, currentFiles)
		}
		row.Connection, err = connectionDiagnostic(ctx, r, capsule.ProjectID, harness)
		if err != nil {
			return ProfileDiagnostic{}, err
		}
		if (row.Pinned == nil) != (row.Current == nil) ||
			(row.Pinned != nil && row.Pinned.Revision != row.Current.Revision) ||
			len(row.Exclusions) > 0 || len(row.Warnings) > 0 {
			matches = false
		}
		result.Harnesses = append(result.Harnesses, row)
	}

	author := CommitAuthorDiagnostic{Source: "none", ProfileName: profile.GitName, ProfileEmail: profile.GitEmail}
	switch {
	case project.CommitAuthorName != "" && project.CommitAuthorEmail != "":
		author.Source, author.Name, author.Email = "project", project.CommitAuthorName, project.CommitAuthorEmail
	case hasIdentity && identity.GitName != "":
		author.Source, author.Name, author.Email = "capsule", identity.GitName, identity.GitEmail
	}
	result.CommitAuthor = author
	if identity.GitName != profile.GitName || identity.GitEmail != profile.GitEmail {
		matches = false
	}
	result.MatchesProfile = matches
	return result, nil
}

// fileDifference lists the files current has that pinned lacks, and the
// reverse.
func fileDifference(pinned, current []string) ([]string, []string) {
	inPinned := map[string]bool{}
	for _, path := range pinned {
		inPinned[path] = true
	}
	inCurrent := map[string]bool{}
	added := []string{}
	for _, path := range current {
		inCurrent[path] = true
		if !inPinned[path] {
			added = append(added, path)
		}
	}
	removed := []string{}
	for _, path := range pinned {
		if !inCurrent[path] {
			removed = append(removed, path)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func connectionDiagnostic(ctx context.Context, r ports.Reader, project domain.ProjectID, harness string) (ConnectionDiagnostic, error) {
	id, err := r.GetProjectProviderConnection(ctx, project, harness)
	if err != nil || id == "" {
		return ConnectionDiagnostic{}, err
	}
	connection, err := r.GetProviderConnection(ctx, id)
	if err != nil {
		return ConnectionDiagnostic{}, err
	}
	return ConnectionDiagnostic{
		Granted: true, ConnectionID: id, Name: connection.Name, Provider: connection.Provider,
		Revoked: connection.Revoked, NeedsSignIn: connection.NeedsSignIn,
	}, nil
}
