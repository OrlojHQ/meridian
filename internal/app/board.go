package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// The board is built from content-free summaries: Capsule, Thread, Run,
// Delivery and ThreadLink rows plus the in-memory Thread attention. It never
// reads transcript content beyond that derivation and never polls Git.

// BoardStatus is the group a board row sorts into.
type BoardStatus string

const (
	BoardNeedsYou       BoardStatus = "needs_you"
	BoardWorking        BoardStatus = "working"
	BoardPROpen         BoardStatus = "pr_open"
	BoardReadyForReview BoardStatus = "ready_for_review"
	BoardIdle           BoardStatus = "idle"
	BoardPaused         BoardStatus = "paused"
	BoardSealed         BoardStatus = "sealed"
)

var boardStatusOrder = []BoardStatus{
	BoardNeedsYou, BoardWorking, BoardPROpen, BoardReadyForReview, BoardIdle, BoardPaused, BoardSealed,
}

// BoardAttention says why a row needs the operator.
type BoardAttention string

const (
	BoardAttentionPermission BoardAttention = "permission"
	BoardAttentionInput      BoardAttention = "input"
	BoardAttentionFailed     BoardAttention = "failed"
)

// BoardExpand chooses which helper Capsules get their own rows.
type BoardExpand string

const (
	// BoardExpandAttention lists main Capsules plus helpers that need the
	// operator. It is the default.
	BoardExpandAttention BoardExpand = "attention"
	BoardExpandAll       BoardExpand = "all"
	BoardExpandNone      BoardExpand = "none"
)

// boardMaxCapsules bounds one board read; the page says when it was cut.
const boardMaxCapsules = 500

type BoardQuery struct {
	ProjectID domain.ProjectID
	Expand    BoardExpand
}

// ChildRollup counts a lead's helpers by state.
type ChildRollup struct {
	Total    int
	Working  int
	Idle     int
	Done     int
	NeedsYou int
	Failed   int
	// Archived counts auto-archived helpers. They are also counted as done.
	Archived int
}

type BoardItem struct {
	Capsule        domain.Capsule
	Thread         *domain.Thread
	Run            *domain.Run
	Attention      ThreadAttention
	LatestDelivery *domain.Delivery
	Link           *domain.ThreadLink
	Status         BoardStatus
	Since          time.Time
	NeedsYou       BoardAttention
	Rollup         *ChildRollup
	// Surfaced marks a helper listed on its own because it needs the operator.
	Surfaced bool
}

type Board struct {
	Items     []BoardItem
	Truncated bool
}

func (s *Service) Board(ctx context.Context, query BoardQuery) (Board, error) {
	switch query.Expand {
	case "":
		query.Expand = BoardExpandAttention
	case BoardExpandAttention, BoardExpandAll, BoardExpandNone:
	default:
		return Board{}, fmt.Errorf("%w: expand must be attention, all or none", domain.ErrInvalid)
	}
	capsules, truncated, err := s.boardCapsules(ctx, query.ProjectID)
	if err != nil {
		return Board{}, err
	}
	items := make([]BoardItem, 0, len(capsules))
	byThread := make(map[domain.ThreadID]int)
	byCapsule := make(map[domain.CapsuleID]int)
	for _, capsule := range capsules {
		item, err := s.boardItem(ctx, capsule)
		if err != nil {
			return Board{}, err
		}
		byCapsule[capsule.ID] = len(items)
		if item.Thread != nil {
			byThread[item.Thread.ID] = len(items)
		}
		items = append(items, item)
	}
	for index := range items {
		item := &items[index]
		if item.Thread == nil {
			continue
		}
		links, err := s.ListThreadLinks(ctx, item.Thread.ID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return Board{}, err
		}
		if len(links) == 0 {
			continue
		}
		rollup := ChildRollup{Total: len(links)}
		for _, link := range links {
			child, ok := byCapsule[link.ChildCapsuleID]
			var status BoardStatus
			if ok {
				status = items[child].Status
			}
			if !link.ArchivedAt.IsZero() {
				rollup.Archived++
				rollup.Done++
				continue
			}
			switch {
			case status == BoardNeedsYou:
				rollup.NeedsYou++
			case link.State == domain.LinkClosed && link.CloseReason == domain.LinkFailed:
				rollup.Failed++
			case link.State == domain.LinkClosed:
				rollup.Done++
			case status == BoardWorking:
				rollup.Working++
			default:
				rollup.Idle++
			}
		}
		item.Rollup = &rollup
	}

	visible := make([]BoardItem, 0, len(items))
	for _, item := range items {
		if item.Capsule.Role == domain.CapsuleSub {
			_, leadListed := byThread[item.Capsule.LeadThreadID]
			item.Surfaced = item.Status == BoardNeedsYou
			switch {
			case !leadListed:
				// A helper whose lead is gone is never hidden.
			case query.Expand == BoardExpandAll:
			case query.Expand == BoardExpandAttention && item.Surfaced:
			default:
				continue
			}
		}
		visible = append(visible, item)
	}
	slices.SortStableFunc(visible, func(a, b BoardItem) int {
		if order := slices.Index(boardStatusOrder, a.Status) - slices.Index(boardStatusOrder, b.Status); order != 0 {
			return order
		}
		if a.Status == BoardNeedsYou {
			return a.Since.Compare(b.Since)
		}
		return b.Since.Compare(a.Since)
	})
	return Board{Items: visible, Truncated: truncated}, nil
}

