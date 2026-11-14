package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/secrets"
)

// DepartedDeletion counts what DeleteDepartedMember started or removed.
type DepartedDeletion struct {
	Projects    int
	Secrets     int
	Connections int
	Setups      int
}

// requireBreakGlass admits only the break-glass admin. Moving or deleting a
// departed member's work is its job alone (ADR 0046): an admin who needs
// another member's work asks that member.
func requireBreakGlass(ctx context.Context) error {
	caller, err := principal.Require(ctx)
	if err != nil {
		return err
	}
	if caller.Kind() != principal.KindBreakGlass {
		return domain.ErrNotFound
	}
	return nil
}

// departed returns the departed member: they must exist and be disabled.
func departed(ctx context.Context, reader ports.Reader, id domain.UserID) (domain.User, error) {
	user, err := reader.GetUser(ctx, id)
	if err != nil {
		return domain.User{}, err
	}
	if user.DisabledAt.IsZero() {
		return domain.User{}, fmt.Errorf("%w: disable the member before moving or deleting their work", domain.ErrConflict)
	}
	return user, nil
}

func listOwnedSecrets(ctx context.Context, reader ports.Reader, owner domain.UserID) ([]domain.Secret, error) {
	var all []domain.Secret
	for offset := 0; ; {
		page, more, err := reader.ListSecrets(ctx, owner, ports.Page{Offset: offset, Limit: 100})
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if !more {
			return all, nil
		}
		offset += len(page)
	}
}

// TransferDepartedMember moves a departed member's Projects and everything
// under them, their secrets, and their harness setups to another member, in
// one transaction. Secret envelopes bind their owner, so each is re-wrapped
// for the new owner at the same resource version. Provider connections do
// not move: a personal login stays with its account holder (ADR 0038), so
// Project grants of those connections are dropped and the new owner grants
// their own. The target must not already hold a secret of the same name.
func (s *Service) TransferDepartedMember(ctx context.Context, from, to domain.UserID) error {
	if err := requireBreakGlass(ctx); err != nil {
		return err
	}
	caller, _ := principal.From(ctx)
	system := principal.WithSystem(ctx)
	return s.store.Transact(system, func(tx ports.Transaction) error {
		if _, err := departed(system, tx, from); err != nil {
			return err
		}
		target, err := tx.GetUser(system, to)
		if err != nil {
			return err
		}
		if !target.DisabledAt.IsZero() || to == from {
			return fmt.Errorf("%w: the receiving member must be another, enabled member", domain.ErrConflict)
		}
		moving, err := listOwnedSecrets(system, tx, from)
		if err != nil {
			return err
		}
		var clashes []string
		for _, secret := range moving {
			if _, err := tx.GetSecret(system, to, secret.Name); err == nil {
				clashes = append(clashes, secret.Name)
			} else if !errors.Is(err, domain.ErrNotFound) {
				return err
			}
		}
		if len(clashes) > 0 {
			sort.Strings(clashes)
			return fmt.Errorf("%w: the receiving member already has secrets named %s", domain.ErrConflict, strings.Join(clashes, ", "))
		}
		if len(moving) > 0 && s.secretKey == nil {
			return domain.ErrUnsupported
		}
		payloads := make([]secrets.Payload, len(moving))
		for index, secret := range moving {
			if payloads[index], err = s.secretKey.Open(secret); err != nil {
				return fmt.Errorf("open a departed member's secret: %w", err)
			}
		}
		if err := tx.TransferOwnership(system, from, to); err != nil {
			return err
		}
		for index, secret := range moving {
			next := secret
			next.OwnerUserID = to
			if next.Nonce, next.Ciphertext, err = s.secretKey.Seal(next, payloads[index]); err != nil {
				return err
			}
			if err := tx.UpdateSecret(system, next, secret.ResourceVersion); err != nil {
				return err
			}
		}
		record, ok := auditRecordFor(principal.With(ctx, caller), s.clock.Now(), domain.AuditMemberTransfer,
			"user", string(from)+":"+string(to), domain.AuditAllowed)
		if !ok {
			return principal.ErrMissing
		}
		return tx.AppendAuditRecord(system, record)
	})
}

