package app

import (
	"cmp"
	"context"
	"errors"
	"sort"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

// Notes delivery (ADR 0060 section 4). At each session start, including a
// resume after idle pause and a renewal, meridiand sends capsuled the note
// sets this Thread receives, and the Start or Resume metadata gains a
// content-free index. Project notes are a snapshot per session; working
// notes can also be refreshed with sync-notes.

// sessionNotes is what one session start delivers.
type sessionNotes struct {
	delivery  *coordproto.NotesDelivery
	index     *adapterproto.NotesIndex
	delivered []domain.DeliveredNote
	previous  domain.ThreadNotes
	hadRecord bool
}

// projectNotesFor decides whether a Thread receives Project notes. off
// delivers to no Thread; a routine's Thread receives them only when the
// routine opts in; a subagent follows the Project setting, not its lead's
// choice; otherwise the Thread's "Include Project notes" choice wins over
// the setting.
func projectNotesFor(
	delivery domain.NoteDelivery, choice domain.NoteChoice, lead, sub bool, routine *domain.Routine,
) bool {
	switch {
	case delivery == domain.NoteDeliveryOff:
		return false
	case routine != nil:
		return routine.IncludeNotes
	case sub:
		return true
	case choice == domain.NoteChoiceInclude:
		return true
	case choice == domain.NoteChoiceExclude:
		return false
	case delivery == domain.NoteDeliveryLeadsOnly:
		return lead
	default:
		return true
	}
}

// buildSessionNotes reads the sets for one session start. It returns nil
// when this Station has no notes key.
func (s *Service) buildSessionNotes(
	ctx context.Context, thread domain.Thread, capsule domain.Capsule,
) (*sessionNotes, error) {
	if _, err := s.notesKey(); err != nil {
		return nil, nil
	}
	ctx = principal.WithSystem(ctx)
	result := &sessionNotes{delivery: &coordproto.NotesDelivery{}}
	err := s.store.View(ctx, func(reader ports.Reader) error {
		previous, err := reader.GetThreadNotes(ctx, thread.ID)
		if err == nil {
			result.previous, result.hadRecord = previous, true
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		grant, lead, sub, err := treeOf(ctx, reader, thread, capsule)
		if err != nil {
			return err
		}
		var routine *domain.Routine
		fires, err := reader.RoutineFiresByCapsule(ctx, []domain.CapsuleID{capsule.ID})
		if err != nil {
			return err
		}
		if fire, ok := fires[capsule.ID]; ok {
			item, err := reader.GetRoutine(ctx, fire.RoutineID)
			if err != nil && !errors.Is(err, domain.ErrNotFound) {
				return err
			}
			// A fire whose routine is gone delivers nothing.
			routine = &item
		}
		projectScope, projectDEK, err := s.openNoteScope(ctx, reader, capsule.ProjectID, "")
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		defer wipe(projectDEK)
		delivery := domain.NoteDeliveryAllThreads
		if projectScope.ID != "" {
			delivery = projectScope.Delivery
		}
		if projectNotesFor(delivery, result.previous.Choice, lead, sub, routine) {
			result.delivery.DeliverProject = true
			if len(projectDEK) > 0 {
				if result.delivery.Project, err = liveNoteFiles(ctx, reader, projectScope, projectDEK); err != nil {
					return err
				}
			}
		}
		if grant.ID != "" {
			result.delivery.DeliverWorking = true
			if result.delivery.Working, err = s.workingNoteFiles(ctx, reader, grant); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result.index, result.delivered = notesIndexFor(result.delivery, result.previous.Delivered)
	return result, nil
}

// treeOf returns the grant whose working notes a Thread's session
// receives: the lead's own active grant, or the active grant of a subagent
// whose link is open.
func treeOf(
	ctx context.Context, reader ports.Reader, thread domain.Thread, capsule domain.Capsule,
) (domain.CoordinationGrant, bool, bool, error) {
	if capsule.Role == domain.CapsuleSub || capsule.LinkID != "" {
		if capsule.LinkID == "" {
			return domain.CoordinationGrant{}, false, true, nil
		}
		link, err := reader.GetThreadLink(ctx, capsule.LinkID)
		if errors.Is(err, domain.ErrNotFound) {
			return domain.CoordinationGrant{}, false, capsule.Role == domain.CapsuleSub, nil
		}
		if err != nil {
			return domain.CoordinationGrant{}, false, false, err
		}
		sub := capsule.Role == domain.CapsuleSub
		if link.State != domain.LinkActive || link.ChildThreadID != thread.ID || link.ChildCapsuleID != capsule.ID {
			return domain.CoordinationGrant{}, false, sub, nil
		}
		grant, err := reader.GetCoordinationGrant(ctx, link.GrantID)
		if err != nil {
			return domain.CoordinationGrant{}, false, false, err
		}
		if grant.State != domain.GrantActive {
			return domain.CoordinationGrant{}, false, sub, nil
		}
		// A promoted subagent that kept its link stays in the tree.
		return grant, false, true, nil
	}
	grant, err := reader.GetCoordinationGrantByRootThread(ctx, thread.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.CoordinationGrant{}, false, false, nil
	}
	if err != nil {
		return domain.CoordinationGrant{}, false, false, err
	}
	if grant.State != domain.GrantActive {
		return domain.CoordinationGrant{}, false, false, nil
	}
	return grant, true, false, nil
}

// notesIndexFor builds the content-free index and the delivered set, with
// Changed set for every note that differs from the previous session.
func notesIndexFor(
	delivery *coordproto.NotesDelivery, previous []domain.DeliveredNote,
) (*adapterproto.NotesIndex, []domain.DeliveredNote) {
	before := map[string]domain.DeliveredNote{}
	for _, note := range previous {
		before[string(note.Tier)+"/"+note.Name] = note
	}
	index := &adapterproto.NotesIndex{Notes: []adapterproto.NotesEntry{}}
	delivered := []domain.DeliveredNote{}
	add := func(tier domain.NoteTier, files []coordproto.NoteFile) {
		for _, file := range files {
			note := domain.DeliveredNote{
				Tier: tier, Name: file.Name, Revision: file.Revision, Size: len(file.Content),
				Digest: domain.NoteDigest(file.Content),
			}
			delivered = append(delivered, note)
			old, seen := before[string(tier)+"/"+file.Name]
			index.Notes = append(index.Notes, adapterproto.NotesEntry{
				Tier: string(tier), Name: file.Name, Size: note.Size, Revision: file.Revision,
				Changed: len(previous) > 0 && (!seen || old.Revision != file.Revision || old.Digest != note.Digest),
			})
		}
	}
	if delivery.DeliverProject {
		add(domain.NoteTierProject, delivery.Project)
	}
	if delivery.DeliverWorking {
		add(domain.NoteTierWorking, delivery.Working)
	}
	if !delivery.DeliverProject && !delivery.DeliverWorking {
		index = nil
	}
	return index, delivered
}

// projectNotesChanged counts the Project notes that differ between two
// delivered sets, including removals.
func projectNotesChanged(before, after []domain.DeliveredNote) int {
	old := map[string]domain.DeliveredNote{}
	for _, note := range before {
		if note.Tier == domain.NoteTierProject {
			old[note.Name] = note
		}
	}
	changed := 0
	for _, note := range after {
		if note.Tier != domain.NoteTierProject {
			continue
		}
		if prior, ok := old[note.Name]; !ok || prior.Digest != note.Digest {
			changed++
		}
		delete(old, note.Name)
	}
	return changed + len(old)
}

// KindNotesUpdated marks, in a transcript, a later session of the Thread
// that received a different Project-note set than its previous session. It
// is a content-free divider, kept like the compaction marker.
const KindNotesUpdated adapterproto.Kind = "notes_updated"

// recordSessionNotes stores the content-free set a session received and,
// when the Thread had an earlier session whose Project notes differ, appends
// the divider. Whether the harness was sent Start or Resume does not matter:
// Restart session after a failure, or a resume with no harness state, starts
// a new harness session below the same transcript, and the operator reading
// on still needs to see that the notes changed. A Thread's first session,
// including one Start fresh creates, has no earlier set and no divider.
func (s *Service) recordSessionNotes(
	ctx context.Context, thread domain.Thread, capsule domain.Capsule, notes *sessionNotes,
) {
	if notes == nil {
		return
	}
	ctx = principal.WithSystem(context.WithoutCancel(ctx))
	now := s.clock.Now().UTC()
	changed := 0
	if notes.hadRecord && !notes.previous.DeliveredAt.IsZero() {
		changed = projectNotesChanged(notes.previous.Delivered, notes.delivered)
	}
	sort.Slice(notes.delivered, func(i, j int) bool {
		if notes.delivered[i].Tier != notes.delivered[j].Tier {
			return notes.delivered[i].Tier < notes.delivered[j].Tier
		}
		return notes.delivered[i].Name < notes.delivered[j].Name
	})
	_ = s.store.Transact(ctx, func(tx ports.Transaction) error {
		record := notes.previous
		if !notes.hadRecord {
			project, err := tx.GetProject(ctx, capsule.ProjectID)
			if err != nil {
				return err
			}
			record = domain.ThreadNotes{ThreadID: thread.ID, ProjectID: project.ID, OwnerUserID: project.OwnerUserID}
		}
		record.Delivered, record.DeliveredAt, record.UpdatedAt = notes.delivered, now, now
		record.Indexed = false
		if err := tx.PutThreadNotes(ctx, record); err != nil {
			return err
		}
		if changed == 0 {
			return nil
		}
		current, err := tx.GetThread(ctx, thread.ID)
		if err != nil {
			return err
		}
		dek, err := s.unwrapThread(current)
		if err != nil {
			return err
		}
		defer wipe(dek)
		message, err := s.encryptThreadMessage(dek, current, domain.ThreadMessageID(s.ids.NewID()),
			current.MessageCount+1, domain.ThreadRoleSystem, domain.ThreadMessageStatus, domain.ThreadBlockJSON,
			persistedThreadBlock{Event: &ThreadAdapterEvent{Type: KindNotesUpdated, NotesChanged: changed}}, now)
		if err != nil {
			return err
		}
		return tx.AppendThreadMessage(ctx, message)
	})
}

// recordNotesIndexed records whether the harness negotiated the index, once
// its adapter's hello arrives.
func (s *Service) recordNotesIndexed(ctx context.Context, thread domain.ThreadID, indexed bool) {
	ctx = principal.WithSystem(context.WithoutCancel(ctx))
	_ = s.store.Transact(ctx, func(tx ports.Transaction) error {
		record, err := tx.GetThreadNotes(ctx, thread)
		if err != nil || record.Indexed == indexed {
			return nil
		}
		record.Indexed = indexed
		record.UpdatedAt = s.clock.Now().UTC()
		return tx.PutThreadNotes(ctx, record)
	})
}

// SetThreadNoteChoice records a new Thread's "Include Project notes"
// choice. It is written when the Thread is created, before its first
// session, and never changed by a Capsule.
func (s *Service) setThreadNoteChoiceTx(
	ctx context.Context, tx ports.Transaction, project domain.Project, thread domain.ThreadID,
	choice domain.NoteChoice, now time.Time,
) error {
	if choice == domain.NoteChoiceDefault {
		return nil
	}
	if !choice.Valid() {
		return domain.ErrInvalid
	}
	return tx.PutThreadNotes(ctx, domain.ThreadNotes{
		ThreadID: thread, ProjectID: project.ID, OwnerUserID: project.OwnerUserID, Choice: choice,
		Delivered: []domain.DeliveredNote{}, UpdatedAt: now,
	})
}

// ThreadNotesView is what a Thread's details show about notes.
type ThreadNotesView struct {
	Choice      domain.NoteChoice
	Delivered   []domain.DeliveredNote
	DeliveredAt time.Time
	Indexed     bool
}

func (s *Service) GetThreadNotes(ctx context.Context, id domain.ThreadID) (ThreadNotesView, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return ThreadNotesView{}, err
	}
	var result ThreadNotesView
	err := s.store.View(ctx, func(reader ports.Reader) error {
		if _, err := reader.GetThread(ctx, id); err != nil {
			return err
		}
		record, err := reader.GetThreadNotes(ctx, id)
		if errors.Is(err, domain.ErrNotFound) {
			result.Delivered = []domain.DeliveredNote{}
			return nil
		}
		if err != nil {
			return err
		}
		result = ThreadNotesView{Choice: record.Choice, Delivered: record.Delivered,
			DeliveredAt: record.DeliveredAt, Indexed: record.Indexed}
		return nil
	})
	return result, err
}
