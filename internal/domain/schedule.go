package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	// The time zone database is embedded so a schedule's zone resolves the
	// same way on a host without /usr/share/zoneinfo.
	_ "time/tzdata"
)

// Scheduled Project Threads (ADR 0057). A schedule is a clock, a fixed
// prompt, and limits. Each fire starts one fresh Project Thread.

const (
	ScheduleMinSpacing        = 15 * time.Minute
	ScheduleMinRun            = 5 * time.Minute
	ScheduleMaxRun            = 24 * time.Hour
	ScheduleDefaultRun        = 2 * time.Hour
	ScheduleMaxCatchUp        = 24 * time.Hour
	ScheduleDefaultCatchUp    = time.Hour
	ScheduleDefaultKept       = 5
	ScheduleMaxKept           = 20
	ScheduleMaxPromptBytes    = 32 << 10
	ScheduleStartFailureLimit = 3
	// ScheduleModelWait bounds how long a fire waits for the harness to
	// report its models, and then to confirm a switch.
	ScheduleModelWait = 2 * time.Minute
	// ScheduleIdleSettle is how long a Thread must stay idle with nothing
	// waiting before its outcome is decided, so Project checks that start
	// after a turn ends are seen first.
	ScheduleIdleSettle = 20 * time.Second
	// ScheduleMissedRecords caps the missed rows written for one gap.
	ScheduleMissedRecords = 24
	maxCadenceBytes       = 128
	maxTimeZoneBytes      = 64
)

var scheduleNamePattern = regexp.MustCompile(`^[a-z0-9-]{1,48}$`)

type ScheduleID string
type ScheduleFireID string

// ValidScheduleName reports whether name is a schedule name.
func ValidScheduleName(name string) bool {
	return scheduleNamePattern.MatchString(name)
}

// Schedule is one operator-authored clock on a Project. The prompt, model,
// and effort are sealed together in Content*; they never appear here in
// plaintext.
type Schedule struct {
	ID                ScheduleID
	ProjectID         ProjectID
	OwnerUserID       UserID
	Name              string
	Harness           string
	Cadence           string
	TimeZone          string
	MaxRun            time.Duration
	CatchUp           time.Duration
	MaxKept           int
	Enabled           bool
	DisabledReason    string
	StartFailures     int
	NextFireAt        time.Time
	LastFireAt        time.Time
	ContentNonce      []byte
	ContentCiphertext []byte
	ResourceVersion   ResourceVersion
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (s Schedule) Validate() error {
	if s.ID == "" || s.ProjectID == "" || !ValidScheduleName(s.Name) || !ValidHarnessName(s.Harness) ||
		s.Cadence == "" || len(s.Cadence) > maxCadenceBytes ||
		s.TimeZone == "" || len(s.TimeZone) > maxTimeZoneBytes ||
		s.MaxRun < ScheduleMinRun || s.MaxRun > ScheduleMaxRun || s.MaxRun%time.Second != 0 ||
		s.CatchUp < 0 || s.CatchUp > ScheduleMaxCatchUp || s.CatchUp%time.Second != 0 ||
		s.MaxKept < 1 || s.MaxKept > ScheduleMaxKept || len(s.DisabledReason) > 64 ||
		s.StartFailures < 0 || len(s.ContentNonce) == 0 || len(s.ContentCiphertext) == 0 ||
		s.ResourceVersion <= 0 || s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: invalid schedule", ErrInvalid)
	}
	return nil
}

// ScheduleFireTrigger says what started a fire.
type ScheduleFireTrigger string

const (
	ScheduleTriggerClock   ScheduleFireTrigger = "clock"
	ScheduleTriggerCatchUp ScheduleFireTrigger = "catch_up"
	ScheduleTriggerManual  ScheduleFireTrigger = "manual"
)

func (t ScheduleFireTrigger) Valid() bool {
	return t == ScheduleTriggerClock || t == ScheduleTriggerCatchUp || t == ScheduleTriggerManual
}

// ScheduleFireState is where a fire is. starting waits for the Thread's
// session, selecting applies the model and effort, running waits for the
// outcome, and cleaning ends the session and deletes an empty Capsule.
type ScheduleFireState string

const (
	ScheduleFireSkipped   ScheduleFireState = "skipped"
	ScheduleFireMissed    ScheduleFireState = "missed"
	ScheduleFireStarting  ScheduleFireState = "starting"
	ScheduleFireSelecting ScheduleFireState = "selecting"
	ScheduleFireRunning   ScheduleFireState = "running"
	ScheduleFireCleaning  ScheduleFireState = "cleaning"
	ScheduleFireFinished  ScheduleFireState = "finished"
)

func (s ScheduleFireState) Valid() bool {
	switch s {
	case ScheduleFireSkipped, ScheduleFireMissed, ScheduleFireStarting, ScheduleFireSelecting,
		ScheduleFireRunning, ScheduleFireCleaning, ScheduleFireFinished:
		return true
	}
	return false
}

// Active reports whether the fire still holds a run in progress.
func (s ScheduleFireState) Active() bool {
	switch s {
	case ScheduleFireStarting, ScheduleFireSelecting, ScheduleFireRunning, ScheduleFireCleaning:
		return true
	}
	return false
}

