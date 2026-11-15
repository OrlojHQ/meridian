package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// Profile is the caller's workstation profile (ADR 0049): the stored row
// plus the owner's current default setup for each harness.
type Profile struct {
	domain.UserProfile
	Setups []domain.HarnessSetup
}

// ProfileUpdate replaces the git identity and default connections.
type ProfileUpdate struct {
	GitName                 string                 `json:"gitName"`
	GitEmail                string                 `json:"gitEmail"`
	DefaultConnections      map[string]string      `json:"defaultConnections"`
	ExpectedResourceVersion domain.ResourceVersion `json:"expectedResourceVersion"`
}

// GitIdentity is a commit author's name and email, nothing else from a git
// config.
type GitIdentity = harnesssetup.GitIdentity

// ProfileImportSetup is one harness's reviewed bundle and the report of what
// its import left out.
type ProfileImportSetup struct {
	Bundle harnesssetup.Bundle `json:"bundle"`
	Report harnesssetup.Report `json:"report"`
}

// ProfileImport is one reviewed import of a workstation, saved atomically.
type ProfileImport struct {
	Setups []ProfileImportSetup `json:"setups"`
	// Git, when set, replaces the profile's git identity.
	Git                     *GitIdentity           `json:"git,omitempty"`
	ExpectedResourceVersion domain.ResourceVersion `json:"expectedResourceVersion"`
}

// ProfileImportResult says which harnesses got a new revision and which
// were already current.
type ProfileImportResult struct {
	Profile   Profile
	Saved     []domain.HarnessSetup
	Unchanged []string
}

type profileUpdateReplay struct {
	Hash    string
	Profile domain.UserProfile
}

type profileImportReplay struct {
	Hash      string
	Saved     []domain.HarnessSetup
	Unchanged []string
}

type capsuleProfileReplay struct {
	Hash string
}

// reportEnvelopeID binds a report to its revision's owner, under an id no
// bundle envelope uses, so a report cannot be opened as a bundle or as
// another user's report.
func reportEnvelopeID(revision domain.HarnessSetupRevision) string {
	return setupEnvelopeID(revision) + ":report"
}

// GetProfile returns the caller's workstation profile. A user who never
// saved one gets an empty profile at resource version zero.
func (s *Service) GetProfile(ctx context.Context) (Profile, error) {
	if err := requirePrincipal(ctx); err != nil {
		return Profile{}, err
	}
	owner, err := ownerOf(ctx)
	if err != nil {
		return Profile{}, err
	}
	var result Profile
	err = s.store.View(ctx, func(r ports.Reader) error {
		var err error
		result, err = readProfile(ctx, r, owner)
		return err
	})
	return result, err
}

func readProfile(ctx context.Context, r ports.Reader, owner domain.UserID) (Profile, error) {
	stored, err := r.GetUserProfile(ctx, owner)
	if err != nil {
		return Profile{}, err
	}
	items, err := r.ListHarnessSetups(ctx)
	if err != nil {
		return Profile{}, err
	}
	result := Profile{UserProfile: stored, Setups: []domain.HarnessSetup{}}
	for _, item := range items {
		if item.Default && !item.Deleted && item.OwnerUserID == owner {
			result.Setups = append(result.Setups, item)
		}
	}
	sort.Slice(result.Setups, func(i, j int) bool { return result.Setups[i].Harness < result.Setups[j].Harness })
	return result, nil
}

// validDefaultConnections checks that each default names one of the
// caller's live connections that its harness accepts.
func validDefaultConnections(ctx context.Context, r ports.Reader, defaults map[string]string) error {
	if len(defaults) > len(harnesssetup.Harnesses()) {
		return domain.ErrInvalid
	}
	for harness, id := range defaults {
		if !harnesssetup.ConnectionHarness(harness) || id == "" {
			return domain.ErrInvalid
		}
		connection, err := r.GetProviderConnection(ctx, id)
		if err != nil {
			return err
		}
		if connection.Revoked {
			return fmt.Errorf("%w: default connection was disconnected", domain.ErrConflict)
		}
		if !harnesssetup.ConnectionSupported(harness, connection.Provider, connection.DefaultModel) {
			return fmt.Errorf("%w: %s does not accept this connection", domain.ErrInvalid, harness)
		}
	}
	return nil
}

func sameConnections(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for harness, id := range a {
		if b[harness] != id {
			return false
		}
	}
	return true
}

