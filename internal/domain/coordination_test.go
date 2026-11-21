package domain

import (
	"errors"
	"testing"
	"time"
)

func TestCoordinationPolicyUnattendedLimit(t *testing.T) {
	policy, err := CoordinationPolicy{}.Normalize()
	if err != nil || policy.UnattendedLimit != CoordinationDefaultUnattended ||
		policy.MaxDescendants != CoordinationDefaultDescendants || CoordinationDefaultDescendants != 8 ||
		CoordinationMaxDescendants != 32 || CoordinationSpawnWindow != 24*time.Hour {
		t.Fatalf("defaults = %+v, %v", policy, err)
	}
	for _, limit := range []time.Duration{CoordinationMinUnattended, CoordinationMaxUnattended} {
		if _, err := (CoordinationPolicy{UnattendedLimit: limit}).Normalize(); err != nil {
			t.Errorf("limit %s: %v", limit, err)
		}
	}
	for _, limit := range []time.Duration{
		-time.Second, CoordinationMinUnattended - time.Second, CoordinationMaxUnattended + time.Second,
		CoordinationMinUnattended + time.Millisecond,
	} {
		if _, err := (CoordinationPolicy{UnattendedLimit: limit}).Normalize(); !errors.Is(err, ErrInvalid) {
			t.Errorf("limit %s = %v", limit, err)
		}
	}
}

func TestCoordinationGrantLapse(t *testing.T) {
	start := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	grant := CoordinationGrant{
		State: GrantActive, LastOperatorAt: start, UnattendedLimit: time.Hour,
		MaxChildren: 4, MaxDescendants: 8, MaxLiveCapsules: 4, MaxMessagesPerLink: 50,
	}
	if !grant.Usable(start.Add(59*time.Minute)) || grant.StateAt(start) != GrantActive {
		t.Fatal("open grant is not usable")
	}
	at := start.Add(time.Hour)
	if grant.Usable(at) || !grant.Lapsed(at) || grant.StateAt(at) != GrantLapsed {
		t.Fatal("grant did not lapse at its limit")
	}
	marked := grant
	marked.LapsedAt = at
	// A marked lapse holds even if the clock reads earlier.
	if marked.Usable(start) {
		t.Fatal("marked lapse was usable")
	}
	reopened := marked.Attended(at.Add(time.Minute))
	if !reopened.LapsedAt.IsZero() || !reopened.Usable(at.Add(time.Minute)) ||
		!reopened.LapsesAt().Equal(at.Add(time.Minute+time.Hour)) {
		t.Fatalf("reopened = %+v", reopened)
	}
	if back := reopened.Attended(start); !back.LastOperatorAt.Equal(reopened.LastOperatorAt) {
		t.Fatal("attendance moved backwards")
	}
	for _, state := range []CoordinationGrantState{GrantRevoked, GrantExpired} {
		ended := grant
		ended.State = state
		if ended.Usable(start) || ended.Lapsed(at) || ended.StateAt(at) != state {
			t.Errorf("%s grant = usable %v, lapsed %v", state, ended.Usable(start), ended.Lapsed(at))
		}
	}
	limit := 2 * time.Hour
	patched, err := grant.Apply(CoordinationGrantPatch{UnattendedLimit: &limit})
	if err != nil || patched.UnattendedLimit != limit || !patched.LastOperatorAt.Equal(start) {
		t.Fatalf("patched = %+v, %v", patched, err)
	}
	bad := CoordinationMaxUnattended + time.Hour
	if _, err := grant.Apply(CoordinationGrantPatch{UnattendedLimit: &bad}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("over-long limit = %v", err)
	}
	if !SpawnWindowStart(at).Equal(at.Add(-24 * time.Hour)) {
		t.Fatal("window start")
	}
}
