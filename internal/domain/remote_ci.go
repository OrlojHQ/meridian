package domain

import (
	"fmt"
	"time"
	"unicode"
)

// Remote CI (ADR 0053). A watch is one commit Meridian pushed to a pull
// request. The station polls GitHub for check runs and commit statuses.
// A later webhook or GitHub App can record the same observation without
// polling. The row stores names, conclusions, links, and an encrypted tail.
// It never stores a token.

const (
	RemoteCIDetailBytes = 4 << 10
	RemoteCIMaxChecks   = 100
	RemoteCIQuietPolls  = 8
	maxRemoteCIName     = 128
	maxRemoteCIURL      = 2048
)

type RemoteCIWatchID string

// RemoteCIState is what the operator is told about GitHub CI for one commit.
type RemoteCIState string

const (
	RemoteCIWaiting    RemoteCIState = "waiting"
	RemoteCIPassed     RemoteCIState = "passed"
	RemoteCIFailing    RemoteCIState = "failing"
	RemoteCIReconnect  RemoteCIState = "reconnect"
	RemoteCIConnect    RemoteCIState = "connect"
	RemoteCIUnreadable RemoteCIState = "unreadable"
	RemoteCIAbsent     RemoteCIState = "absent"
)

func (s RemoteCIState) Valid() bool {
	switch s {
	case RemoteCIWaiting, RemoteCIPassed, RemoteCIFailing, RemoteCIReconnect,
		RemoteCIConnect, RemoteCIUnreadable, RemoteCIAbsent:
		return true
	default:
		return false
	}
}

// Polling reports whether the station should keep asking GitHub. A pass, a
// failure, and a commit with no CI stay put. A missing login or a refusal
// for one repository is retried slowly, so connecting or approving SSO is
// picked up without a webhook.
func (s RemoteCIState) Polling() bool {
	switch s {
	case RemoteCIWaiting, RemoteCIConnect, RemoteCIReconnect, RemoteCIUnreadable:
		return true
	default:
		return false
	}
}

// RemoteCICheck is one GitHub check run or commit status. Detail is the
// ciphertext of a short redacted tail.
type RemoteCICheck struct {
	Position         int
	Name             string
	Source           string
	Conclusion       string
	URL              string
	OutputTruncated  bool
	OutputNonce      []byte
	OutputCiphertext []byte
}

func (c RemoteCICheck) Validate() error {
	if c.Name == "" || len(c.Name) > maxRemoteCIName || len(c.URL) > maxRemoteCIURL ||
		(c.Source != "check_run" && c.Source != "status") || c.Conclusion == "" ||
		len(c.Conclusion) > 32 {
		return fmt.Errorf("%w: invalid remote CI check", ErrInvalid)
	}
	for _, r := range c.Name {
		if r == '\n' || r == '\r' || unicode.IsControl(r) {
			return fmt.Errorf("%w: invalid remote CI check name", ErrInvalid)
		}
	}
	return nil
}

// RemoteCIWatch is the CI reading for one pushed commit.
type RemoteCIWatch struct {
	ID              RemoteCIWatchID
	CapsuleID       CapsuleID
	ProjectID       ProjectID
	OwnerUserID     UserID
	DeliveryID      DeliveryID
	RepositoryOwner string
	RepositoryName  string
	PullNumber      int64
	PullURL         string
	GitHead         string
	GitTree         string
	State           RemoteCIState
	Epoch           int64
	Attempt         int
	Counted         bool
	Passed          int
	Failed          int
	Pending         int
	PollCount       int
	NextPollAt      time.Time
	ReworkSent      bool
	PushReview      bool
	Superseded      bool
	ErrorCode       string
	ResourceVersion ResourceVersion
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinishedAt      time.Time
}

func (w RemoteCIWatch) Validate() error {
	if w.ID == "" || w.CapsuleID == "" || w.ProjectID == "" || !w.State.Valid() ||
		w.PullNumber <= 0 || w.GitHead == "" || len(w.GitHead) > 128 || len(w.GitTree) > 128 ||
		w.RepositoryOwner == "" || w.RepositoryName == "" ||
		w.Attempt < 0 || w.Attempt > MaxReworkAttemptSetting ||
		w.Passed < 0 || w.Failed < 0 || w.Pending < 0 || w.PollCount < 0 ||
		len(w.ErrorCode) > 64 || len(w.PullURL) > maxRemoteCIURL ||
		w.ResourceVersion <= 0 || w.CreatedAt.IsZero() || w.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: invalid remote CI watch", ErrInvalid)
	}
	return nil
}

// RemoteCIBackoff is the wait before the next poll. It starts at five
// seconds and doubles until it reaches one minute.
func RemoteCIBackoff(polls int) time.Duration {
	delay := 5 * time.Second
	for i := 0; i < polls && delay < time.Minute; i++ {
		delay *= 2
	}
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}

// RemoteCILabel is the plain-language line for a watch. shown is the
// one-based attempt. A failing result names the review a push still needs.
func RemoteCILabel(state RemoteCIState, shown, total int, reworkSent, pushReview bool, code string) string {
	switch state {
	case RemoteCIWaiting:
		return "Waiting on CI"
	case RemoteCIPassed:
		return "CI passed"
	case RemoteCIFailing:
		if total < 1 {
			total = 1
		}
		if shown < 1 {
			shown = 1
		}
		if shown > total {
			shown = total
		}
		label := fmt.Sprintf("CI failing (%d/%d)", shown, total)
		if reworkSent && shown < total {
			label = fmt.Sprintf("%s. Reworking · attempt %d of %d", label, shown+1, total)
		}
		if pushReview {
			label += ". Review the fix before it can be pushed."
		}
		return label
	case RemoteCIReconnect:
		return "Can't read CI: Reconnect GitHub"
	case RemoteCIConnect:
		return "Connect GitHub to read CI"
	case RemoteCIUnreadable:
		if code == "secret" {
			return "Can't read CI with the Project's GitHub token."
		}
		return "Can't read CI for this repository"
	case RemoteCIAbsent:
		return "No CI reported for this commit"
	default:
		return ""
	}
}