// UpdateProfile replaces the caller's git identity and default connections.
// A default connection is a suggestion, never a grant: no Project grant
// changes here (ADR 0049).
func (s *Service) UpdateProfile(ctx context.Context, input ProfileUpdate, key string) (Profile, error) {
	if err := requirePrincipal(ctx); err != nil {
		return Profile{}, err
	}
	owner, err := ownerOf(ctx)
	if err != nil {
		return Profile{}, err
	}
	if err := requireIdempotency(key); err != nil {
		return Profile{}, err
	}
	if !domain.ValidGitIdentity(input.GitName, input.GitEmail) {
		return Profile{}, fmt.Errorf("%w: git identity", domain.ErrInvalid)
	}
	if input.DefaultConnections == nil {
		input.DefaultConnections = map[string]string{}
	}
	fingerprint := setupFingerprint(input)
	connectionsChanged := false
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		const scope = "profile:update"
		if replay, ok, err := getReplay[profileUpdateReplay](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			if replay.Hash != fingerprint {
				return domain.ErrConflict
			}
			return nil
		}
		current, err := tx.GetUserProfile(ctx, owner)
		if err != nil {
			return err
		}
		if current.ResourceVersion != input.ExpectedResourceVersion {
			return domain.ErrConflict
		}
		if err := validDefaultConnections(ctx, tx, input.DefaultConnections); err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		next := domain.UserProfile{
			OwnerUserID: owner, GitName: input.GitName, GitEmail: input.GitEmail,
			DefaultConnections: input.DefaultConnections,
			ResourceVersion:    current.ResourceVersion + 1, UpdatedAt: now,
		}
		if err := tx.PutUserProfile(ctx, next, current.ResourceVersion); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "profile", string(owner), "profile.updated", next.ResourceVersion, nil); err != nil {
			return err
		}
		if !sameConnections(current.DefaultConnections, next.DefaultConnections) {
			connectionsChanged = true
			if err := s.audit(ctx, tx, domain.AuditProfileDefaultConnections, "profile", string(owner)); err != nil {
				return err
			}
		}
		return putReplay(ctx, tx, scope, key, profileUpdateReplay{Hash: fingerprint, Profile: next}, now)
	})
	if err != nil {
		if connectionsChanged {
			s.auditFailure(ctx, domain.AuditProfileDefaultConnections, "profile", string(owner), err)
		}
		return Profile{}, err
	}
	return s.GetProfile(ctx)
}

func validateProfileImport(input ProfileImport) error {
	if len(input.Setups) == 0 && input.Git == nil {
		return fmt.Errorf("%w: nothing to import", domain.ErrInvalid)
	}
	if len(input.Setups) > len(harnesssetup.Harnesses()) {
		return fmt.Errorf("%w: too many harnesses", domain.ErrInvalid)
	}
	seen := map[string]bool{}
	for _, setup := range input.Setups {
		if err := setup.Bundle.Validate(); err != nil {
			return fmt.Errorf("%w: %s", domain.ErrInvalid, err)
		}
		if seen[setup.Bundle.Harness] {
			return fmt.Errorf("%w: %s appears twice", domain.ErrInvalid, setup.Bundle.Harness)
		}
		seen[setup.Bundle.Harness] = true
		if err := setup.Report.Validate(); err != nil {
			return fmt.Errorf("%w: %s", domain.ErrInvalid, err)
		}
	}
	if input.Git != nil && !domain.ValidGitIdentity(input.Git.Name, input.Git.Email) {
		return fmt.Errorf("%w: git identity", domain.ErrInvalid)
	}
	return nil
}

