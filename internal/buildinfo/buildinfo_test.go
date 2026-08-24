package buildinfo

import "testing"

func TestInfoString(t *testing.T) {
	info := Info{
		Version: "1.2.3",
		Commit:  "abc123",
		Date:    "2026-08-23",
	}

	const want = "1.2.3 (commit abc123, built 2026-08-23)"
	if got := info.String(); got != want {
		t.Fatalf("Info.String() = %q, want %q", got, want)
	}
}

func TestCurrent(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, Date
	t.Cleanup(func() {
		Version, Commit, Date = oldVersion, oldCommit, oldDate
	})

	Version, Commit, Date = "test", "deadbeef", "now"
	got := Current()
	if got.Version != Version || got.Commit != Commit || got.Date != Date {
		t.Fatalf("Current() = %#v, want current package metadata", got)
	}
}
