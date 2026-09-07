package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/ports"
	"strings"
)

type setupReplay struct {
	Hash  string
	Setup domain.HarnessSetup
}

func setupFingerprint(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type SetupImport struct {
	ID                      string                 `json:"id,omitempty"`
	Name                    string                 `json:"name"`
	Bundle                  harnesssetup.Bundle    `json:"bundle"`
	Default                 bool                   `json:"default"`
	ExpectedResourceVersion domain.ResourceVersion `json:"expectedResourceVersion"`
}
type SetupMutation struct {
	Name                    string                 `json:"name,omitempty"`
	Revision                string                 `json:"revision,omitempty"`
	Default                 *bool                  `json:"default,omitempty"`
	Deleted                 bool                   `json:"deleted,omitempty"`
	ExpectedResourceVersion domain.ResourceVersion `json:"expectedResourceVersion"`
}

func (s *Service) ListHarnessSetups(ctx context.Context) ([]domain.HarnessSetup, error) {
	var items []domain.HarnessSetup
	err := s.store.View(ctx, func(r ports.Reader) error { var err error; items, err = r.ListHarnessSetups(ctx); return err })
	return items, err
}
func findSetup(items []domain.HarnessSetup, id string) (domain.HarnessSetup, error) {
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return domain.HarnessSetup{}, domain.ErrNotFound
}
func (s *Service) ImportHarnessSetup(ctx context.Context, input SetupImport, key string) (domain.HarnessSetup, error) {
	if s.secretKey == nil {
		return domain.HarnessSetup{}, domain.ErrUnsupported
	}
	if err := input.Bundle.Validate(); err != nil {
		return domain.HarnessSetup{}, fmt.Errorf("%w: %s", domain.ErrInvalid, err)
	}
	name, err := requireName(input.Name)
	if err != nil {
		return domain.HarnessSetup{}, err
	}
	if err := requireIdempotency(key); err != nil {
		return domain.HarnessSetup{}, err
	}
	var result domain.HarnessSetup
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		scope := "setup:import"
		fingerprint := setupFingerprint(input)
		if replay, ok, err := getReplay[setupReplay](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			if replay.Hash != fingerprint {
				return domain.ErrConflict
			}
			result = replay.Setup
			return nil
		}
		items, err := tx.ListHarnessSetups(ctx)
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		if input.ID == "" {
			if input.ExpectedResourceVersion != 0 {
				return domain.ErrConflict
			}
			if len(items) >= 128 {
				return fmt.Errorf("%w: setup limit reached", domain.ErrInvalid)
			}
			result = domain.HarnessSetup{ID: s.ids.NewID(), Harness: input.Bundle.Harness, CreatedAt: now, ResourceVersion: 1}
		} else {
			result, err = findSetup(items, input.ID)
			if err != nil {
				return err
			}
			if result.Deleted || result.Harness != input.Bundle.Harness || result.ResourceVersion != input.ExpectedResourceVersion {
				return domain.ErrConflict
			}
			result.ResourceVersion++
		}
		result.Name = name
		result.Default = input.Default
		result.Revision = s.ids.NewID()
		if result.Default {
			for _, item := range items {
				if item.Harness == result.Harness && item.Default && item.ID != result.ID {
					item.Default = false
					item.ResourceVersion++
					if err := tx.PutHarnessSetup(ctx, item); err != nil {
						return err
					}
				}
			}
		}
		raw, err := json.Marshal(input.Bundle)
		if err != nil {
			return err
		}
		defer wipe(raw)
		revision := domain.HarnessSetupRevision{ID: result.Revision, SetupID: result.ID, Digest: input.Bundle.Digest(), KeyID: s.secretKey.ID, CreatedAt: now, Files: []string{}}
		for _, f := range input.Bundle.Files {
			revision.Files = append(revision.Files, f.Path)
		}
		revision.Nonce, revision.Ciphertext, err = s.secretKey.SealSetup(revision.ID, raw)
		if err != nil {
			return err
		}
		if err := tx.PutHarnessSetup(ctx, result); err != nil {
			return err
		}
		if err := tx.InsertHarnessSetupRevision(ctx, revision); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "harness_setup", result.ID, "harness_setup.imported", result.ResourceVersion, nil); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, setupReplay{Hash: fingerprint, Setup: result}, now)
	})
	return result, err
}
func (s *Service) MutateHarnessSetup(ctx context.Context, id string, input SetupMutation, key string) (domain.HarnessSetup, error) {
	if err := requireIdempotency(key); err != nil {
		return domain.HarnessSetup{}, err
	}
	var result domain.HarnessSetup
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		fingerprint := setupFingerprint(input)
		scope := "setup:mutate:" + id
		if replay, ok, err := getReplay[setupReplay](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			if replay.Hash != fingerprint {
				return domain.ErrConflict
			}
			result = replay.Setup
			return nil
		}
		items, err := tx.ListHarnessSetups(ctx)
		if err != nil {
			return err
		}
		result, err = findSetup(items, id)
		if err != nil {
			return err
		}
		if result.Deleted || result.ResourceVersion != input.ExpectedResourceVersion {
			return domain.ErrConflict
		}
		if input.Name != "" {
			result.Name, err = requireName(input.Name)
			if err != nil {
				return err
			}
		}
		if input.Revision != "" {
			rev, err := tx.GetHarnessSetupRevision(ctx, input.Revision)
			if err != nil {
				return err
			}
			if rev.SetupID != id {
				return domain.ErrInvalid
			}
			result.Revision = rev.ID
		}
		if input.Default != nil {
			result.Default = *input.Default
		}
		result.Deleted = input.Deleted
		if result.Deleted {
			result.Default = false
		}
		if result.Default {
			for _, item := range items {
				if item.ID != id && item.Harness == result.Harness && item.Default {
					item.Default = false
					item.ResourceVersion++
					if err := tx.PutHarnessSetup(ctx, item); err != nil {
						return err
					}
				}
			}
		}
		result.ResourceVersion++
		if err := tx.PutHarnessSetup(ctx, result); err != nil {
			return err
		}
		if result.Deleted {
			if err := tx.CollectDeletedHarnessSetups(ctx); err != nil {
				return err
			}
		}
		if err := s.appendEvent(ctx, tx, "harness_setup", id, "harness_setup.updated", result.ResourceVersion, nil); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, setupReplay{Hash: fingerprint, Setup: result}, s.clock.Now().UTC())
	})
	return result, err
}
func (s *Service) HarnessSetupRevisions(ctx context.Context, id string) ([]domain.HarnessSetupRevision, error) {
	var items []domain.HarnessSetupRevision
	err := s.store.View(ctx, func(r ports.Reader) error {
		all, err := r.ListHarnessSetups(ctx)
		if err != nil {
			return err
		}
		if _, err := findSetup(all, id); err != nil {
			return err
		}
		items, err = r.ListHarnessSetupRevisions(ctx, id)
		return err
	})
	return items, err
}