// ImportProfile saves one reviewed workstation import in one transaction:
// a new default revision for each harness whose bundle changed, each with
// its sealed report, and the git identity. A failure saves nothing.
func (s *Service) ImportProfile(ctx context.Context, input ProfileImport, key string) (ProfileImportResult, error) {
	if err := requirePrincipal(ctx); err != nil {
		return ProfileImportResult{}, err
	}
	owner, err := ownerOf(ctx)
	if err != nil {
		return ProfileImportResult{}, err
	}
	if s.secretKey == nil {
		return ProfileImportResult{}, domain.ErrUnsupported
	}
	if err := requireIdempotency(key); err != nil {
		return ProfileImportResult{}, err
	}
	if err := validateProfileImport(input); err != nil {
		return ProfileImportResult{}, err
	}
	fingerprint := setupFingerprint(input)
	var replay profileImportReplay
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		const scope = "profile:import"
		if saved, ok, err := getReplay[profileImportReplay](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			if saved.Hash != fingerprint {
				return domain.ErrConflict
			}
			replay = saved
			return nil
		}
		current, err := tx.GetUserProfile(ctx, owner)
		if err != nil {
			return err
		}
		if current.ResourceVersion != input.ExpectedResourceVersion {
			return domain.ErrConflict
		}
		items, err := tx.ListHarnessSetups(ctx)
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		replay = profileImportReplay{Hash: fingerprint, Saved: []domain.HarnessSetup{}, Unchanged: []string{}}
		for _, setup := range input.Setups {
			saved, changed, err := s.importProfileSetup(ctx, tx, items, setup, now)
			if err != nil {
				return err
			}
			if changed {
				replay.Saved = append(replay.Saved, saved)
			} else {
				replay.Unchanged = append(replay.Unchanged, setup.Bundle.Harness)
			}
		}
		next := current
		next.OwnerUserID = owner
		if input.Git != nil {
			next.GitName, next.GitEmail = input.Git.Name, input.Git.Email
		}
		next.ResourceVersion = current.ResourceVersion + 1
		next.UpdatedAt = now
		if err := tx.PutUserProfile(ctx, next, current.ResourceVersion); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "profile", string(owner), "profile.updated", next.ResourceVersion, nil); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditProfileImport, "profile", string(owner)); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, replay, now)
	})
	if err != nil {
		s.auditFailure(ctx, domain.AuditProfileImport, "profile", string(owner), err)
		return ProfileImportResult{}, err
	}
	profile, err := s.GetProfile(ctx)
	if err != nil {
		return ProfileImportResult{}, err
	}
	return ProfileImportResult{Profile: profile, Saved: replay.Saved, Unchanged: replay.Unchanged}, nil
}

// importProfileSetup saves one harness of a profile import as the owner's
// default. A bundle identical to the current default is left alone.
func (s *Service) importProfileSetup(
	ctx context.Context, tx ports.Transaction, items []domain.HarnessSetup, setup ProfileImportSetup, now time.Time,
) (domain.HarnessSetup, bool, error) {
	harness := setup.Bundle.Harness
	input := SetupImport{Name: "My " + harness + " setup", Bundle: setup.Bundle, Default: true}
	for _, item := range items {
		if item.Harness != harness || !item.Default || item.Deleted {
			continue
		}
		revision, err := tx.GetHarnessSetupRevision(ctx, item.Revision)
		if err != nil {
			return domain.HarnessSetup{}, false, err
		}
		if revision.Digest == setup.Bundle.Digest() {
			return item, false, nil
		}
		input.ID, input.Name, input.ExpectedResourceVersion = item.ID, item.Name, item.ResourceVersion
	}
	saved, err := s.saveSetupRevision(ctx, tx, items, input, now)
	if err != nil {
		return domain.HarnessSetup{}, false, err
	}
	// A client should already have redacted laptop paths; this makes sure
	// no home directory, username, or project name is ever sealed.
	raw, err := json.Marshal(setup.Report.Redacted())
	if err != nil {
		return domain.HarnessSetup{}, false, err
	}
	revision := domain.HarnessSetupRevision{OwnerUserID: saved.OwnerUserID, ID: saved.Revision}
	nonce, ciphertext, err := s.secretKey.SealSetup(reportEnvelopeID(revision), raw)
	if err != nil {
		return domain.HarnessSetup{}, false, err
	}
	if err := tx.InsertHarnessSetupReport(ctx, domain.HarnessSetupReport{
		RevisionID: saved.Revision, KeyID: s.secretKey.ID, Nonce: nonce, Ciphertext: ciphertext,
	}); err != nil {
		return domain.HarnessSetup{}, false, err
	}
	return saved, true, nil
}

// openReport opens a revision's sealed report. A revision saved without one
// reports false.
func (s *Service) openReport(ctx context.Context, r ports.Reader, revision domain.HarnessSetupRevision) (harnesssetup.Report, bool, error) {
	sealed, err := r.GetHarnessSetupReport(ctx, revision.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return harnesssetup.Report{}, false, nil
	}
	if err != nil {
		return harnesssetup.Report{}, false, err
	}
	raw, err := s.secretKey.OpenSetup(reportEnvelopeID(revision), sealed.KeyID, sealed.Nonce, sealed.Ciphertext)
	if err != nil {
		return harnesssetup.Report{}, false, err
	}
	defer wipe(raw)
	var report harnesssetup.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return harnesssetup.Report{}, false, domain.ErrCorrupt
	}
	if err := report.Validate(); err != nil {
		return harnesssetup.Report{}, false, domain.ErrCorrupt
	}
	return report, true, nil
}

