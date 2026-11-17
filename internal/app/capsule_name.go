package app

import (
	"context"
	"fmt"
	"unicode"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

type renameOutcome struct {
	Name string `json:"name"`
}

// RenameCapsule saves the name shown for a Capsule. The text is whatever the
// operator submits. A transcript is not copied in unless they send it.
func (s *Service) RenameCapsule(
	ctx context.Context,
	id domain.CapsuleID,
	name string,
	expected domain.ResourceVersion,
	key string,
) (domain.Capsule, error) {
	if err := requirePrincipal(ctx); err != nil {
		return domain.Capsule{}, err
	}
	name, err := requireCapsuleTitle(name)
	if err != nil {
		return domain.Capsule{}, err
	}
	if expected <= 0 {
		return domain.Capsule{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return domain.Capsule{}, err
	}
	scope := "capsule:" + string(id) + ":rename"
	var result domain.Capsule
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, replayErr := getReplay[renameOutcome](ctx, tx, scope, key); replayErr != nil {
			return replayErr
		} else if ok {
			if replay.Name != name {
				return fmt.Errorf("%w: Idempotency-Key was used with a different request", domain.ErrConflict)
			}
			result, err = tx.GetCapsule(ctx, id)
			return err
		}
		current, getErr := tx.GetCapsule(ctx, id)
		if getErr != nil {
			return getErr
		}
		if current.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if mutateErr := current.CanMutate(); mutateErr != nil {
			return mutateErr
		}
		if current.Name == name {
			result = current
			return putReplay(ctx, tx, scope, key, renameOutcome{Name: name}, s.clock.Now().UTC())
		}
		previous := current.ResourceVersion
		current.Name = name
		current.UpdatedAt = s.clock.Now().UTC()
		current.ResourceVersion++
		if updateErr := tx.UpdateCapsule(ctx, current, previous); updateErr != nil {
			return updateErr
		}
		if eventErr := s.appendEvent(
			ctx, tx, "capsule", string(id), "capsule.rename", current.ResourceVersion, nil,
		); eventErr != nil {
			return eventErr
		}
		if auditErr := s.audit(ctx, tx, domain.AuditCapsuleRename, "capsule", string(id)); auditErr != nil {
			return auditErr
		}
		result = current
		return putReplay(ctx, tx, scope, key, renameOutcome{Name: name}, current.UpdatedAt)
	})
	if err != nil {
		s.auditFailure(ctx, domain.AuditCapsuleRename, "capsule", string(id), err)
		return domain.Capsule{}, err
	}
	return result, nil
}

func requireCapsuleTitle(name string) (string, error) {
	name, err := requireName(name)
	if err != nil {
		return "", err
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || unicode.Is(unicode.Cf, r) {
			return "", fmt.Errorf("%w: name contains unsupported characters", domain.ErrInvalid)
		}
	}
	return name, nil
}
