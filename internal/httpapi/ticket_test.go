package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

func TestAttachTicketsEnforceExpiryScopeSingleUseAndOrigin(t *testing.T) {
	server := &Server{tickets: newAttachTicketRegistry(16)}
	tests := []struct {
		name   string
		ticket attachTicket
		runID  string
		origin string
	}{
		{
			name: "expired", runID: "run-1",
			ticket: attachTicket{RunID: "run-1", ExpiresAt: time.Now().Add(-time.Second)},
		},
		{
			name: "scope", runID: "run-2",
			ticket: attachTicket{RunID: "run-1", ExpiresAt: time.Now().Add(time.Minute)},
		},
		{
			name: "origin", runID: "run-1", origin: "https://attacker.example",
			ticket: attachTicket{RunID: domain.RunID("run-1"), ExpiresAt: time.Now().Add(time.Minute)},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if err := server.tickets.mint(testCase.name, testCase.ticket, time.Now()); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(
				http.MethodGet, "/runs/"+testCase.runID+"/attach?ticket="+testCase.name, nil,
			)
			request.SetPathValue("runId", testCase.runID)
			if testCase.origin != "" {
				request.Header.Set("Origin", testCase.origin)
			}
			response := httptest.NewRecorder()
			server.attachRun(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d", response.Code)
			}
			replay := httptest.NewRecorder()
			server.attachRun(replay, request)
			if replay.Code != http.StatusBadRequest {
				t.Fatalf("reused ticket status = %d", replay.Code)
			}
		})
	}
}

func TestAttachTicketRegistryPrunesUnusedExpiryAndBoundsCapacity(t *testing.T) {
	now := time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)
	registry := newAttachTicketRegistry(2)
	if err := registry.mint("expired", attachTicket{
		RunID: "expired", ExpiresAt: now.Add(time.Second),
	}, now); err != nil {
		t.Fatal(err)
	}
	if registry.size(now.Add(2*time.Second)) != 0 {
		t.Fatal("unused expired ticket was not pruned")
	}
	for _, value := range []string{"one", "two"} {
		if err := registry.mint(value, attachTicket{
			RunID: domain.RunID(value), ExpiresAt: now.Add(time.Minute),
		}, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.mint("three", attachTicket{
		RunID: "three", ExpiresAt: now.Add(time.Minute),
	}, now); !errors.Is(err, domain.ErrResourceExhausted) {
		t.Fatalf("capacity error = %v", err)
	}
	if registry.size(now) != 2 {
		t.Fatalf("registry size = %d", registry.size(now))
	}
	response := httptest.NewRecorder()
	writeError(response, domain.ErrResourceExhausted)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("capacity HTTP status = %d", response.Code)
	}
}

func TestAttachTicketTakeIsAtomicUnderConcurrency(t *testing.T) {
	now := time.Now().UTC()
	registry := newAttachTicketRegistry(4)
	if err := registry.mint("single", attachTicket{
		RunID: "run-1", ExpiresAt: now.Add(time.Minute),
	}, now); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wait sync.WaitGroup
	for range 64 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, ok := registry.take("single", now); ok {
				successes.Add(1)
			}
		}()
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful takes = %d", successes.Load())
	}
	if registry.size(now) != 0 {
		t.Fatal("single-use ticket remained in registry")
	}
}

func TestAttachTicketRegistryStoresOnlyDigests(t *testing.T) {
	now := time.Now().UTC()
	registry := newAttachTicketRegistry(1)
	plaintext := "plaintext-ticket-value-that-must-not-be-retained"
	if err := registry.mint(plaintext, attachTicket{
		RunID: "run-1", ExpiresAt: now.Add(time.Minute),
	}, now); err != nil {
		t.Fatal(err)
	}
	for digest := range registry.entries {
		if string(digest[:]) == plaintext {
			t.Fatal("attach ticket plaintext was retained as a registry key")
		}
	}
	if _, ok := registry.take(plaintext, now); !ok {
		t.Fatal("digest-backed ticket was not redeemable")
	}
}