// pinProfileIdentity copies the owner's profile identity onto a new Capsule.
func pinProfileIdentity(ctx context.Context, tx ports.Transaction, capsule domain.Capsule, now time.Time) error {
	profile, err := tx.GetUserProfile(ctx, capsule.OwnerUserID)
	if err != nil {
		return err
	}
	if profile.GitName == "" {
		return nil
	}
	return tx.PutCapsuleProfilePin(ctx, domain.CapsuleProfilePin{
		CapsuleID: capsule.ID, GitName: profile.GitName, GitEmail: profile.GitEmail, PinnedAt: now,
	})
}

// UpdateCapsuleProfile re-pins a Capsule to the owner's current profile:
// the setups a new Capsule in its Project would pin, and the git identity.
// It is refused while a Run or Thread session is active, so a harness's
// home never changes under it. Only the owner can ask; a Capsule cannot
// re-pin itself.
func (s *Service) UpdateCapsuleProfile(ctx context.Context, id domain.CapsuleID, key string) (ProfileDiagnostic, error) {
	if err := requirePrincipal(ctx); err != nil {
		return ProfileDiagnostic{}, err
	}
	if _, err := ownerOf(ctx); err != nil {
		return ProfileDiagnostic{}, err
	}
	if err := requireIdempotency(key); err != nil {
		return ProfileDiagnostic{}, err
	}
	fingerprint := setupFingerprint(id)
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		const scope = "capsule:profile-update"
		if replay, ok, err := getReplay[capsuleProfileReplay](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			if replay.Hash != fingerprint {
				return domain.ErrConflict
			}
			return nil
		}
		capsule, err := tx.GetCapsule(ctx, id)
		if err != nil {
			return err
		}
		project, err := tx.GetProject(ctx, capsule.ProjectID)
		if err != nil {
			return err
		}
		if err := projectAcceptsWork(project); err != nil {
			return err
		}
		if capsule.State == domain.CapsuleDeleting || capsule.State == domain.CapsuleDeleted {
			return domain.ErrIllegalTransition
		}
		if active, err := tx.HasActiveRun(ctx, id); err != nil {
			return err
		} else if active {
			return fmt.Errorf("%w: stop the running session before updating this Capsule", domain.ErrConflict)
		}
		if _, err := tx.GetActiveThread(ctx, id); err == nil {
			return fmt.Errorf("%w: stop the running session before updating this Capsule", domain.ErrConflict)
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if err := tx.UnpinHarnessSetups(ctx, id); err != nil {
			return err
		}
		if err := tx.DeleteCapsuleProfilePin(ctx, id); err != nil {
			return err
		}
		if err := s.pinHarnessSetups(ctx, tx, id, ""); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "capsule", string(id), "capsule.profile_updated", capsule.ResourceVersion, nil); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditCapsuleProfileUpdate, "capsule", string(id)); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, capsuleProfileReplay{Hash: fingerprint}, s.clock.Now())
	})
	if err != nil {
		s.auditFailure(ctx, domain.AuditCapsuleProfileUpdate, "capsule", string(id), err)
		return ProfileDiagnostic{}, err
	}
	return s.CapsuleProfileDiagnostic(ctx, id)
}

// commitAuthor is the author a Delivery commit uses: the Project's, or else
// the identity the Capsule pinned from its owner's profile (ADR 0049).
// Empty means none, and a commit is refused.
func (s *Service) commitAuthor(ctx context.Context, project domain.Project, capsule domain.CapsuleID) (string, string, error) {
	if project.CommitAuthorName != "" && project.CommitAuthorEmail != "" {
		return project.CommitAuthorName, project.CommitAuthorEmail, nil
	}
	var pin domain.CapsuleProfilePin
	var ok bool
	err := s.store.View(ctx, func(r ports.Reader) error {
		var err error
		pin, ok, err = r.GetCapsuleProfilePin(ctx, capsule)
		return err
	})
	if err != nil || !ok {
		return "", "", err
	}
	return pin.GitName, pin.GitEmail, nil
}

// runtimeGitIdentity is the identity a Capsule pinned, sent to capsuled at
// each session start.
func (s *Service) runtimeGitIdentity(ctx context.Context, id domain.CapsuleID) (*harnesssetup.GitIdentity, error) {
	var pin domain.CapsuleProfilePin
	var ok bool
	err := s.store.View(ctx, func(r ports.Reader) error {
		var err error
		pin, ok, err = r.GetCapsuleProfilePin(ctx, id)
		return err
	})
	if err != nil || !ok || pin.GitName == "" {
		return nil, err
	}
	return &harnesssetup.GitIdentity{Name: pin.GitName, Email: pin.GitEmail}, nil
}

var errCommitAuthorRequired = fmt.Errorf("%w: a commit author is required; set one on the Project or in your profile", domain.ErrIllegalTransition)