// pinHarnessSetups snapshots defaults inside Capsule creation's transaction.
// The sentinel "clean" opts out. An explicit setup applies only to its harness.
func (s *Service) pinHarnessSetups(ctx context.Context, tx ports.Transaction, id domain.CapsuleID, selection string) error {
	if selection == "clean" {
		return nil
	}
	items, err := tx.ListHarnessSetups(ctx)
	if err != nil {
		return err
	}
	capsule, err := tx.GetCapsule(ctx, id)
	if err != nil {
		return err
	}
	if selection != "" {
		item, err := findSetup(items, selection)
		if err != nil {
			return err
		}
		if item.Deleted {
			return domain.ErrNotFound
		}
		if capsule.LauncherHarness != "" && capsule.LauncherHarness != item.Harness {
			return domain.ErrInvalid
		}
		return tx.PinHarnessSetup(ctx, id, item.Harness, item.Revision)
	}
	overrides, err := tx.ProjectHarnessSetups(ctx, capsule.ProjectID)
	if err != nil {
		return err
	}
	selected := map[string]domain.HarnessSetup{}
	for _, item := range items {
		if item.Default && !item.Deleted {
			selected[item.Harness] = item
		}
	}
	for harness, setup := range overrides {
		delete(selected, harness)
		if setup == "clean" {
			continue
		}
		item, err := findSetup(items, setup)
		if err != nil {
			return err
		}
		if item.Deleted {
			return fmt.Errorf("%w: project setup was deleted; select a replacement", domain.ErrConflict)
		}
		selected[harness] = item
	}
	for harness, item := range selected {
		if err := tx.PinHarnessSetup(ctx, id, harness, item.Revision); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) runtimeHarnessSetup(ctx context.Context, id domain.CapsuleID, harness string) (*harnesssetup.RuntimeSetup, error) {
	// Structured adapters may use a suffix but must identify a supported pack.
	harness = strings.TrimSuffix(harness, "-structured")
	var result *harnesssetup.RuntimeSetup
	err := s.store.View(ctx, func(r ports.Reader) error {
		revision, err := r.GetPinnedHarnessSetup(ctx, id, harness)
		if err != nil || revision == "" {
			return err
		}
		item, err := r.GetHarnessSetupRevision(ctx, revision)
		if err != nil {
			return err
		}
		raw, err := s.secretKey.OpenSetup(item.ID, item.KeyID, item.Nonce, item.Ciphertext)
		if err != nil {
			return err
		}
		defer wipe(raw)
		result = &harnesssetup.RuntimeSetup{Revision: revision}
		if err := json.Unmarshal(raw, &result.Bundle); err != nil {
			return domain.ErrCorrupt
		}
		if result.Bundle.Digest() != item.Digest {
			return domain.ErrCorrupt
		}
		return result.Bundle.Validate()
	})
	return result, err
}

func (s *Service) ProjectHarnessSetups(ctx context.Context, id domain.ProjectID) (map[string]string, error) {
	var result map[string]string
	err := s.store.View(ctx, func(r ports.Reader) error {
		if _, err := r.GetProject(ctx, id); err != nil {
			return err
		}
		var err error
		result, err = r.ProjectHarnessSetups(ctx, id)
		return err
	})
	return result, err
}
func (s *Service) SetProjectHarnessSetup(ctx context.Context, id domain.ProjectID, harness, setup string) error {
	if !harnesssetup.Supported(harness) {
		return domain.ErrInvalid
	}
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		if _, err := tx.GetProject(ctx, id); err != nil {
			return err
		}
		if setup != "" && setup != "clean" {
			items, err := tx.ListHarnessSetups(ctx)
			if err != nil {
				return err
			}
			item, err := findSetup(items, setup)
			if err != nil {
				return err
			}
			if item.Deleted || item.Harness != harness {
				return domain.ErrInvalid
			}
		}
		return tx.SetProjectHarnessSetup(ctx, id, harness, setup)
	})
}

