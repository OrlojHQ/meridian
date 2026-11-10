package docker

import (
	"context"
	"errors"
	"io"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/moby/moby/api/types/jsonstream"
)

type pullStream struct {
	io.Reader
}

func (pullStream) Close() error { return nil }
func (pullStream) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(func(jsonstream.Message, error) bool) {}
}
func (pullStream) Wait(context.Context) error { return nil }

func TestPullProgressParsesBoundsAndDropsText(t *testing.T) {
	secret := "registry-token-SECRET"
	body := strings.Join([]string{
		`{"status":"Pulling fs layer","id":"sha256:aaa"}`,
		`{"status":"Downloading","id":"sha256:aaa","progressDetail":{"current":100,"total":400}}`,
		`{"status":"` + secret + `","id":"sha256:aaa","progressDetail":{"current":1,"total":1}}`,
		`{"status":"Downloading","id":"../etc/passwd","progressDetail":{"current":50,"total":50}}`,
		`{"status":"Downloading","id":"sha256:bbb","progressDetail":{"current":50,"total":100}}`,
		`{"status":"Downloading","id":"sha256:aaa","progressDetail":{"current":40,"total":400}}`,
		`{"status":"Downloading","id":"sha256:ccc","progressDetail":{"current":1099511627777,"total":1}}`,
		`{"status":"Download complete","id":"sha256:bbb"}`,
		`{"status":"Downloading","id":"sha256:aaa","progressDetail":{"current":400,"total":400}}`,
	}, "\n") + "\n"
	var reports []ports.StartupReport
	plan := &ports.StartupPublisher{Report: func(report ports.StartupReport) {
		reports = append(reports, report)
	}}
	plan.SetPhases(domain.ImagePullPhases())
	err := readPullProgress(context.Background(), pullStream{strings.NewReader(body)}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) == 0 {
		t.Fatal("no progress reported")
	}
	last := reports[len(reports)-1]
	if last.Phase != domain.PhasePullingImage || last.CurrentBytes != 500 || last.TotalBytes != 500 {
		t.Fatalf("progress = %+v", last)
	}
	for _, report := range reports {
		encoded, _ := domain.StartupEventData(mustProgress(t, report))
		if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "passwd") {
			t.Fatalf("report retained untrusted text: %s", encoded)
		}
		if report.CurrentBytes < 0 || report.CurrentBytes > report.TotalBytes && report.TotalBytes > 0 {
			t.Fatalf("jitter or overflow: %+v", report)
		}
	}
	// A later smaller current for sha256:aaa must not move the total backward.
	var previous int64
	for _, report := range reports {
		if report.CurrentBytes < previous {
			t.Fatalf("progress decreased from %d to %d", previous, report.CurrentBytes)
		}
		previous = report.CurrentBytes
	}
}

func TestPullProgressUnknownTotalIsIndeterminate(t *testing.T) {
	body := `{"status":"Downloading","id":"sha256:aaa","progressDetail":{"current":12}}` + "\n"
	var got ports.StartupReport
	plan := &ports.StartupPublisher{Report: func(report ports.StartupReport) { got = report }}
	plan.SetPhases(domain.ImagePullPhases())
	if err := readPullProgress(context.Background(), pullStream{strings.NewReader(body)}, plan); err != nil {
		t.Fatal(err)
	}
	if got.CurrentBytes != 12 || got.TotalBytes != 0 {
		t.Fatalf("progress = %+v", got)
	}
}

func TestPullFailureClassificationDropsStreamText(t *testing.T) {
	secret := "basic-auth-SECRET"
	cases := []struct {
		body string
		code domain.PullFailureCode
	}{
		{`{"errorDetail":{"code":401,"message":"unauthorized: ` + secret + `"},"error":"unauthorized"}` + "\n", domain.PullAuthRequired},
		{`{"errorDetail":{"message":"manifest unknown: ` + secret + `"}}` + "\n", domain.PullImageNotFound},
		{`{"error":"dial tcp: lookup registry.example: no such host ` + secret + `"}` + "\n", domain.PullRegistryUnreachable},
	}
	for _, tc := range cases {
		plan := &ports.StartupPublisher{Report: func(ports.StartupReport) {}}
		plan.SetPhases(domain.ImagePullPhases())
		err := readPullProgress(context.Background(), pullStream{strings.NewReader(tc.body)}, plan)
		var failure *domain.StartupFailure
		if !errors.As(err, &failure) || failure.Code != tc.code {
			t.Fatalf("body %s => %v", tc.body, err)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "registry.example") {
			t.Fatalf("error retained stream text: %s", err.Error())
		}
	}
}

