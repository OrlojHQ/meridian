package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// FormatStartup renders a phase label and, while an image is pulling, numeric
// progress. Unknown phases return an empty string.
func FormatStartup(phase StartupPhase, current, total int64) string {
	label := phase.Label()
	if label == "" {
		return ""
	}
	if phase != PhasePullingImage {
		return label
	}
	if total > 0 {
		if current < 0 {
			current = 0
		}
		if current > total {
			current = total
		}
		percent := current * 100 / total
		return fmt.Sprintf("%s · %s / %s · %d%%", label, FormatBytes(current), FormatBytes(total), percent)
	}
	if current > 0 {
		return label + " · " + FormatBytes(current)
	}
	return label
}

// FormatBytes renders a decimal byte count as B, KB, or MB.
func FormatBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	const mb = 1_000_000
	const kb = 1_000
	switch {
	case n >= 10*mb:
		return fmt.Sprintf("%d MB", n/mb)
	case n >= mb:
		tenths := n * 10 / mb
		return fmt.Sprintf("%d.%d MB", tenths/10, tenths%10)
	case n >= kb:
		return fmt.Sprintf("%d KB", n/kb)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// Startup phases are a fixed vocabulary. Registry and runtime text never
// becomes a phase name.
type StartupPhase string

const (
	PhasePullingImage    StartupPhase = "pulling_image"
	PhaseCreatingCapsule StartupPhase = "creating_capsule"
	PhaseStartingHarness StartupPhase = "starting_harness"
	PhaseConnecting      StartupPhase = "connecting"
)

// MaxPullBytes is the largest image-pull byte count Meridian will store or
// return. Larger values are rejected as hostile input.
const MaxPullBytes int64 = 1 << 40

var startupRank = map[StartupPhase]int{
	PhasePullingImage:    0,
	PhaseCreatingCapsule: 1,
	PhaseStartingHarness: 2,
	PhaseConnecting:      3,
}

func (p StartupPhase) Valid() bool {
	_, ok := startupRank[p]
	return ok
}

func (p StartupPhase) Label() string {
	switch p {
	case PhasePullingImage:
		return "Pulling image"
	case PhaseCreatingCapsule:
		return "Creating Capsule"
	case PhaseStartingHarness:
		return "Starting harness"
	case PhaseConnecting:
		return "Connecting"
	default:
		return ""
	}
}

// ImagePullPhases is the ordered plan when a provider has to pull an image.
func ImagePullPhases() []StartupPhase {
	return []StartupPhase{
		PhasePullingImage, PhaseCreatingCapsule, PhaseStartingHarness, PhaseConnecting,
	}
}

// LocalImagePhases is the ordered plan when the image is already present.
func LocalImagePhases() []StartupPhase {
	return []StartupPhase{PhaseCreatingCapsule, PhaseStartingHarness, PhaseConnecting}
}

// IncludePhase inserts phase into phases in canonical order.
func IncludePhase(phases []StartupPhase, phase StartupPhase) []StartupPhase {
	if !phase.Valid() {
		return append([]StartupPhase(nil), phases...)
	}
	for _, existing := range phases {
		if existing == phase {
			return append([]StartupPhase(nil), phases...)
		}
	}
	next := append(append([]StartupPhase(nil), phases...), phase)
	ordered := make([]StartupPhase, 0, len(startupRank))
	for _, candidate := range []StartupPhase{
		PhasePullingImage, PhaseCreatingCapsule, PhaseStartingHarness, PhaseConnecting,
	} {
		for _, item := range next {
			if item == candidate {
				ordered = append(ordered, item)
				break
			}
		}
	}
	return ordered
}

// ImagePullProgress is numeric pull progress. TotalBytes is zero when the
// registry did not report a total.
type ImagePullProgress struct {
	CurrentBytes int64 `json:"currentBytes"`
	TotalBytes   int64 `json:"totalBytes,omitempty"`
}

// StartupProgress is the public, content-free view of Capsule startup.
type StartupProgress struct {
	CapsuleID CapsuleID          `json:"-"`
	Phase     StartupPhase       `json:"phase"`
	Phases    []StartupPhase     `json:"phases"`
	Pull      *ImagePullProgress `json:"pull,omitempty"`
	UpdatedAt time.Time          `json:"updatedAt"`
}

// PullFailureCode classifies an image pull without retaining registry text.
type PullFailureCode string

const (
	PullRegistryUnreachable PullFailureCode = "registry_unreachable"
	PullAuthRequired        PullFailureCode = "auth_required"
	PullImageNotFound       PullFailureCode = "image_not_found"
)

func (c PullFailureCode) Valid() bool {
	switch c {
	case "", PullRegistryUnreachable, PullAuthRequired, PullImageNotFound:
		return true
	default:
		return false
	}
}

func (c PullFailureCode) Message() string {
	switch c {
	case PullRegistryUnreachable:
		return "The image registry could not be reached."
	case PullAuthRequired:
		return "The image registry requires authentication."
	case PullImageNotFound:
		return "The Capsule image was not found."
	default:
		return "The Capsule image could not be pulled."
	}
}

// StartupFailure is a pull failure whose Error text is a fixed sentence.
// The registry's own message is never included.
type StartupFailure struct {
	Code PullFailureCode
}

func (e *StartupFailure) Error() string {
	if e == nil {
		return PullFailureCode("").Message()
	}
	return e.Code.Message()
}

// NewStartupProgress validates and bounds a startup report. current is clamped
// to total when a total is known. Byte counts outside MaxPullBytes are rejected.
func NewStartupProgress(
	id CapsuleID,
	phase StartupPhase,
	phases []StartupPhase,
	current, total int64,
	at time.Time,
) (StartupProgress, error) {
	if id == "" || !phase.Valid() || at.IsZero() {
		return StartupProgress{}, fmt.Errorf("%w: invalid startup progress", ErrInvalid)
	}
	if len(phases) == 0 || len(phases) > len(startupRank) {
		return StartupProgress{}, fmt.Errorf("%w: invalid startup phases", ErrInvalid)
	}
	seen := make(map[StartupPhase]bool, len(phases))
	last := -1
	normalized := make([]StartupPhase, 0, len(phases))
	for _, item := range phases {
		rank, ok := startupRank[item]
		if !ok || seen[item] || rank < last {
			return StartupProgress{}, fmt.Errorf("%w: invalid startup phases", ErrInvalid)
		}
		seen[item] = true
		last = rank
		normalized = append(normalized, item)
	}
	if !seen[phase] {
		return StartupProgress{}, fmt.Errorf("%w: startup phase is not in the reported set", ErrInvalid)
	}
	if current < 0 || total < 0 || current > MaxPullBytes || total > MaxPullBytes {
		return StartupProgress{}, fmt.Errorf("%w: startup progress is out of bounds", ErrInvalid)
	}
	if total > 0 && current > total {
		current = total
	}
	progress := StartupProgress{
		CapsuleID: id,
		Phase:     phase,
		Phases:    normalized,
		UpdatedAt: at.UTC(),
	}
	if phase == PhasePullingImage {
		pull := &ImagePullProgress{CurrentBytes: current}
		if total > 0 {
			pull.TotalBytes = total
		}
		progress.Pull = pull
	}
	return progress, nil
}

func (p StartupProgress) Validate() error {
	current, total := int64(0), int64(0)
	if p.Pull != nil {
		current, total = p.Pull.CurrentBytes, p.Pull.TotalBytes
	}
	normalized, err := NewStartupProgress(p.CapsuleID, p.Phase, p.Phases, current, total, p.UpdatedAt)
	if err != nil {
		return err
	}
	if p.Phase != PhasePullingImage && p.Pull != nil {
		return fmt.Errorf("%w: pull progress is only valid while pulling an image", ErrInvalid)
	}
	if p.Phase == PhasePullingImage {
		if p.Pull == nil || p.Pull.CurrentBytes != normalized.Pull.CurrentBytes ||
			p.Pull.TotalBytes != normalized.Pull.TotalBytes {
			return fmt.Errorf("%w: pull progress does not match its bounds", ErrInvalid)
		}
	}
	return nil
}

// StartupEventData is the only JSON persisted for a startup event: phase names
// and byte counts.
func StartupEventData(progress StartupProgress) ([]byte, error) {
	if err := progress.Validate(); err != nil {
		return nil, err
	}
	payload := struct {
		Phase        StartupPhase   `json:"phase"`
		Phases       []StartupPhase `json:"phases"`
		CurrentBytes int64          `json:"currentBytes,omitempty"`
		TotalBytes   int64          `json:"totalBytes,omitempty"`
	}{
		Phase:  progress.Phase,
		Phases: progress.Phases,
	}
	if progress.Pull != nil {
		payload.CurrentBytes = progress.Pull.CurrentBytes
		payload.TotalBytes = progress.Pull.TotalBytes
	}
	return json.Marshal(payload)
}

// ClassifyPullText maps a bounded prefix of untrusted pull text onto a failure
// code. The input is not retained.
func ClassifyPullText(text string) PullFailureCode {
	if len(text) > 512 {
		text = text[:512]
	}
	text = strings.ToLower(text)
	switch {
	case strings.Contains(text, "unauthorized"),
		strings.Contains(text, "authentication required"),
		strings.Contains(text, "auth required"),
		strings.Contains(text, "pull access denied"),
		strings.Contains(text, "access denied"),
		strings.Contains(text, "insufficient_scope"),
		strings.Contains(text, "docker login"),
		strings.Contains(text, "no basic auth"):
		return PullAuthRequired
	case strings.Contains(text, "manifest unknown"),
		strings.Contains(text, "not found"),
		strings.Contains(text, "name unknown"),
		strings.Contains(text, "repository does not exist"):
		return PullImageNotFound
	case strings.Contains(text, "no such host"),
		strings.Contains(text, "connection refused"),
		strings.Contains(text, "connection reset"),
		strings.Contains(text, "i/o timeout"),
		strings.Contains(text, "timeout"),
		strings.Contains(text, "network is unreachable"),
		strings.Contains(text, "no route to host"),
		strings.Contains(text, "dial tcp"),
		strings.Contains(text, "temporary failure"),
		strings.Contains(text, "tls handshake"):
		return PullRegistryUnreachable
	default:
		return ""
	}
}

// ClassifyImagePullWait maps a Kubernetes container wait reason. Only the
// fixed image-pull reasons are classified; the message is not retained.
func ClassifyImagePullWait(reason, message string) *StartupFailure {
	switch reason {
	case "ErrImagePull", "ImagePullBackOff":
		return &StartupFailure{Code: ClassifyPullText(message)}
	default:
		return nil
	}
}