// ScheduleOutcome is the content-free result of a fire.
type ScheduleOutcome string

const (
	ScheduleOutcomeNone      ScheduleOutcome = ""
	ScheduleOutcomeProposed  ScheduleOutcome = "proposed"
	ScheduleOutcomeChanged   ScheduleOutcome = "changed"
	ScheduleOutcomeNoChanges ScheduleOutcome = "no_changes"
	ScheduleOutcomeWaiting   ScheduleOutcome = "waiting"
	ScheduleOutcomeTimedOut  ScheduleOutcome = "timed_out"
	ScheduleOutcomeFailed    ScheduleOutcome = "failed"
)

func (o ScheduleOutcome) Valid() bool {
	switch o {
	case ScheduleOutcomeNone, ScheduleOutcomeProposed, ScheduleOutcomeChanged, ScheduleOutcomeNoChanges,
		ScheduleOutcomeWaiting, ScheduleOutcomeTimedOut, ScheduleOutcomeFailed:
		return true
	}
	return false
}

// NeedsOperator reports whether the outcome raises attention.
func (o ScheduleOutcome) NeedsOperator() bool {
	switch o {
	case ScheduleOutcomeProposed, ScheduleOutcomeChanged, ScheduleOutcomeWaiting,
		ScheduleOutcomeTimedOut, ScheduleOutcomeFailed:
		return true
	}
	return false
}

// Skip and failure reasons. They are codes, never message text.
const (
	ScheduleReasonOverlap       = "overlap"
	ScheduleReasonCapacity      = "capacity"
	ScheduleReasonBacklog       = "backlog"
	ScheduleReasonOwner         = "owner_unavailable"
	ScheduleReasonProject       = "project_unavailable"
	ScheduleReasonMissed        = "missed"
	ScheduleReasonStartFailed   = "start_failed"
	ScheduleReasonModelMissing  = "model_unreported"
	ScheduleReasonModelNone     = "model_unsupported"
	ScheduleReasonModelAbsent   = "model_unavailable"
	ScheduleReasonModelRefused  = "model_refused"
	ScheduleReasonEffortNone    = "effort_unsupported"
	ScheduleReasonEffortAbsent  = "effort_unavailable"
	ScheduleReasonEffortRefused = "effort_refused"
	ScheduleReasonSessionEnded  = "session_ended"
	ScheduleReasonInspectFailed = "inspect_failed"
	// Disabled reasons.
	ScheduleDisabledStartFailures = "start_failures"
	ScheduleDisabledHarness       = "harness_not_allowed"
	ScheduleDisabledTransferred   = "transferred"
)

// ScheduleFire is one tick of a schedule's clock.
type ScheduleFire struct {
	ID              ScheduleFireID
	ScheduleID      ScheduleID
	ProjectID       ProjectID
	OwnerUserID     UserID
	Trigger         ScheduleFireTrigger
	ScheduledFor    time.Time
	State           ScheduleFireState
	Outcome         ScheduleOutcome
	Reason          string
	CapsuleID       CapsuleID
	ThreadID        ThreadID
	CapsuleDeleted  bool
	ModelRequested  bool
	EffortRequested bool
	SelectingSince  time.Time
	IdleSince       time.Time
	DeadlineAt      time.Time
	StartedAt       time.Time
	FinishedAt      time.Time
	ResourceVersion ResourceVersion
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (f ScheduleFire) Validate() error {
	if f.ID == "" || f.ScheduleID == "" || f.ProjectID == "" || !f.Trigger.Valid() ||
		f.ScheduledFor.IsZero() || !f.State.Valid() || !f.Outcome.Valid() || len(f.Reason) > 64 ||
		f.ResourceVersion <= 0 || f.CreatedAt.IsZero() || f.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: invalid schedule fire", ErrInvalid)
	}
	return nil
}

// Cadence is a parsed five-field cron expression in one time zone.
type Cadence struct {
	minutes  [60]bool
	hours    [24]bool
	days     [32]bool
	months   [13]bool
	weekdays [7]bool
	// anyDay and anyWeekday record a "*" field: cron matches a day when
	// either restricted field matches, and ignores an unrestricted one.
	anyDay     bool
	anyWeekday bool
	location   *time.Location
}

var cadenceMacros = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