func TestPullProgressIsThrottled(t *testing.T) {
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, `{"status":"Downloading","id":"sha256:aaa","progressDetail":{"current":`+itoa(i*10)+`,"total":1000}}`)
	}
	body := strings.Join(lines, "\n") + "\n"
	current := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	previous := pullNow
	pullNow = func() time.Time { return current }
	t.Cleanup(func() { pullNow = previous })
	var count int
	plan := &ports.StartupPublisher{Report: func(ports.StartupReport) { count++ }}
	plan.SetPhases(domain.ImagePullPhases())
	done := make(chan error, 1)
	go func() {
		done <- readPullProgress(context.Background(), pullStream{strings.NewReader(body)}, plan)
	}()
	// The reader and the clock share one timestamp, so every line is inside
	// the emit interval after the first. Advance once before the last lines
	// by letting the goroutine start, then the clock stays frozen, so only
	// the first sample and the forced final sample are published.
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("emitted %d progress updates, want 2", count)
	}
}

func TestResolveImageReportsPullPhasesAndClassifiesAPIErrors(t *testing.T) {
	engine := newFakeEngine()
	engine.missing = true
	engine.pullBody = pullStream{strings.NewReader(
		`{"status":"Downloading","id":"sha256:aaa","progressDetail":{"current":312000000,"total":540000000}}` + "\n",
	)}
	provider := testProvider(t, engine)
	var phases []domain.StartupPhase
	var last ports.StartupReport
	plan := &ports.StartupPublisher{Report: func(report ports.StartupReport) {
		phases = append([]domain.StartupPhase(nil), report.Phases...)
		last = report
	}}
	reference := "ghcr.io/orlojhq/meridian-capsule-codex:v0.3.0"
	if _, err := provider.resolveImage(context.Background(), reference, plan); err != nil {
		t.Fatal(err)
	}
	if last.Phase != domain.PhasePullingImage || last.CurrentBytes != 312000000 || last.TotalBytes != 540000000 {
		t.Fatalf("last report = %+v", last)
	}
	if len(phases) != 4 || phases[0] != domain.PhasePullingImage {
		t.Fatalf("phases = %#v", phases)
	}

	engine = newFakeEngine()
	engine.missing = true
	engine.pullErr = errors.New("Get https://ghcr.io/v2/: dial tcp: no such host token-SECRET")
	provider = testProvider(t, engine)
	_, err := provider.resolveImage(context.Background(), reference, nil)
	var failure *domain.StartupFailure
	if !errors.As(err, &failure) || failure.Code != domain.PullRegistryUnreachable {
		t.Fatalf("pull error = %v", err)
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "ghcr.io") {
		t.Fatalf("resolve error retained registry text: %s", err.Error())
	}

	engine = newFakeEngine()
	provider = testProvider(t, engine)
	var local []domain.StartupPhase
	plan = &ports.StartupPublisher{Report: func(report ports.StartupReport) {
		local = append([]domain.StartupPhase(nil), report.Phases...)
	}}
	if _, err := provider.resolveImage(context.Background(), "ghcr.io/orlojhq/meridian-capsule-codex:v0.3.0", plan); err != nil {
		t.Fatal(err)
	}
	if len(engine.pulled) != 0 || !plan.HasPhases() {
		t.Fatalf("present image pulled=%#v hasPhases=%v", engine.pulled, plan.HasPhases())
	}
	plan.Progress(domain.PhaseCreatingCapsule, 0, 0)
	if len(local) != 3 || local[0] != domain.PhaseCreatingCapsule {
		t.Fatalf("present image phases = %#v", local)
	}
}

func mustProgress(t *testing.T, report ports.StartupReport) domain.StartupProgress {
	t.Helper()
	progress, err := domain.NewStartupProgress(
		"capsule-1", report.Phase, report.Phases, report.CurrentBytes, report.TotalBytes,
		time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	return progress
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [8]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}