// DeleteDepartedMember deletes a departed member's work: it starts deletion
// of each of their Projects (which removes everything under them as usual),
// crypto-shreds their secrets, revokes their provider connections, and
// deletes their harness setups. The member's row and audit records stay.
// It can be repeated until nothing is left.
func (s *Service) DeleteDepartedMember(ctx context.Context, from domain.UserID) (DepartedDeletion, error) {
	if err := requireBreakGlass(ctx); err != nil {
		return DepartedDeletion{}, err
	}
	caller, _ := principal.From(ctx)
	system := principal.WithSystem(ctx)
	var projects []domain.Project
	var owned []domain.Secret
	var connections []domain.ProviderConnection
	var setups []domain.HarnessSetup
	if err := s.store.View(system, func(reader ports.Reader) error {
		if _, err := departed(system, reader, from); err != nil {
			return err
		}
		for offset := 0; ; {
			page, more, err := reader.ListProjects(system, from, ports.Page{Offset: offset, Limit: 100})
			if err != nil {
				return err
			}
			projects = append(projects, page...)
			if !more {
				break
			}
			offset += len(page)
		}
		var err error
		if owned, err = listOwnedSecrets(system, reader, from); err != nil {
			return err
		}
		all, err := reader.ListProviderConnections(system)
		if err != nil {
			return err
		}
		for _, connection := range all {
			if connection.OwnerUserID == from && !connection.Revoked {
				connections = append(connections, connection)
			}
		}
		allSetups, err := reader.ListHarnessSetups(system)
		if err != nil {
			return err
		}
		for _, setup := range allSetups {
			if setup.OwnerUserID == from && !setup.Deleted {
				setups = append(setups, setup)
			}
		}
		return nil
	}); err != nil {
		return DepartedDeletion{}, err
	}
	var result DepartedDeletion
	for _, project := range projects {
		if project.State == domain.ProjectDeleting {
			continue
		}
		if _, err := s.deleteProject(system, project.ID, project.ResourceVersion, "", false); err != nil {
			return result, fmt.Errorf("delete a departed member's Project: %w", err)
		}
		result.Projects++
	}
	for _, secret := range owned {
		if err := s.store.Transact(system, func(tx ports.Transaction) error {
			return tx.DeleteSecret(system, from, secret.Name, secret.ResourceVersion)
		}); err != nil {
			return result, fmt.Errorf("delete a departed member's secret: %w", err)
		}
		result.Secrets++
	}
	for _, connection := range connections {
		if _, err := s.PutProviderConnection(system, connection.ID, ConnectionInput{
			Name: connection.Name, Provider: connection.Provider, Revoked: true,
			ExpectedResourceVersion: connection.ResourceVersion,
		}); err != nil {
			return result, fmt.Errorf("revoke a departed member's connection: %w", err)
		}
		result.Connections++
	}
	for _, setup := range setups {
		if _, err := s.MutateHarnessSetup(system, setup.ID, SetupMutation{
			Deleted: true, ExpectedResourceVersion: setup.ResourceVersion,
		}, "departed-delete:"+setup.ID+":"+fmt.Sprint(setup.ResourceVersion)); err != nil {
			return result, fmt.Errorf("delete a departed member's harness setup: %w", err)
		}
		result.Setups++
	}
	record, ok := auditRecordFor(principal.With(ctx, caller), s.clock.Now(), domain.AuditMemberDelete,
		"user", string(from), domain.AuditAllowed)
	if ok {
		_ = s.store.Transact(system, func(tx ports.Transaction) error {
			return tx.AppendAuditRecord(system, record)
		})
	}
	return result, nil
}
