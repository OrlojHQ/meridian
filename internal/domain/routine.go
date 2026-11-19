package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	// The time zone database is embedded so a routine's zone resolves the
	// same way on a host without /usr/share/zoneinfo.
	_ "time/tzdata"
)

// Routines (ADR 0057). A routine is a clock, a fixed
// prompt, and limits. Each fire starts one fresh Project Thread.

const (
	RoutineMinSpacing        = 15 * time.Minute
	RoutineMinRun            = 5 * time.Minute
	RoutineMaxRun            = 24 * time.Hour
	RoutineDefaultRun        = 2 * time.Hour
	RoutineMaxCatchUp        = 24 * time.Hour
	RoutineDefaultCatchUp    = time.Hour
	RoutineDefaultKept       = 5
	RoutineMaxKept           = 20
	RoutineMaxPromptBytes    = 32 << 10
	RoutineStartFailureLimit = 3
	// RoutineModelWait bounds how long a fire waits for the harness to
	// report its models, and then to confirm a switch.
	RoutineModelWait = 2 * time.Minute
	// RoutineIdleSettle is how long a Thread must stay idle with nothing
	// waiting before its outcome is decided, so Project checks that start
	// after a turn ends are seen first.
	RoutineIdleSettle = 20 * time.Second
	// RoutineMissedRecords caps the missed rows written for one gap.
	RoutineMissedRecords = 24
	maxCadenceBytes      = 128
	maxTimeZoneBytes     = 64
)

var routineNamePattern = regexp.MustCompile(`^[a-z0-9-]{1,48}$`)

type RoutineID string
type RoutineFireID string

// ValidRoutineName reports whether name is a routine name.
func ValidRoutineName(name string) bool {
	return routineNamePattern.MatchString(name)
}

// Routine is one operator-authored clock on a Project. The prompt, model,
// and effort are sealed together in Content*; they never appear here in
// plaintext.
type Routine struct {
	ID                RoutineID
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

func (s Routine) Validate() error {
	if s.ID == "" || s.ProjectID == "" || !ValidRoutineName(s.Name) || !ValidHarnessName(s.Harness) ||
		s.Cadence == "" || len(s.Cadence) > maxCadenceBytes ||
		s.TimeZone == "" || len(s.TimeZone) > maxTimeZoneBytes ||
		s.MaxRun < RoutineMinRun || s.MaxRun > RoutineMaxRun || s.MaxRun%time.Second != 0 ||
		s.CatchUp < 0 || s.CatchUp > RoutineMaxCatchUp || s.CatchUp%time.Second != 0 ||
		s.MaxKept < 1 || s.MaxKept > RoutineMaxKept || len(s.DisabledReason) > 64 ||
		s.StartFailures < 0 || len(s.ContentNonce) == 0 || len(s.ContentCiphertext) == 0 ||
		s.ResourceVersion <= 0 || s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: invalid routine", ErrInvalid)
	}
	return nil
}

// RoutineFireTrigger says what started a fire.
type RoutineFireTrigger string

const (
	RoutineTriggerClock   RoutineFireTrigger = "clock"
	RoutineTriggerCatchUp RoutineFireTrigger = "catch_up"
	RoutineTriggerManual  RoutineFireTrigger = "manual"
)

func (t RoutineFireTrigger) Valid() bool {
	return t == RoutineTriggerClock || t == RoutineTriggerCatchUp || t == RoutineTriggerManual
}

// RoutineFireState is where a fire is. starting waits for the Thread's
// session, selecting applies the model and effort, running waits for the
// outcome, and cleaning ends the session and deletes an empty Capsule.
type RoutineFireState string

const (
	RoutineFireSkipped   RoutineFireState = "skipped"
	RoutineFireMissed    RoutineFireState = "missed"
	RoutineFireStarting  RoutineFireState = "starting"
	RoutineFireSelecting RoutineFireState = "selecting"
	RoutineFireRunning   RoutineFireState = "running"
	RoutineFireCleaning  RoutineFireState = "cleaning"
	RoutineFireFinished  RoutineFireState = "finished"
)

