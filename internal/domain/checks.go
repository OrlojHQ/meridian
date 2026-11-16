package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Project checks (ADR 0052). A check is an argv the Capsule runs when an
// agent finishes. Checks are a quality gate for the operator, not a security
// boundary: the agent can change the commands it is asked to satisfy.

const (
	MaxProjectChecks           = 16
	MaxCheckArgv               = 128
	MaxCheckArgBytes           = 4096
	DefaultCheckTimeoutSeconds = 10 * 60
	MaxCheckTimeoutSeconds     = 60 * 60
	MaxReworkAttemptSetting    = 10
	CheckOutputBytes           = 64 << 10
	CheckReworkLines           = 200
	maxCheckNameBytes          = 64
)

type CheckRunID string

// CheckTrigger is why a local check suite started. Remote CI is a separate
// watch in the same summary (ADR 0053), not one of these triggers.
type CheckTrigger string

const (
	CheckTriggerTurnIdle CheckTrigger = "turn_idle"
	CheckTriggerDelivery CheckTrigger = "delivery_proposed"
	CheckTriggerRunExit  CheckTrigger = "run_exit"
	CheckTriggerManual   CheckTrigger = "manual"
)

func (t CheckTrigger) Valid() bool {
	switch t {
	case CheckTriggerTurnIdle, CheckTriggerDelivery, CheckTriggerRunExit, CheckTriggerManual:
		return true
	default:
		return false
	}
}

// CheckRunState is the durable state of one suite execution.
type CheckRunState string

const (
	CheckQueued    CheckRunState = "queued"
	CheckRunning   CheckRunState = "running"
	CheckPassed    CheckRunState = "passed"
	CheckFailed    CheckRunState = "failed"
	CheckError     CheckRunState = "error"
	CheckCancelled CheckRunState = "cancelled"
	CheckStale     CheckRunState = "stale"
)

func (s CheckRunState) Valid() bool {
	switch s {
	case CheckQueued, CheckRunning, CheckPassed, CheckFailed, CheckError, CheckCancelled, CheckStale:
		return true
	default:
		return false
	}
}

func (s CheckRunState) Terminal() bool {
	switch s {
	case CheckPassed, CheckFailed, CheckError, CheckCancelled, CheckStale:
		return true
	default:
		return false
	}
}

// CheckPhase is the content-free summary a Capsule shows. It is derived; it
// is not a Capsule lifecycle state.
type CheckPhase string

const (
	CheckPhaseQueued    CheckPhase = "queued"
	CheckPhaseRunning   CheckPhase = "running"
	CheckPhaseReworking CheckPhase = "reworking"
	CheckPhasePassed    CheckPhase = "passed"
	CheckPhaseFailing   CheckPhase = "failing"
	CheckPhaseStale     CheckPhase = "stale"
	CheckPhaseError     CheckPhase = "error"
)

type CheckCommand struct {
	Name           string
	Argv           []string
	TimeoutSeconds int
}

func (c CheckCommand) Validate() error {
	if !validCheckName(c.Name) {
		return fmt.Errorf("%w: check name must be a short label", ErrInvalid)
	}
	if len(c.Argv) == 0 || len(c.Argv) > MaxCheckArgv {
		return fmt.Errorf("%w: check command must be a bounded argv", ErrInvalid)
	}
	for _, argument := range c.Argv {
		if argument == "" || len(argument) > MaxCheckArgBytes || strings.ContainsRune(argument, 0) {
			return fmt.Errorf("%w: check arguments must be non-empty and bounded", ErrInvalid)
		}
	}
	if c.TimeoutSeconds < 0 || c.TimeoutSeconds > MaxCheckTimeoutSeconds {
		return fmt.Errorf("%w: check timeout must be between 1 second and 60 minutes", ErrInvalid)
	}
	return nil
}

