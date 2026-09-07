package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

var errProjectSetup = errors.New("project setup failed")

func (r *Reconciler) preparationSpec(ctx context.Context, c domain.Capsule, p domain.Project) (domain.Preparation, error) {
	var result domain.Preparation
	err := r.store.Transact(ctx, func(tx ports.Transaction) error {
		var err error
		result, err = tx.GetPreparation(ctx, c.ID)
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		policy, err := tx.GetPreparationPolicy(ctx, c.ProjectID)
		if err != nil {
			return err
		}
		result = domain.Preparation{ReuseDisabled: policy.Disabled, Generation: policy.Generation, CapsuleID: c.ID, RepositoryURL: p.RepositoryURL, ImageReference: c.WorkspaceImage(p), GitSecretName: p.GitSecretName, Setup: append([]string(nil), p.Setup...), Stage: "checkout", UpdatedAt: r.clock.Now().UTC()}
		return tx.PutPreparation(ctx, result)
	})
	return result, err
}
func (r *Reconciler) savePreparation(ctx context.Context, p domain.Preparation) error {
	p.UpdatedAt = r.clock.Now().UTC()
	return r.store.Transact(ctx, func(tx ports.Transaction) error { return tx.PutPreparation(ctx, p) })
}
func preparationHash(p domain.Preparation) string {
	value, _ := json.Marshal(struct {
		Version, Repository, Revision, Image, Platform string
		Generation                                     int64
		Setup                                          []string
	}{"meridian.prepare.v2", p.RepositoryURL, p.SourceRevision, p.ImageDigest, p.Platform, p.Generation, p.Setup})
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}
func cacheablePreparation(p domain.Preparation) bool {
	_, digest, found := strings.Cut(p.ImageDigest, "sha256:")
	return !p.ReuseDisabled && p.RepositoryURL != "" && (len(p.SourceRevision) == 40 || len(p.SourceRevision) == 64) && strings.Trim(p.SourceRevision, "0123456789abcdef") == "" && found && len(digest) == 64 && strings.Trim(digest, "0123456789abcdef") == "" && p.Platform != ""
}
func (r *Reconciler) completePreparation(ctx context.Context, c domain.Capsule, resource ports.ProviderResource) (bool, error) {
	preparer, ok := r.provider.(ports.WorkspacePreparer)
	if !ok || c.OriginMomentID != "" {
		return false, nil
	}
	var p domain.Preparation
	if err := r.store.View(ctx, func(reader ports.Reader) error { var err error; p, err = reader.GetPreparation(ctx, c.ID); return err }); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if p.Stage == "ready" {
		return false, nil
	}
	if p.Stage == "reclone" {
		credential, err := r.preparationGitCredential(ctx, p.GitSecretName)
		if err != nil {
			return false, err
		}
		image := p.ImageReference
		if p.ImageDigest != "" {
			image = p.ImageDigest
		}
		_, err = r.provider.Create(ctx, ports.CreateCapsuleRequest{CapsuleID: c.ID, RepositoryURL: p.RepositoryURL, ImageReference: image, GitCredential: credential, ForcePreparation: true})
		if credential != nil {
			credential.Username, credential.Password = "", ""
		}
		if err != nil {
			return false, err
		}
		p.CacheKey = ""
		p.Stage = "preparing"
	}
	if p.CacheKey == "" {
		identity, err := preparer.PreparationIdentity(ctx, c.ProviderResourceID)
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrUnsupported) {
			p.Stage = "preparing"
			p.CacheMiss = "This image prepares fresh without cache reuse"
			p.Legacy = true
			if err := r.savePreparation(ctx, p); err != nil {
				return false, err
			}
			credential, err := r.preparationGitCredential(ctx, p.GitSecretName)
			if err != nil {
				return false, err
			}
			_, err = r.provider.Create(ctx, ports.CreateCapsuleRequest{CapsuleID: c.ID, RepositoryURL: p.RepositoryURL, ImageReference: p.ImageReference, Setup: p.Setup, GitCredential: credential})
			if credential != nil {
				credential.Username, credential.Password = "", ""
			}
			if err != nil {
				return false, err
			}
			p.Stage = "ready"
			return true, r.savePreparation(ctx, p)
		}
		if err != nil {
			return false, err
		}
		p.SourceRevision, p.Platform, p.ImageDigest = identity.SourceRevision, identity.Platform, resource.ImageDigest
		p.CacheKey = preparationHash(p)
		if p.ReuseDisabled {
			p.CacheMiss = "Reuse disabled for this Project"
		} else if !cacheablePreparation(p) {
			p.CacheMiss = "Source or image identity could not be verified"
		}
		p.Stage = "preparing"
		if err := r.savePreparation(ctx, p); err != nil {
			return false, err
		}
	}
	if cacheablePreparation(p) && r.setupCacheEnabled(ctx) && p.CacheMiss == "" {
		var moment domain.Moment
		err := r.store.View(ctx, func(reader ports.Reader) error {
			cache, err := reader.GetSetupMomentCache(ctx, c.ProjectID, p.CacheKey)
			if err != nil {
				return err
			}
			if r.clock.Now().Sub(cache.CreatedAt) > 72*time.Hour {
				return domain.ErrNotFound
			}
			moment, err = reader.GetMoment(ctx, cache.MomentID)
			return err
		})
		if err == nil && moment.Kind == domain.MomentSetupCache && moment.ProjectID == c.ProjectID && moment.ProjectSetupHash == p.CacheKey && moment.ImageDigest == p.ImageDigest && moment.GitHEAD == p.SourceRevision {
			p.Stage = "restoring"
			if err := r.savePreparation(ctx, p); err != nil {
				return false, err
			}
			if err := r.restorePreparedWorkspace(ctx, c, moment); err == nil {
				p.Reused = true
				p.Stage = "ready"
				return true, r.savePreparation(ctx, p)
			}
			// A failed restore can leave partial files. Recreate the original checkout
			// through the provider before attempting fresh setup.
			p.CacheMiss = "Prepared files unavailable; preparing fresh"
			p.Stage = "reclone"
			if err := r.savePreparation(ctx, p); err != nil {
				return false, err
			}
			return true, nil
		} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return false, err
		}
		p.CacheMiss = "No matching prepared environment"
	}
	p.Stage = "preparing"
	if err := r.savePreparation(ctx, p); err != nil {
		return false, err
	}
	if err := preparer.FinishPreparation(ctx, c.ProviderResourceID, p.Setup, p.SourceRevision, p.CacheKey); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, fmt.Errorf("%w: check the configured setup command and retry", errProjectSetup)
	}
	if cacheablePreparation(p) && r.setupCacheEnabled(ctx) {
		p.Stage = "saving"
		if err := r.savePreparation(ctx, p); err != nil {
			return false, err
		}
		// Cache publication is an optimization; a valid fresh environment remains usable.
		_ = r.publishPreparation(ctx, c, p)
	}
	p.Stage = "ready"
	return true, r.savePreparation(ctx, p)
}
func (r *Reconciler) restorePreparedWorkspace(ctx context.Context, c domain.Capsule, m domain.Moment) error {
	manifestReader, manifestSize, err := r.artifacts.Open(ctx, m.ManifestSHA256)
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(manifestReader, 128<<10))
	closeErr := manifestReader.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	sum := sha256.Sum256(raw)
	var manifest momentManifest
	if int64(len(raw)) != manifestSize || hex.EncodeToString(sum[:]) != m.ManifestSHA256 || json.Unmarshal(raw, &manifest) != nil || manifest.Version != "meridian.moment.v1" || manifest.ArchiveSHA256 != m.ArchiveSHA256 || manifest.ArchiveSize != m.ArchiveSize || manifest.ProjectSetupHash != m.ProjectSetupHash || manifest.ImageDigest != m.ImageDigest || manifest.GitHEAD != m.GitHEAD || manifest.SourceCapsuleID != m.CapsuleID || manifest.Final {
		return domain.ErrCorrupt
	}

	archive, size, err := r.artifacts.Open(ctx, m.ArchiveSHA256)
	if err != nil {
		return err
	}
	defer archive.Close()
	if size != m.ArchiveSize {
		return domain.ErrCorrupt
	}
	return r.snapshotter.RestoreWorkspace(ctx, c.ProviderResourceID, m.ArchiveSHA256, size, archive)
}
func (r *Reconciler) publishPreparation(ctx context.Context, c domain.Capsule, p domain.Preparation) error {
	var project domain.Project
	if err := r.store.View(ctx, func(reader ports.Reader) error {
		var err error
		project, err = reader.GetProject(ctx, c.ProjectID)
		return err
	}); err != nil {
		return err
	}
	service := &Service{store: r.store, clock: r.clock, ids: r.ids, snapshotter: r.snapshotter, artifacts: r.artifacts, observer: r.observer, providerName: r.providerName}
	moment, err := service.captureSetupArtifacts(ctx, c, project)
	if err != nil {
		return err
	}
	if moment.GitHEAD != p.SourceRevision || moment.ImageDigest != p.ImageDigest {
		return domain.ErrConflict
	}
	return r.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetCapsule(ctx, c.ID)
		if err != nil {
			return err
		}
		if current.State != domain.CapsulePreparing || current.DesiredState != domain.IntentReady {
			return domain.ErrConflict
		}
		if err := tx.InsertMoment(ctx, moment); err != nil {
			return err
		}
		return tx.PutSetupMomentCache(ctx, domain.SetupMomentCache{ProjectID: c.ProjectID, ConfigHash: p.CacheKey, MomentID: moment.ID, CreatedAt: moment.CreatedAt})
	})
}
func (s *Service) GetPreparation(ctx context.Context, id domain.CapsuleID) (domain.Preparation, error) {
	var p domain.Preparation
	err := s.store.View(ctx, func(reader ports.Reader) error { var err error; p, err = reader.GetPreparation(ctx, id); return err })
	return p, err
}

func (r *Reconciler) preparationGitCredential(ctx context.Context, name string) (*ports.GitHTTPSCredential, error) {
	if name == "" {
		return nil, nil
	}
	var secret domain.Secret
	if err := r.store.View(ctx, func(reader ports.Reader) error { var err error; secret, err = reader.GetSecret(ctx, name); return err }); err != nil {
		return nil, err
	}
	if secret.Purpose != domain.SecretGitHTTPS || r.secretKey == nil {
		return nil, domain.ErrInvalid
	}
	payload, err := r.secretKey.Open(secret)
	if err != nil {
		return nil, err
	}
	return &ports.GitHTTPSCredential{Username: payload.Username, Password: payload.Password}, nil
}

func (r *Reconciler) preparationContext(parent context.Context, id domain.CapsuleID) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-r.done:
				cancel()
				return
			case <-ticker.C:
				c, err := r.getCapsule(ctx, id)
				if err == nil && c.DesiredState == domain.IntentDeleted {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}