func (s RoutineFireState) Valid() bool {
	switch s {
	case RoutineFireSkipped, RoutineFireMissed, RoutineFireStarting, RoutineFireSelecting,
		RoutineFireRunning, RoutineFireCleaning, RoutineFireFinished:
		return true
	}
	return false
}

// Active reports whether the fire still holds a run in progress.
func (s RoutineFireState) Active() bool {
	switch s {
	case RoutineFireStarting, RoutineFireSelecting, RoutineFireRunning, RoutineFireCleaning:
		return true
	}
	return false
}

// RoutineOutcome is the content-free result of a fire.
type RoutineOutcome string

const (
	RoutineOutcomeNone      RoutineOutcome = ""
	RoutineOutcomeProposed  RoutineOutcome = "proposed"
	RoutineOutcomeChanged   RoutineOutcome = "changed"
	RoutineOutcomeNoChanges RoutineOutcome = "no_changes"
	RoutineOutcomeWaiting   RoutineOutcome = "waiting"
	RoutineOutcomeTimedOut  RoutineOutcome = "timed_out"
	RoutineOutcomeFailed    RoutineOutcome = "failed"
)

func (o RoutineOutcome) Valid() bool {
	switch o {
	case RoutineOutcomeNone, RoutineOutcomeProposed, RoutineOutcomeChanged, RoutineOutcomeNoChanges,
		RoutineOutcomeWaiting, RoutineOutcomeTimedOut, RoutineOutcomeFailed:
		return true
	}
	return false
}

// NeedsOperator reports whether the outcome raises attention.
func (o RoutineOutcome) NeedsOperator() bool {
	switch o {
	case RoutineOutcomeProposed, RoutineOutcomeChanged, RoutineOutcomeWaiting,
		RoutineOutcomeTimedOut, RoutineOutcomeFailed:
		return true
	}
	return false
}

// Skip and failure reasons. They are codes, never message text.
const (
	RoutineReasonOverlap       = "overlap"
	RoutineReasonCapacity      = "capacity"
	RoutineReasonBacklog       = "backlog"
	RoutineReasonOwner         = "owner_unavailable"
	RoutineReasonProject       = "project_unavailable"
	RoutineReasonMissed        = "missed"
	RoutineReasonStartFailed   = "start_failed"
	RoutineReasonModelMissing  = "model_unreported"
	RoutineReasonModelNone     = "model_unsupported"
	RoutineReasonModelAbsent   = "model_unavailable"
	RoutineReasonModelRefused  = "model_refused"
	RoutineReasonEffortNone    = "effort_unsupported"
	RoutineReasonEffortAbsent  = "effort_unavailable"
	RoutineReasonEffortRefused = "effort_refused"
	RoutineReasonSessionEnded  = "session_ended"
	RoutineReasonInspectFailed = "inspect_failed"
	// Disabled reasons.
	RoutineDisabledStartFailures = "start_failures"
	RoutineDisabledHarness       = "harness_not_allowed"
	RoutineDisabledTransferred   = "transferred"
)

// RoutineFire is one tick of a routine's clock.
type RoutineFire struct {
	ID              RoutineFireID
	RoutineID       RoutineID
	ProjectID       ProjectID
	OwnerUserID     UserID
	Trigger         RoutineFireTrigger
	ScheduledFor    time.Time
	State           RoutineFireState
	Outcome         RoutineOutcome
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

func (f RoutineFire) Validate() error {
	if f.ID == "" || f.RoutineID == "" || f.ProjectID == "" || !f.Trigger.Valid() ||
		f.ScheduledFor.IsZero() || !f.State.Valid() || !f.Outcome.Valid() || len(f.Reason) > 64 ||
		f.ResourceVersion <= 0 || f.CreatedAt.IsZero() || f.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: invalid routine fire", ErrInvalid)
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
// fires starting at from are closer than RoutineMinSpacing, or when the
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
		if next.Sub(previous) < RoutineMinSpacing {
			return fmt.Errorf("%w: fires must be at least 15 minutes apart", ErrInvalid)
		}
		previous = next
	}
	return nil
}