func (c CheckCommand) Timeout() time.Duration {
	seconds := c.TimeoutSeconds
	if seconds == 0 {
		seconds = DefaultCheckTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

func validCheckName(name string) bool {
	if name == "" || len(name) > maxCheckNameBytes {
		return false
	}
	for index, r := range name {
		switch {
		case r > unicode.MaxASCII || unicode.IsControl(r) || r == ' ':
			return false
		case index == 0 && !unicode.IsLetter(r) && !unicode.IsDigit(r):
			return false
		case !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.':
			return false
		}
	}
	return true
}

// ProjectChecks is the owner's required commands for every Capsule in the
// Project. An empty list means the Project behaves as if checks did not exist.
// ResourceVersion is the Project row's version, used for optimistic writes.
type ProjectChecks struct {
	ProjectID         ProjectID
	Checks            []CheckCommand
	AutoRework        bool
	MaxReworkAttempts int
	ResourceVersion   ResourceVersion
	UpdatedAt         time.Time
}

func (p ProjectChecks) Validate() error {
	if len(p.Checks) > MaxProjectChecks {
		return fmt.Errorf("%w: a Project can have at most %d checks", ErrInvalid, MaxProjectChecks)
	}
	if p.MaxReworkAttempts < 0 || p.MaxReworkAttempts > MaxReworkAttemptSetting {
		return fmt.Errorf("%w: rework attempts must be from 0 to %d", ErrInvalid, MaxReworkAttemptSetting)
	}
	seen := make(map[string]struct{}, len(p.Checks))
	for _, check := range p.Checks {
		if err := check.Validate(); err != nil {
			return err
		}
		if _, ok := seen[check.Name]; ok {
			return fmt.Errorf("%w: check names must be unique", ErrInvalid)
		}
		seen[check.Name] = struct{}{}
	}
	return nil
}

func (p ProjectChecks) Enabled() bool {
	return len(p.Checks) > 0
}

// CheckBudget is how many times a suite runs before auto-rework stops.
// Zero configured attempts still runs the suite once and does not send the
// agent back. Any larger setting is the total number of tries, including the
// first.
func CheckBudget(maxReworkAttempts int) int {
	if maxReworkAttempts <= 0 {
		return 1
	}
	return maxReworkAttempts
}

// ShouldAutoRework reports whether a failed suite should send the agent back.
// attempt is zero-based: the first run is 0. Native exits and manual runs
// never resume the agent. Subagent Capsules never auto-rework.
func ShouldAutoRework(auto bool, stopped bool, trigger CheckTrigger, role CapsuleRole, attempt, maxReworkAttempts int) bool {
	if !auto || stopped || role == CapsuleSub {
		return false
	}
	switch trigger {
	case CheckTriggerTurnIdle, CheckTriggerDelivery:
	default:
		return false
	}
	return attempt+1 < CheckBudget(maxReworkAttempts)
}

// CheckRun is one execution of the Project's suite inside one Capsule.
// Attempt is zero-based within an operator epoch. Output lives on the child
// results, encrypted, and is never copied onto this row.
type CheckRun struct {
	ID                CheckRunID
	CapsuleID         CapsuleID
	ProjectID         ProjectID
	OwnerUserID       UserID
	ThreadID          ThreadID
	Attempt           int
	Trigger           CheckTrigger
	State             CheckRunState
	GitHead           string
	GitTree           string
	StartedAt         time.Time
	FinishedAt        time.Time
	NotBefore         time.Time
	Epoch             int64
	BasisMessageCount int64
	BasisRunID        RunID
	ReworkAllowed     bool
	ReworkSent        bool
	RestartRetried    bool
	Passed            int
	Failed            int
	ErrorCode         string
	ResourceVersion   ResourceVersion
	CreatedAt         time.Time
}

func (r CheckRun) Validate() error {
	if r.ID == "" || r.CapsuleID == "" || r.ProjectID == "" ||
		!r.Trigger.Valid() || !r.State.Valid() ||
		r.Attempt < 0 || r.Attempt > MaxReworkAttemptSetting ||
		r.NotBefore.IsZero() || r.CreatedAt.IsZero() || r.ResourceVersion <= 0 ||
		len(r.GitHead) > 128 || len(r.GitTree) > 128 || len(r.ErrorCode) > 64 {
		return fmt.Errorf("%w: invalid check run", ErrInvalid)
	}
	if r.State == CheckRunning && r.StartedAt.IsZero() {
		return fmt.Errorf("%w: a running check has no start time", ErrInvalid)
	}
	if r.State.Terminal() && r.FinishedAt.IsZero() {
		return fmt.Errorf("%w: a finished check has no finish time", ErrInvalid)
	}
	return nil
}

// CheckResult is one command inside a run. The ciphertext is the redacted
// output tail. Plaintext never crosses the store.
type CheckResult struct {
	Position         int
	Name             string
	Argv             []string
	ExitCode         int
	HasExit          bool
	Duration         time.Duration
	TimedOut         bool
	OutputTruncated  bool
	OutputNonce      []byte
	OutputCiphertext []byte
}

// CheckControl is the per-Capsule rework generation. Epoch increments when
// the operator sends a message, pauses, cancels, or rewinds, which drops any
// in-flight automatic retry. Stopped is the explicit "stop auto-fix" control.
type CheckControl struct {
	CapsuleID CapsuleID
	Epoch     int64
	Stopped   bool
}

// ChecksSummary is the content-free Capsule field. It carries no command
// text and no output.
type ChecksSummary struct {
	Phase            CheckPhase
	Attempt          int
	MaxAttempts      int
	Tree             string
	Head             string
	FinishedAt       time.Time
	ReworkSent       bool
	AutoRework       bool
	Stopped          bool
	ProposalWithheld bool
	Passed           int
	Failed           int
	RunID            CheckRunID
}

// CheckPhaseOf derives the summary phase from the latest relevant run and the
// current Project settings. attempt is zero-based.
func CheckPhaseOf(state CheckRunState, reworkSent bool, auto bool, stopped bool, trigger CheckTrigger, role CapsuleRole, attempt, maxReworkAttempts int) CheckPhase {
	switch state {
	case CheckQueued:
		return CheckPhaseQueued
	case CheckRunning:
		return CheckPhaseRunning
	case CheckPassed:
		return CheckPhasePassed
	case CheckStale:
		return CheckPhaseStale
	case CheckError:
		return CheckPhaseError
	case CheckFailed:
		if ShouldAutoRework(auto, stopped, trigger, role, attempt, maxReworkAttempts) && reworkSent {
			return CheckPhaseReworking
		}
		return CheckPhaseFailing
	default:
		return ""
	}
}
