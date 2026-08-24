package httpapi

import (
	"crypto/sha256"
	"sync"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

const maxOutstandingAttachTickets = 256

type attachTicketRegistry struct {
	mu      sync.Mutex
	max     int
	entries map[[sha256.Size]byte]attachTicket
}

func newAttachTicketRegistry(max int) *attachTicketRegistry {
	if max <= 0 {
		max = maxOutstandingAttachTickets
	}
	return &attachTicketRegistry{
		max:     max,
		entries: make(map[[sha256.Size]byte]attachTicket),
	}
}

func (r *attachTicketRegistry) mint(value string, ticket attachTicket, now time.Time) error {
	if value == "" {
		return domain.ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	digest := sha256.Sum256([]byte(value))
	if _, exists := r.entries[digest]; exists {
		return domain.ErrConflict
	}
	if len(r.entries) >= r.max {
		return domain.ErrResourceExhausted
	}
	r.entries[digest] = ticket
	return nil
}

func (r *attachTicketRegistry) take(value string, now time.Time) (attachTicket, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	digest := sha256.Sum256([]byte(value))
	ticket, exists := r.entries[digest]
	if !exists {
		return attachTicket{}, false
	}
	delete(r.entries, digest)
	return ticket, true
}

func (r *attachTicketRegistry) pruneLocked(now time.Time) {
	for digest, ticket := range r.entries {
		if !ticket.ExpiresAt.After(now) {
			delete(r.entries, digest)
		}
	}
}

func (r *attachTicketRegistry) size(now time.Time) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	return len(r.entries)
}
