package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/secrets"
)

// EnvelopeRewrap counts what BindEnvelopeOwners changed.
type EnvelopeRewrap struct {
	Secrets        int
	Connections    int
	SetupRevisions int
	// Unreadable envelopes did not open under the installation key before
	// the re-wrap either. They are left exactly as they were.
	Unreadable int
}

// BindEnvelopeOwners re-wraps every credential envelope sealed before
// owners were bound, so its authenticated context names its row's owner
// (ADR 0046). It runs once at startup, after migrations, while the daemon
// holds the data-directory lease and before the API serves. It is one
// transaction: a write failure leaves every old envelope in place. Running
// it again changes nothing.
//
// Secret resource versions do not change, so no client sees a new ETag.
// Plaintext is held only between opening and re-sealing one envelope.
func BindEnvelopeOwners(ctx context.Context, store ports.Store, key *secrets.InstallationKey) (EnvelopeRewrap, error) {
	var result EnvelopeRewrap
	if store == nil || key == nil {
		return result, nil
	}
	ctx = principal.WithSystem(ctx)
	err := store.Transact(ctx, func(tx ports.Transaction) error {
		result = EnvelopeRewrap{}
		var legacy []domain.Secret
		for offset := 0; ; {
			page, more, err := tx.ListSecrets(ctx, "", ports.Page{Offset: offset, Limit: 100})
			if err != nil {
				return err
			}
			for _, secret := range page {
				if secret.EnvelopeVersion == secrets.LegacyEnvelopeVersion {
					legacy = append(legacy, secret)
				}
			}
			if !more {
				break
			}
			offset += len(page)
		}
		for _, secret := range legacy {
			next, err := key.RewrapLegacy(secret)
			if errors.Is(err, domain.ErrKeyMismatch) || errors.Is(err, domain.ErrCorrupt) {
				result.Unreadable++
				continue
			}
			if err != nil {
				return fmt.Errorf("re-wrap secret envelope: %w", err)
			}
			if err := tx.UpdateSecret(ctx, next, secret.ResourceVersion); err != nil {
				return fmt.Errorf("store re-wrapped secret: %w", err)
			}
			result.Secrets++
		}

		connections, err := tx.ListProviderConnections(ctx)
		if err != nil {
			return err
		}
		for _, listed := range connections {
			connection, err := tx.GetProviderConnection(ctx, listed.ID)
			if err != nil {
				return err
			}
			if opened, err := key.OpenSetup(connectionEnvelopeID(connection), connection.KeyID, connection.Nonce, connection.Ciphertext); err == nil {
				wipe(opened)
				continue
			}
			plain, err := key.OpenSetup(legacyConnectionEnvelopeID(connection), connection.KeyID, connection.Nonce, connection.Ciphertext)
			if err != nil {
				result.Unreadable++
				continue
			}
			nonce, ciphertext, err := key.SealSetup(connectionEnvelopeID(connection), plain)
			wipe(plain)
			if err != nil {
				return fmt.Errorf("re-wrap connection envelope: %w", err)
			}
			connection.KeyID, connection.Nonce, connection.Ciphertext = key.ID, nonce, ciphertext
			if err := tx.PutProviderConnection(ctx, connection); err != nil {
				return fmt.Errorf("store re-wrapped connection: %w", err)
			}
			result.Connections++
		}
		setups, err := tx.ListHarnessSetups(ctx)
		if err != nil {
			return err
		}
		for _, setup := range setups {
			revisions, err := tx.ListHarnessSetupRevisions(ctx, setup.ID)
			if err != nil {
				return err
			}
			for _, listed := range revisions {
				revision, err := tx.GetHarnessSetupRevision(ctx, listed.ID)
				if err != nil {
					return err
				}
				if opened, err := key.OpenSetup(setupEnvelopeID(revision), revision.KeyID, revision.Nonce, revision.Ciphertext); err == nil {
					wipe(opened)
					continue
				}
				// Only startup migration accepts the legacy, owner-free id.
				plain, err := key.OpenSetup(revision.ID, revision.KeyID, revision.Nonce, revision.Ciphertext)
				if err != nil {
					result.Unreadable++
					continue
				}
				nonce, ciphertext, err := key.SealSetup(setupEnvelopeID(revision), plain)
				wipe(plain)
				if err != nil {
					return fmt.Errorf("re-wrap setup revision envelope: %w", err)
				}
				revision.KeyID, revision.Nonce, revision.Ciphertext = key.ID, nonce, ciphertext
				if err := tx.ReplaceHarnessSetupRevisionEnvelope(ctx, revision); err != nil {
					return fmt.Errorf("store re-wrapped setup revision: %w", err)
				}
				result.SetupRevisions++
			}
		}

		return nil
	})
	if err != nil {
		return EnvelopeRewrap{}, err
	}
	return result, nil
}
