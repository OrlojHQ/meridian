package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/secrets"
)

var secretNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)
var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

type SecretPage struct {
	Items      []domain.Secret
	NextOffset int
}

type SecretInput struct {
	Purpose  domain.SecretPurpose
	Value    string
	Username string
	Password string
}

func validateSecretName(name string) error {
	if !secretNamePattern.MatchString(name) {
		return fmt.Errorf("%w: secret name is invalid", domain.ErrInvalid)
	}
	return nil
}

func validateSecretInput(name string, input SecretInput) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	if !input.Purpose.Valid() {
		return fmt.Errorf("%w: secret purpose is invalid", domain.ErrInvalid)
	}
	switch input.Purpose {
	case domain.SecretGitHTTPS, domain.SecretGitPush:
		if input.Password == "" || input.Value != "" {
			return fmt.Errorf("%w: Git secret requires password/token input", domain.ErrInvalid)
		}
	case domain.SecretGitHubAPI:
		if input.Value == "" || input.Username != "" || input.Password != "" {
			return fmt.Errorf("%w: GitHub API secret requires token input", domain.ErrInvalid)
		}
	case domain.SecretHarnessEnv:
		if !environmentNamePattern.MatchString(name) || input.Username != "" || input.Password != "" {
			return fmt.Errorf("%w: harness secret name must be an environment variable", domain.ErrInvalid)
		}
	}
	return nil
}

func (s *Service) PutSecret(
	ctx context.Context,
	name string,
	input SecretInput,
	expected domain.ResourceVersion,
	idempotencyKey string,
) (domain.Secret, error) {
	if s.secretKey == nil {
		return domain.Secret{}, domain.ErrUnsupported
	}
	if err := validateSecretInput(name, input); err != nil {
		return domain.Secret{}, err
	}
	if err := requireIdempotency(idempotencyKey); err != nil {
		return domain.Secret{}, err
	}
	scope := "secret:put:" + name
	var result domain.Secret
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[domain.Secret](ctx, tx, scope, idempotencyKey); err != nil {
			return err
		} else if ok {
			result = replay
			return nil
		}
		now := s.clock.Now().UTC()
		existing, err := tx.GetSecret(ctx, name)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			if expected != 0 {
				return domain.ErrConflict
			}
			result = domain.Secret{
				ID: domain.SecretID(s.ids.NewID()), Name: name, Purpose: input.Purpose,
				EnvelopeVersion: secrets.EnvelopeVersion, KEKID: s.secretKey.ID,
				KEKVersion: s.secretKey.Version, CreatedAt: now, UpdatedAt: now,
				ResourceVersion: 1,
			}
		case err != nil:
			return err
		default:
			if expected == 0 || existing.ResourceVersion != expected {
				return domain.ErrConflict
			}
			result = existing
			result.Purpose = input.Purpose
			result.EnvelopeVersion = secrets.EnvelopeVersion
			result.KEKID = s.secretKey.ID
			result.KEKVersion = s.secretKey.Version
			result.UpdatedAt = now
			result.ResourceVersion++
		}
		payload := secrets.Payload{
			Value: input.Value, Username: input.Username, Password: input.Password,
		}
		nonce, ciphertext, err := s.secretKey.Seal(result, payload)
		if err != nil {
			return err
		}
		result.Nonce, result.Ciphertext = nonce, ciphertext
		if result.ResourceVersion == 1 {
			if err := tx.InsertSecret(ctx, result); err != nil {
				return err
			}
		} else if err := tx.UpdateSecret(ctx, result, expected); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "secret", string(result.ID), "secret.stored",
			result.ResourceVersion, nil); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, idempotencyKey, result, now)
	})
	return result, err
}

func (s *Service) ListSecrets(ctx context.Context, offset, limit int) (SecretPage, error) {
	page := normalizePage(offset, limit)
	var result SecretPage
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var more bool
		var err error
		result.Items, more, err = reader.ListSecrets(ctx, page)
		if more {
			result.NextOffset = page.Offset + len(result.Items)
		}
		return err
	})
	return result, err
}

func (s *Service) DeleteSecret(
	ctx context.Context,
	name string,
	expected domain.ResourceVersion,
	idempotencyKey string,
) (domain.Secret, error) {
	if err := validateSecretName(name); err != nil {
		return domain.Secret{}, err
	}
	if expected <= 0 {
		return domain.Secret{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(idempotencyKey); err != nil {
		return domain.Secret{}, err
	}
	scope := "secret:delete:" + name
	var result domain.Secret
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[domain.Secret](ctx, tx, scope, idempotencyKey); err != nil {
			return err
		} else if ok {
			result = replay
			return nil
		}
		var err error
		result, err = tx.GetSecret(ctx, name)
		if err != nil {
			return err
		}
		if result.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if err := tx.DeleteSecret(ctx, name, expected); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "secret", string(result.ID), "secret.deleted",
			expected, nil); err != nil {
			return err
		}
		// The row is deleted: ciphertext, nonce, and envelope metadata are
		// removed. Audit/event data remains content-free.
		result.Nonce, result.Ciphertext = nil, nil
		return putReplay(ctx, tx, scope, idempotencyKey, result, s.clock.Now().UTC())
	})
	return result, err
}

func (s *Service) resolveSecret(
	ctx context.Context, name string, purpose domain.SecretPurpose,
) (secrets.Payload, error) {
	if s.secretKey == nil {
		return secrets.Payload{}, domain.ErrUnsupported
	}
	var secret domain.Secret
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		secret, err = reader.GetSecret(ctx, name)
		return err
	})
	if err != nil {
		return secrets.Payload{}, err
	}
	if secret.Purpose != purpose {
		return secrets.Payload{}, fmt.Errorf("%w: secret purpose mismatch", domain.ErrInvalid)
	}
	return s.secretKey.Open(secret)
}

func (s *Service) resolveHarnessSecrets(
	ctx context.Context, project domain.Project,
) (map[string]string, error) {
	values := make(map[string]string, len(project.HarnessSecretNames))
	for _, name := range project.HarnessSecretNames {
		payload, err := s.resolveSecret(ctx, name, domain.SecretHarnessEnv)
		if err != nil {
			return nil, fmt.Errorf("resolve authorized harness secret: %w", err)
		}
		values[name] = payload.Value
	}
	return values, nil
}

func cloneStringMap(value map[string]string) map[string]string {
	if len(value) == 0 {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func clearStringMap(value map[string]string) {
	for key := range value {
		value[key] = strings.Repeat("\x00", len(value[key]))
		delete(value, key)
	}
}
