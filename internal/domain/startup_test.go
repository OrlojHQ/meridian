package domain

import (
	"strings"
	"testing"
	"time"
)

func TestStartupProgressBoundsAndSanitizes(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	progress, err := NewStartupProgress(
		"capsule-1", PhasePullingImage, ImagePullPhases(), MaxPullBytes, MaxPullBytes, now,
	)
	if err != nil || progress.Pull == nil || progress.Pull.CurrentBytes != MaxPullBytes {
		t.Fatalf("max bytes = %#v, %v", progress, err)
	}
	if _, err := NewStartupProgress(
		"capsule-1", PhasePullingImage, ImagePullPhases(), MaxPullBytes+1, 0, now,
	); err == nil {
		t.Fatal("oversized byte count was accepted")
	}
	if _, err := NewStartupProgress(
		"capsule-1", StartupPhase("rm -rf /"), []StartupPhase{StartupPhase("rm -rf /")}, 1, 1, now,
	); err == nil {
		t.Fatal("hostile phase name was accepted")
	}
	clamped, err := NewStartupProgress(
		"capsule-1", PhasePullingImage, []StartupPhase{PhasePullingImage}, 500, 100, now,
	)
	if err != nil || clamped.Pull.CurrentBytes != 100 || clamped.Pull.TotalBytes != 100 {
		t.Fatalf("clamp = %#v, %v", clamped.Pull, err)
	}
	unknown, err := NewStartupProgress(
		"capsule-1", PhasePullingImage, []StartupPhase{PhasePullingImage}, 10, 0, now,
	)
	if err != nil || unknown.Pull.TotalBytes != 0 {
		t.Fatalf("unknown total = %#v, %v", unknown.Pull, err)
	}
	creating, err := NewStartupProgress(
		"capsule-1", PhaseCreatingContainer, LocalImagePhases(), 10, 10, now,
	)
	if err != nil || creating.Pull != nil {
		t.Fatalf("non-pull progress = %#v, %v", creating, err)
	}
	if _, err := NewStartupProgress(
		"capsule-1", PhaseConnecting, []StartupPhase{PhaseConnecting, PhasePullingImage}, 0, 0, now,
	); err == nil {
		t.Fatal("out-of-order phases were accepted")
	}
	data, err := StartupEventData(clamped)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "rm -rf") || !strings.Contains(string(data), `"currentBytes":100`) {
		t.Fatalf("event data = %s", data)
	}
}

func TestFormatStartupUsesOnlyNumbersAndPhaseNames(t *testing.T) {
	if got := FormatStartup(PhasePullingImage, 312_000_000, 540_000_000); got != "Pulling image · 312 MB / 540 MB · 57%" {
		t.Fatalf("determinate = %q", got)
	}
	if got := FormatStartup(PhasePullingImage, 12, 0); got != "Pulling image · 12 B" {
		t.Fatalf("indeterminate = %q", got)
	}
	if got := FormatStartup(PhaseConnecting, 0, 0); got != "Connecting" {
		t.Fatalf("phase = %q", got)
	}
	if FormatStartup(StartupPhase("rm -rf"), 1, 1) != "" {
		t.Fatal("hostile phase was formatted")
	}
}

func TestIncludePhaseKeepsCanonicalOrder(t *testing.T) {
	phases := IncludePhase(
		[]StartupPhase{PhaseCreatingContainer, PhaseConnecting},
		PhasePullingImage,
	)
	phases = IncludePhase(phases, PhaseStartingHarness)
	want := []StartupPhase{
		PhasePullingImage, PhaseCreatingContainer, PhaseStartingHarness, PhaseConnecting,
	}
	if len(phases) != len(want) {
		t.Fatalf("phases = %#v", phases)
	}
	for i := range want {
		if phases[i] != want[i] {
			t.Fatalf("phases = %#v", phases)
		}
	}
	again := IncludePhase(phases, PhasePullingImage)
	if len(again) != len(phases) {
		t.Fatalf("duplicate phase inserted: %#v", again)
	}
}

func TestClassifyPullFailureDropsRegistryText(t *testing.T) {
	secret := "ghcr-token-SECRET-VALUE"
	cases := []struct {
		text string
		code PullFailureCode
	}{
		{"pull access denied for ghcr.io/orlojhq/capsule, may require 'docker login': " + secret, PullAuthRequired},
		{"unauthorized: authentication required " + secret, PullAuthRequired},
		{"manifest for ghcr.io/orlojhq/missing:v9 not found: " + secret, PullImageNotFound},
		{"manifest unknown: " + secret, PullImageNotFound},
		{"Get https://ghcr.io/v2/: dial tcp: lookup ghcr.io: no such host " + secret, PullRegistryUnreachable},
		{"net/http: TLS handshake timeout " + secret, PullRegistryUnreachable},
		{"something unexpected " + secret, ""},
	}
	for _, tc := range cases {
		code := ClassifyPullText(tc.text)
		if code != tc.code {
			t.Fatalf("classify %q = %q, want %q", tc.text, code, tc.code)
		}
		err := &StartupFailure{Code: code}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "ghcr.io") {
			t.Fatalf("public error retained registry text: %s", err.Error())
		}
		if err.Error() != code.Message() {
			t.Fatalf("message = %q", err.Error())
		}
	}
	buried := strings.Repeat("a", 520) + "unauthorized"
	if ClassifyPullText(buried) != "" {
		t.Fatal("classification scanned past the bounded prefix")
	}
	failure := ClassifyImagePullWait("ImagePullBackOff", "unauthorized: "+secret)
	if failure == nil || failure.Code != PullAuthRequired || strings.Contains(failure.Error(), secret) {
		t.Fatalf("wait classification = %#v", failure)
	}
	if ClassifyImagePullWait("ContainerCreating", "unauthorized") != nil {
		t.Fatal("unrelated wait reason was classified as a pull failure")
	}
}