// HarnessSetupContents returns one consistent setup version for explicit editing.
func (s *Service) HarnessSetupContents(ctx context.Context, id string) (SetupImport, error) {
	var result SetupImport
	if s.secretKey == nil {
		return result, domain.ErrUnsupported
	}
	err := s.store.View(ctx, func(r ports.Reader) error {
		items, err := r.ListHarnessSetups(ctx)
		if err != nil {
			return err
		}
		setup, err := findSetup(items, id)
		if err != nil {
			return err
		}
		if setup.Deleted {
			return domain.ErrNotFound
		}
		revision, err := r.GetHarnessSetupRevision(ctx, setup.Revision)
		if err != nil {
			return err
		}
		if revision.SetupID != id {
			return domain.ErrCorrupt
		}
		raw, err := s.secretKey.OpenSetup(revision.ID, revision.KeyID, revision.Nonce, revision.Ciphertext)
		if err != nil {
			return err
		}
		defer wipe(raw)
		result = SetupImport{ID: setup.ID, Name: setup.Name, Default: setup.Default, ExpectedResourceVersion: setup.ResourceVersion}
		if err := json.Unmarshal(raw, &result.Bundle); err != nil {
			return domain.ErrCorrupt
		}
		if result.Bundle.Digest() != revision.Digest || result.Bundle.Harness != setup.Harness {
			return domain.ErrCorrupt
		}
		return result.Bundle.Validate()
	})
	return result, err
}