func (s *Service) boardCapsules(ctx context.Context, projectID domain.ProjectID) ([]domain.Capsule, bool, error) {
	var capsules []domain.Capsule
	truncated := false
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var projects []domain.ProjectID
		if projectID != "" {
			if _, err := reader.GetProject(ctx, projectID); err != nil {
				return err
			}
			projects = []domain.ProjectID{projectID}
		} else {
			for offset := 0; ; {
				page, more, err := reader.ListProjects(ctx, ports.Page{Offset: offset, Limit: 100})
				if err != nil {
					return err
				}
				for _, project := range page {
					projects = append(projects, project.ID)
				}
				if !more {
					break
				}
				offset += len(page)
			}
		}
		for _, id := range projects {
			for offset := 0; ; {
				page, more, err := reader.ListCapsules(ctx, id, "", ports.Page{Offset: offset, Limit: 100})
				if err != nil {
					return err
				}
				for _, capsule := range page {
					if capsule.State == domain.CapsuleDeleted {
						continue
					}
					if len(capsules) == boardMaxCapsules {
						truncated = true
						return nil
					}
					capsules = append(capsules, capsule)
				}
				if !more {
					break
				}
				offset += len(page)
			}
		}
		return nil
	})
	return capsules, truncated, err
}

func (s *Service) boardItem(ctx context.Context, capsule domain.Capsule) (BoardItem, error) {
	item := BoardItem{Capsule: capsule}
	err := s.store.View(ctx, func(reader ports.Reader) error {
		threads, _, err := reader.ListThreads(ctx, capsule.ID, ports.Page{Limit: 100})
		if err != nil {
			return err
		}
		item.Thread = primaryThread(threads)
		if item.Thread != nil && item.Thread.CurrentRunID != "" {
			run, err := reader.GetRun(ctx, item.Thread.CurrentRunID)
			if err == nil {
				item.Run = &run
			} else if !errors.Is(err, domain.ErrNotFound) {
				return err
			}
		}
		if item.Run == nil {
			run, err := reader.LatestRun(ctx, capsule.ID)
			if err == nil {
				item.Run = &run
			} else if !errors.Is(err, domain.ErrNotFound) {
				return err
			}
		}
		deliveries, _, err := reader.ListDeliveries(ctx, capsule.ID, ports.Page{Limit: 1})
		if err != nil {
			return err
		}
		if len(deliveries) > 0 {
			item.LatestDelivery = &deliveries[0]
		}
		if capsule.LinkID != "" {
			link, err := reader.GetThreadLink(ctx, capsule.LinkID)
			if err == nil {
				item.Link = &link
			} else if !errors.Is(err, domain.ErrNotFound) {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return BoardItem{}, err
	}
	if item.Thread != nil {
		if item.Attention, err = s.ThreadAttention(ctx, *item.Thread); err != nil &&
			!errors.Is(err, domain.ErrTranscriptCorrupt) && !errors.Is(err, domain.ErrNotFound) {
			return BoardItem{}, err
		}
	}
	item.Status, item.Since, item.NeedsYou = boardStatus(item)
	return item, nil
}

// primaryThread is the Thread a Capsule's row stands for: its most recently
// updated active Thread, or else its most recent retained one.
func primaryThread(threads []domain.Thread) *domain.Thread {
	var active, retained *domain.Thread
	for index := range threads {
		thread := &threads[index]
		switch thread.State {
		case domain.ThreadDeleted:
			continue
		case domain.ThreadActive:
			if active == nil || thread.UpdatedAt.After(active.UpdatedAt) {
				active = thread
			}
		}
		if retained == nil || thread.UpdatedAt.After(retained.UpdatedAt) {
			retained = thread
		}
	}
	if active != nil {
		return active
	}
	return retained
}

func boardStatus(item BoardItem) (BoardStatus, time.Time, BoardAttention) {
	capsule, run := item.Capsule, item.Run
	switch {
	case capsule.State == domain.CapsuleSealed:
		return BoardSealed, capsule.UpdatedAt, ""
	case capsule.State == domain.CapsuleFailed:
		return BoardNeedsYou, capsule.UpdatedAt, BoardAttentionFailed
	case capsule.State == domain.CapsuleDeleting, capsule.DesiredState == domain.IntentDeleted,
		capsule.State == domain.CapsuleCreating, capsule.State == domain.CapsulePreparing:
		return BoardWorking, capsule.UpdatedAt, ""
	case capsule.State == domain.CapsulePaused && capsule.DesiredState == domain.IntentReady:
		return BoardWorking, capsule.UpdatedAt, ""
	case capsule.State == domain.CapsulePaused:
		if delivered(item.LatestDelivery) {
			return BoardPROpen, item.LatestDelivery.UpdatedAt, ""
		}
		return BoardPaused, capsule.UpdatedAt, ""
	case capsule.DesiredState != domain.IntentReady, capsule.Maintenance != "":
		return BoardWorking, capsule.UpdatedAt, ""
	}
	if awaiting := item.Attention.Awaiting; awaiting != nil {
		reason := BoardAttentionInput
		if awaiting.Kind == ThreadAwaitingPermission {
			reason = BoardAttentionPermission
		}
		return BoardNeedsYou, awaiting.Since, reason
	}
	if run != nil && run.State == domain.RunFailed {
		return BoardNeedsYou, runFinishedAt(*run), BoardAttentionFailed
	}
	if delivery := item.LatestDelivery; delivery != nil &&
		delivery.State != domain.DeliverySucceeded && delivery.State != domain.DeliveryFailed {
		return BoardWorking, delivery.UpdatedAt, ""
	}
	sessionIdle := item.Attention.Turn == ThreadTurnIdle
	if run != nil && !run.State.Terminal() && !sessionIdle {
		since := run.CreatedAt
		if !run.StartedAt.IsZero() {
			since = run.StartedAt
		}
		return BoardWorking, since, ""
	}
	if delivered(item.LatestDelivery) {
		return BoardPROpen, item.LatestDelivery.UpdatedAt, ""
	}
	switch {
	case run == nil:
		return BoardIdle, capsule.UpdatedAt, ""
	case sessionIdle:
		since := run.UpdatedAt
		if item.Thread != nil {
			since = item.Thread.UpdatedAt
		}
		return BoardIdle, since, ""
	case run.State.Terminal():
		return BoardReadyForReview, runFinishedAt(*run), ""
	}
	return BoardIdle, run.UpdatedAt, ""
}

func delivered(delivery *domain.Delivery) bool {
	return delivery != nil && delivery.State == domain.DeliverySucceeded && delivery.ResultPullRequestURL != ""
}

func runFinishedAt(run domain.Run) time.Time {
	if !run.FinishedAt.IsZero() {
		return run.FinishedAt
	}
	return run.UpdatedAt
}
