package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// generatedThreadName matches an automatic Capsule id of the form thread-
// plus hex. Names an operator typed, including other thread- prefixes, do
// not match.
var generatedThreadName = regexp.MustCompile(`(?i)^thread-[0-9a-f]{6,64}$`)

const capsuleTitleLimit = 48

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

// backfillCapsuleTitles gives an automatic thread- id a short title from the
// operator's first message. A name the operator chose is left alone. The
// title is stored once, so later reads of a list do not decrypt the
// transcript. Decrypt or write failures leave the existing name in place.
func (s *Service) backfillCapsuleTitles(ctx context.Context, capsules []domain.Capsule) []domain.Capsule {
	if len(capsules) == 0 {
		return capsules
	}
	type pending struct {
		index int
		title string
	}
	var found []pending
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		for i := range capsules {
			title := s.derivedCapsuleTitle(ctx, reader, capsules[i])
			if title != "" {
				found = append(found, pending{index: i, title: title})
			}
		}
		return nil
	}); err != nil {
		return capsules
	}
	for _, item := range found {
		updated, ok := s.persistDerivedTitle(ctx, capsules[item.index], item.title)
		if !ok {
			continue
		}
		capsules[item.index].Name = updated.Name
		capsules[item.index].UpdatedAt = updated.UpdatedAt
		capsules[item.index].ResourceVersion = updated.ResourceVersion
	}
	return capsules
}

func (s *Service) derivedCapsuleTitle(
	ctx context.Context,
	reader ports.Reader,
	capsule domain.Capsule,
) string {
	if !strings.HasPrefix(capsule.Name, "thread-") || s.transcriptKey == nil {
		return ""
	}
	intent, err := reader.GetProjectThreadIntentByCapsule(ctx, capsule.ID)
	hasIntent := err == nil
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return ""
	}
	if hasIntent {
		if intent.RequestedName != "" || capsule.Name != intent.CapsuleName {
			return ""
		}
	} else if !generatedThreadName.MatchString(capsule.Name) {
		return ""
	}
	prompt := s.firstOperatorPrompt(ctx, reader, capsule.ID, intent, hasIntent)
	title := capsuleTitleFromMessage(prompt)
	if title == "" || title == capsule.Name {
		return ""
	}
	checked, err := requireCapsuleTitle(title)
	if err != nil || checked == capsule.Name {
		return ""
	}
	return checked
}

func (s *Service) firstOperatorPrompt(
	ctx context.Context,
	reader ports.Reader,
	capsuleID domain.CapsuleID,
	intent domain.ProjectThreadIntent,
	hasIntent bool,
) string {
	threads, _, err := reader.ListThreads(ctx, capsuleID, ports.Page{Limit: 20})
	if err != nil {
		return ""
	}
	for _, thread := range threads {
		messages, _, err := reader.ListThreadMessages(ctx, thread.ID, 0, 50)
		if err != nil {
			return ""
		}
		for _, message := range messages {
			if message.Role != domain.ThreadRoleUser {
				continue
			}
			payload, err := s.decryptPersistedMessage(thread, message)
			if err != nil {
				return ""
			}
			if payload.Origin != nil || strings.TrimSpace(payload.Content) == "" {
				continue
			}
			return payload.Content
		}
	}
	if !hasIntent {
		return ""
	}
	payload, err := s.pendingBlock(ctx, reader, intent)
	if err != nil || payload.Origin != nil {
		return ""
	}
	return payload.Content
}

func (s *Service) persistDerivedTitle(
	ctx context.Context,
	capsule domain.Capsule,
	title string,
) (domain.Capsule, bool) {
	var result domain.Capsule
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetCapsule(ctx, capsule.ID)
		if err != nil {
			return err
		}
		if current.Name != capsule.Name || current.ResourceVersion != capsule.ResourceVersion {
			result = current
			return nil
		}
		if err := current.CanMutate(); err != nil {
			result = current
			return nil
		}
		previous := current.ResourceVersion
		current.Name = title
		current.UpdatedAt = s.clock.Now().UTC()
		current.ResourceVersion++
		if err := tx.UpdateCapsule(ctx, current, previous); err != nil {
			return err
		}
		if err := s.appendEvent(
			ctx, tx, "capsule", string(current.ID), "capsule.rename", current.ResourceVersion, nil,
		); err != nil {
			return err
		}
		result = current
		return nil
	})
	if err != nil || result.Name != title {
		return capsule, false
	}
	return result, true
}

var (
	titleHeading = regexp.MustCompile(`^#+\s*`)
	titleMarkup  = regexp.MustCompile("[`*_~>]")
	titleSpace   = regexp.MustCompile(`\s+`)
	titleTail    = regexp.MustCompile(`[.,:;!-]+$`)
)

func capsuleTitleFromMessage(content string) string {
	text := titleSpace.ReplaceAllString(stripTitleControls(content), " ")
	text = strings.TrimSpace(titleMarkup.ReplaceAllString(titleHeading.ReplaceAllString(text, ""), " "))
	text = titleSpace.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	sentence := text
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '.', '!', '?':
			if i+1 < len(text) && unicode.IsSpace(rune(text[i+1])) {
				sentence = text[:i+1]
				i = len(text)
			}
		}
	}
	source := text
	if len([]rune(sentence)) <= capsuleTitleLimit {
		source = sentence
	}
	runes := []rune(source)
	if len(runes) > capsuleTitleLimit {
		runes = runes[:capsuleTitleLimit]
	}
	word := string(runes)
	if len([]rune(source)) > capsuleTitleLimit {
		if idx := strings.LastIndex(word, " "); idx > 16 {
			word = word[:idx]
		}
	}
	return strings.TrimSpace(titleTail.ReplaceAllString(word, ""))
}

func stripTitleControls(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case r == '\n' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || unicode.Is(unicode.Cf, r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