// ParseCadence parses expression in the IANA zone named zone. Fields are
// minute, hour, day of month, month, and day of week (0 or 7 is Sunday).
// Each field takes "*", a number, a range "a-b", a step "*/n" or "a-b/n",
// or a comma list of those.
func ParseCadence(expression, zone string) (Cadence, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" || len(expression) > maxCadenceBytes {
		return Cadence{}, fmt.Errorf("%w: cadence must be a cron expression", ErrInvalid)
	}
	if zone == "" || len(zone) > maxTimeZoneBytes || strings.EqualFold(zone, "local") {
		return Cadence{}, fmt.Errorf("%w: time zone must be an IANA zone name", ErrInvalid)
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return Cadence{}, fmt.Errorf("%w: unknown time zone %q", ErrInvalid, zone)
	}
	if macro, ok := cadenceMacros[strings.ToLower(expression)]; ok {
		expression = macro
	}
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return Cadence{}, fmt.Errorf("%w: cadence needs five fields: minute hour day month weekday", ErrInvalid)
	}
	c := Cadence{location: location}
	if err := parseCadenceField(fields[0], 0, 59, c.minutes[:]); err != nil {
		return Cadence{}, fmt.Errorf("%w: minute: %v", ErrInvalid, err)
	}
	if err := parseCadenceField(fields[1], 0, 23, c.hours[:]); err != nil {
		return Cadence{}, fmt.Errorf("%w: hour: %v", ErrInvalid, err)
	}
	if err := parseCadenceField(fields[2], 1, 31, c.days[:]); err != nil {
		return Cadence{}, fmt.Errorf("%w: day of month: %v", ErrInvalid, err)
	}
	if err := parseCadenceField(fields[3], 1, 12, c.months[:]); err != nil {
		return Cadence{}, fmt.Errorf("%w: month: %v", ErrInvalid, err)
	}
	var weekdays [8]bool
	if err := parseCadenceField(fields[4], 0, 7, weekdays[:]); err != nil {
		return Cadence{}, fmt.Errorf("%w: day of week: %v", ErrInvalid, err)
	}
	copy(c.weekdays[:], weekdays[:7])
	c.weekdays[0] = c.weekdays[0] || weekdays[7]
	c.anyDay = fields[2] == "*"
	c.anyWeekday = fields[4] == "*"
	return c, nil
}

func parseCadenceField(field string, low, high int, set []bool) error {
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return fmt.Errorf("empty list item")
		}
		rangePart, stepPart, stepped := strings.Cut(part, "/")
		step := 1
		if stepped {
			value, err := strconv.Atoi(stepPart)
			if err != nil || value < 1 || value > high {
				return fmt.Errorf("invalid step %q", stepPart)
			}
			step = value
		}
		start, end := low, high
		switch {
		case rangePart == "*":
		case strings.Contains(rangePart, "-"):
			first, last, _ := strings.Cut(rangePart, "-")
			a, errA := strconv.Atoi(first)
			b, errB := strconv.Atoi(last)
			if errA != nil || errB != nil || a < low || b > high || a > b {
				return fmt.Errorf("invalid range %q", rangePart)
			}
			start, end = a, b
		default:
			value, err := strconv.Atoi(rangePart)
			if err != nil || value < low || value > high {
				return fmt.Errorf("invalid value %q", rangePart)
			}
			start = value
			if !stepped {
				end = value
			}
		}
		for value := start; value <= end; value += step {
			set[value] = true
		}
	}
	return nil
}

func (c Cadence) dayMatches(day time.Time) bool {
	if !c.months[int(day.Month())] {
		return false
	}
	dom := c.days[day.Day()]
	dow := c.weekdays[int(day.Weekday())]
	switch {
	case c.anyDay && c.anyWeekday:
		return true
	case c.anyDay:
		return dow
	case c.anyWeekday:
		return dom
	default:
		return dom || dow
	}
}

// cadenceHorizon bounds the search for the next fire. Five years covers
// every valid expression, including February 29.
const cadenceHorizon = 5 * 366

// Next returns the first fire strictly after after, or the zero time when
// the expression never matches (for example, February 30). Fires are wall
// clock times in the cadence's zone: a local time skipped by a daylight
// saving change does not fire, and a local time that happens twice fires
// once.
func (c Cadence) Next(after time.Time) time.Time {
	if c.location == nil {
		return time.Time{}
	}
	local := after.In(c.location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, c.location)
	for i := 0; i < cadenceHorizon; i++ {
		date := day.AddDate(0, 0, i)
		year, month, dom := date.Date()
		probe := time.Date(year, month, dom, 12, 0, 0, 0, c.location)
		if !c.dayMatches(probe) {
			continue
		}
		for hour := 0; hour < 24; hour++ {
			if !c.hours[hour] {
				continue
			}
			for minute := 0; minute < 60; minute++ {
				if !c.minutes[minute] {
					continue
				}
				candidate := time.Date(year, month, dom, hour, minute, 0, 0, c.location)
				if candidate.Hour() != hour || candidate.Minute() != minute || candidate.Day() != dom {
					continue
				}
				if candidate.After(after) {
					return candidate.UTC()
				}
			}
		}
	}
	return time.Time{}
}

// CheckSpacing returns an error when two fires within the next samples
// fires starting at from are closer than ScheduleMinSpacing, or when the
// expression never fires.
func (c Cadence) CheckSpacing(from time.Time, samples int) error {
	previous := c.Next(from)
	if previous.IsZero() {
		return fmt.Errorf("%w: cadence never fires", ErrInvalid)
	}
	for i := 1; i < samples; i++ {
		next := c.Next(previous)
		if next.IsZero() {
			return nil
		}
		if next.Sub(previous) < ScheduleMinSpacing {
			return fmt.Errorf("%w: fires must be at least 15 minutes apart", ErrInvalid)
		}
		previous = next
	}
	return nil
}
