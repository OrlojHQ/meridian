package domain

import (
	"errors"
	"testing"
	"time"
)

func mustCadence(t *testing.T, expression, zone string) Cadence {
	t.Helper()
	cadence, err := ParseCadence(expression, zone)
	if err != nil {
		t.Fatalf("ParseCadence(%q, %q): %v", expression, zone, err)
	}
	return cadence
}

func TestCadenceNextWeekly(t *testing.T) {
	chicago, _ := time.LoadLocation("America/Chicago")
	cadence := mustCadence(t, "0 6 * * 1", "America/Chicago")
	// Wednesday 18 November 2026, 10:00 in Chicago.
	from := time.Date(2026, 11, 18, 10, 0, 0, 0, chicago)
	next := cadence.Next(from)
	want := time.Date(2026, 11, 23, 6, 0, 0, 0, chicago)
	if !next.Equal(want) {
		t.Fatalf("Next = %v, want %v", next.In(chicago), want)
	}
	if again := cadence.Next(next); !again.Equal(want.AddDate(0, 0, 7)) {
		t.Fatalf("second Next = %v", again.In(chicago))
	}
}

func TestCadenceFieldsAndMacros(t *testing.T) {
	cases := []struct {
		expression string
		from, want time.Time
	}{
		{"*/30 9-10 * * *", time.Date(2026, 1, 1, 9, 10, 0, 0, time.UTC), time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC)},
		{"*/30 9-10 * * *", time.Date(2026, 1, 1, 10, 30, 0, 0, time.UTC), time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)},
		{"15,45 * * * *", time.Date(2026, 1, 1, 0, 15, 0, 0, time.UTC), time.Date(2026, 1, 1, 0, 45, 0, 0, time.UTC)},
		{"@daily", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{"0 0 29 2 *", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)},
		// Sunday may be 0 or 7.
		{"0 12 * * 7", time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)},
		// Both day fields restricted: either matches.
		{"0 0 1 * 1", time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		got := mustCadence(t, tc.expression, "UTC").Next(tc.from)
		if !got.Equal(tc.want) {
			t.Errorf("%q after %v = %v, want %v", tc.expression, tc.from, got, tc.want)
		}
	}
}

func TestCadenceDaylightSaving(t *testing.T) {
	newYork, _ := time.LoadLocation("America/New_York")
	// 02:30 does not exist on 8 March 2026: the fire that day is skipped.
	cadence := mustCadence(t, "30 2 * * *", "America/New_York")
	next := cadence.Next(time.Date(2026, 3, 7, 12, 0, 0, 0, newYork))
	if want := time.Date(2026, 3, 9, 2, 30, 0, 0, newYork); !next.Equal(want) {
		t.Fatalf("spring forward Next = %v, want %v", next.In(newYork), want)
	}
	// 01:30 happens twice on 1 November 2026: it fires once.
	cadence = mustCadence(t, "30 1 * * *", "America/New_York")
	first := cadence.Next(time.Date(2026, 10, 31, 12, 0, 0, 0, newYork))
	second := cadence.Next(first)
	if first.In(newYork).Day() != 1 || second.In(newYork).Day() != 2 {
		t.Fatalf("fall back fires = %v, %v", first.In(newYork), second.In(newYork))
	}
}

func TestParseCadenceRejects(t *testing.T) {
	for _, tc := range []struct{ expression, zone string }{
		{"", "UTC"},
		{"* * * *", "UTC"},
		{"60 * * * *", "UTC"},
		{"0 24 * * *", "UTC"},
		{"0 0 0 * *", "UTC"},
		{"5-1 * * * *", "UTC"},
		{"*/0 * * * *", "UTC"},
		{"0 0 * * *", ""},
		{"0 0 * * *", "Local"},
		{"0 0 * * *", "Mars/Olympus"},
	} {
		if _, err := ParseCadence(tc.expression, tc.zone); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseCadence(%q, %q) = %v, want ErrInvalid", tc.expression, tc.zone, err)
		}
	}
}

func TestCadenceSpacing(t *testing.T) {
	from := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	if err := mustCadence(t, "*/15 * * * *", "UTC").CheckSpacing(from, 200); err != nil {
		t.Fatalf("every 15 minutes: %v", err)
	}
	if err := mustCadence(t, "*/10 * * * *", "UTC").CheckSpacing(from, 200); !errors.Is(err, ErrInvalid) {
		t.Fatalf("every 10 minutes = %v, want ErrInvalid", err)
	}
	if err := mustCadence(t, "0,5 9 * * *", "UTC").CheckSpacing(from, 200); !errors.Is(err, ErrInvalid) {
		t.Fatalf("five minutes apart = %v, want ErrInvalid", err)
	}
	if err := mustCadence(t, "0 0 30 2 *", "UTC").CheckSpacing(from, 10); !errors.Is(err, ErrInvalid) {
		t.Fatalf("never fires = %v, want ErrInvalid", err)
	}
}

func TestValidScheduleName(t *testing.T) {
	for name, want := range map[string]bool{
		"deps-weekly": true, "a": true, "": false, "Deps": false, "has space": false,
		"x123456789012345678901234567890123456789012345678": false,
	} {
		if got := ValidScheduleName(name); got != want {
			t.Errorf("ValidScheduleName(%q) = %v, want %v", name, got, want)
		}
	}
}
