/*
 * ChatCLI - Scheduler: Schedule value type + cron parser.
 *
 * A Schedule answers one question: "when should this job next be
 * considered for execution?". The scheduler's main loop calls
 * Schedule.Next(now, prev) to compute the next fire time.
 *
 * Cron expressions:
 *
 *   Five-field standard form — "minute hour day-of-month month day-of-week"
 *
 *   Field             Range        Aliases
 *   minute            0-59
 *   hour              0-23
 *   day-of-month      1-31
 *   month             1-12         jan feb mar apr may jun jul aug sep oct nov dec
 *   day-of-week       0-6 (0=Sun)  sun mon tue wed thu fri sat
 *
 *   Specials per field: "*", "N", "A,B,C", "A-B", "N/S", "A-B/S".
 *   Shorthand: "@hourly" "@daily" "@weekly" "@monthly" "@yearly".
 *
 * The parser is intentionally minimal — we support the subset that
 * covers 99% of automation use cases and bail with a clear error on
 * anything exotic. No external cron library so we keep compile time
 * / supply chain small.
 */
package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ScheduleKind classifies how Next is computed.
type ScheduleKind string

const (
	// ScheduleAbsolute fires once at ExactTime and is terminal.
	ScheduleAbsolute ScheduleKind = "absolute"
	// ScheduleRelative fires once at created_at + Relative.
	ScheduleRelative ScheduleKind = "relative"
	// ScheduleCron fires on every Cron match until cancelled.
	ScheduleCron ScheduleKind = "cron"
	// ScheduleInterval fires every Interval from first fire.
	ScheduleInterval ScheduleKind = "interval"
	// ScheduleOnCondition has no time component — Next returns now and
	// the wait-condition gate does the work.
	ScheduleOnCondition ScheduleKind = "on_condition"
	// ScheduleManual never fires on its own; it's triggered by another
	// job's Triggers edge. Used for DAG leaf nodes.
	ScheduleManual ScheduleKind = "manual"
)

// MissPolicy decides how to handle a fire window that passed while
// the scheduler was not running (laptop asleep, daemon crashed).
type MissPolicy string

const (
	// MissFireOnce — run once for the missed window, regardless of
	// how many cron ticks happened. Default.
	MissFireOnce MissPolicy = "fire_once"
	// MissFireAll — run once per missed tick (noisy; opt-in).
	MissFireAll MissPolicy = "fire_all"
	// MissSkip — drop the missed window entirely.
	MissSkip MissPolicy = "skip"
)

// Schedule describes when a Job should next be considered for execution.
// Exactly one of ExactTime / Relative / Cron / Interval is used per
// Kind; the others are zero values.
type Schedule struct {
	Kind       ScheduleKind  `json:"kind"`
	ExactTime  time.Time     `json:"exact_time,omitempty"`
	Relative   time.Duration `json:"relative,omitempty"`
	Cron       string        `json:"cron,omitempty"`
	Interval   time.Duration `json:"interval,omitempty"`
	MissPolicy MissPolicy    `json:"miss_policy,omitempty"`
	// Timezone, if set, adjusts cron evaluation. Go time.Location name
	// (IANA) — "Local", "UTC", "America/Sao_Paulo", etc. Empty = Local.
	Timezone string `json:"timezone,omitempty"`

	// cronSchedule is the parsed cron form, cached after the first
	// successful Validate(). Not serialized — rebuilt on load.
	cronSchedule *cronSchedule `json:"-"`
}

// Validate returns a non-nil error when the Schedule's fields are
// inconsistent with its Kind. Also pre-parses cron so Next() doesn't
// pay the cost repeatedly.
func (s *Schedule) Validate() error {
	switch s.Kind {
	case ScheduleAbsolute:
		if s.ExactTime.IsZero() {
			return fmt.Errorf("absolute schedule requires exact_time")
		}
	case ScheduleRelative:
		if s.Relative <= 0 {
			return fmt.Errorf("relative schedule requires positive duration")
		}
	case ScheduleCron:
		if strings.TrimSpace(s.Cron) == "" {
			return fmt.Errorf("cron schedule requires a cron expression")
		}
		parsed, err := parseCron(s.Cron)
		if err != nil {
			return fmt.Errorf("cron parse: %w", err)
		}
		s.cronSchedule = parsed
	case ScheduleInterval:
		if s.Interval <= 0 {
			return fmt.Errorf("interval schedule requires positive duration")
		}
	case ScheduleOnCondition, ScheduleManual:
		// no time fields required
	default:
		return fmt.Errorf("unknown schedule kind %q", s.Kind)
	}

	if s.MissPolicy == "" {
		s.MissPolicy = MissFireOnce
	}
	switch s.MissPolicy {
	case MissFireOnce, MissFireAll, MissSkip:
	default:
		return fmt.Errorf("unknown miss_policy %q", s.MissPolicy)
	}

	if s.Timezone != "" {
		if _, err := time.LoadLocation(s.Timezone); err != nil {
			return fmt.Errorf("invalid timezone %q: %w", s.Timezone, err)
		}
	}
	return nil
}

// Next returns the time at which this Schedule should fire strictly
// after `after`. createdAt is used to anchor relative/interval. When
// no further fires are possible (absolute schedule already passed),
// returns zero time.
//
// Next is deterministic: given the same inputs, the result is the
// same. The scheduler main loop calls it once per transition.
func (s *Schedule) Next(after, createdAt time.Time) time.Time {
	loc := s.location()
	after = after.In(loc)
	switch s.Kind {
	case ScheduleAbsolute:
		if s.ExactTime.After(after) {
			return s.ExactTime
		}
		return time.Time{}
	case ScheduleRelative:
		// Fire once at createdAt + Relative. If already past, the
		// scheduler main loop applies MissPolicy.
		t := createdAt.Add(s.Relative)
		if t.After(after) {
			return t
		}
		// Missed window — let MissPolicy handle (returned time = now).
		return after
	case ScheduleCron:
		if s.cronSchedule == nil {
			// Lazy parse — Validate should have run, but don't panic.
			parsed, err := parseCron(s.Cron)
			if err != nil {
				return time.Time{}
			}
			s.cronSchedule = parsed
		}
		return s.cronSchedule.Next(after)
	case ScheduleInterval:
		// For interval, fire at createdAt + N*Interval > after.
		// Compute the minimum N such that createdAt + N*Interval > after.
		if after.Before(createdAt) {
			return createdAt.Add(s.Interval)
		}
		elapsed := after.Sub(createdAt)
		if elapsed < 0 {
			return createdAt.Add(s.Interval)
		}
		n := int64(elapsed/s.Interval) + 1
		return createdAt.Add(time.Duration(n) * s.Interval)
	case ScheduleOnCondition:
		// Always "fireable now"; wait-condition does the gating.
		return after
	case ScheduleManual:
		// Never self-fires.
		return time.Time{}
	}
	return time.Time{}
}

// IsRecurring reports whether the schedule can fire more than once.
func (s *Schedule) IsRecurring() bool {
	return s.Kind == ScheduleCron || s.Kind == ScheduleInterval
}

// location resolves the timezone, defaulting to Local on any parse failure.
func (s *Schedule) location() *time.Location {
	if s.Timezone == "" {
		return time.Local
	}
	if loc, err := time.LoadLocation(s.Timezone); err == nil {
		return loc
	}
	return time.Local
}

// ─── Cron parser ───────────────────────────────────────────────

// cronSchedule is the pre-parsed form of a cron expression.
// Each field is a 64-bit bitmask; the Nth bit set means value N is
// allowed. (Since month has 12 values and day-of-month 31, 64 bits
// is more than enough for every field.)
type cronSchedule struct {
	minute     uint64 // bits 0..59
	hour       uint64 // bits 0..23
	dayOfMonth uint64 // bits 1..31 (bit 0 unused)
	month      uint64 // bits 1..12 (bit 0 unused)
	dayOfWeek  uint64 // bits 0..6 (0=Sun)
	// domRestricted / dowRestricted capture the classic cron quirk:
	// when BOTH day-of-month and day-of-week are unrestricted ("*"),
	// OR when both are restricted, ANDing them would be wrong — the
	// historical cron semantics is OR between dom and dow whenever
	// either is restricted.
	domRestricted bool
	dowRestricted bool
}

// Next computes the next matching time strictly after `after`. Returns
// the zero time only when no match can be found within the following
// 400 years — which, in practice, means the expression is vacuous
// (e.g. `0 0 30 2 *` for Feb 30th).
func (c *cronSchedule) Next(after time.Time) time.Time {
	// Cron is minute-resolution; round up to the next minute.
	t := after.Add(1 * time.Minute).Truncate(time.Minute)
	maxYear := t.Year() + 400

	for t.Year() < maxYear {
		// Month must match; advance if not.
		if c.month&(1<<uint(t.Month())) == 0 {
			// Jump to first day of next month.
			year, month := t.Year(), t.Month()+1
			if month > 12 {
				year++
				month = 1
			}
			t = time.Date(year, month, 1, 0, 0, 0, 0, t.Location())
			continue
		}

		// Day must match (with dom/dow OR semantics).
		domOK := c.dayOfMonth&(1<<uint(t.Day())) != 0
		dowOK := c.dayOfWeek&(1<<uint(t.Weekday())) != 0
		var dayMatches bool
		switch {
		case !c.domRestricted && !c.dowRestricted:
			dayMatches = true
		case c.domRestricted && !c.dowRestricted:
			dayMatches = domOK
		case !c.domRestricted && c.dowRestricted:
			dayMatches = dowOK
		default:
			// Both restricted → OR (matches if either allows).
			dayMatches = domOK || dowOK
		}
		if !dayMatches {
			// Next day at midnight.
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).Add(24 * time.Hour)
			continue
		}

		// Hour.
		if c.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}

		// Minute.
		if c.minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(1 * time.Minute)
			continue
		}

		return t
	}
	return time.Time{}
}

// parseCron accepts a 5-field cron expression or a shorthand alias and
// returns a cronSchedule ready for Next().
func parseCron(expr string) (*cronSchedule, error) {
	expr = strings.TrimSpace(expr)
	if alias, ok := cronAliases[strings.ToLower(expr)]; ok {
		expr = alias
	}

	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron: expected 5 fields, got %d", len(fields))
	}

	cs := &cronSchedule{}
	var err error

	if cs.minute, cs.domRestricted, err = parseCronField(fields[0], 0, 59, nil, false); err != nil {
		return nil, fmt.Errorf("field minute: %w", err)
	}
	cs.domRestricted = false // reset; we only track for dom field below.

	if cs.hour, _, err = parseCronField(fields[1], 0, 23, nil, false); err != nil {
		return nil, fmt.Errorf("field hour: %w", err)
	}

	if cs.dayOfMonth, cs.domRestricted, err = parseCronField(fields[2], 1, 31, nil, true); err != nil {
		return nil, fmt.Errorf("field day-of-month: %w", err)
	}

	if cs.month, _, err = parseCronField(fields[3], 1, 12, monthAliases, false); err != nil {
		return nil, fmt.Errorf("field month: %w", err)
	}

	if cs.dayOfWeek, cs.dowRestricted, err = parseCronField(fields[4], 0, 6, dowAliases, true); err != nil {
		return nil, fmt.Errorf("field day-of-week: %w", err)
	}

	return cs, nil
}

// parseCronField parses a single cron field. restrictTrackable enables
// the "is this field restricted?" flag — only relevant for dom and dow
// (see cronSchedule doc). Returns (bitmask, restricted, error).
func parseCronField(raw string, lo, hi int, aliases map[string]int, restrictTrackable bool) (uint64, bool, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return 0, false, fmt.Errorf("empty field")
	}

	restricted := restrictTrackable && raw != "*"
	var mask uint64

	// Split on comma for OR sets.
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, false, fmt.Errorf("empty term in %q", raw)
		}

		// Optional /step suffix.
		step := 1
		rangeSpec := part
		if idx := strings.Index(part, "/"); idx >= 0 {
			rangeSpec = part[:idx]
			stepStr := part[idx+1:]
			s, err := strconv.Atoi(stepStr)
			if err != nil || s <= 0 {
				return 0, false, fmt.Errorf("invalid step %q", stepStr)
			}
			step = s
		}

		// Expand the range.
		var start, end int
		switch {
		case rangeSpec == "*":
			start, end = lo, hi
		case strings.Contains(rangeSpec, "-"):
			split := strings.SplitN(rangeSpec, "-", 2)
			s, err := resolveCronValue(split[0], aliases, lo, hi)
			if err != nil {
				return 0, false, err
			}
			e, err := resolveCronValue(split[1], aliases, lo, hi)
			if err != nil {
				return 0, false, err
			}
			start, end = s, e
		default:
			v, err := resolveCronValue(rangeSpec, aliases, lo, hi)
			if err != nil {
				return 0, false, err
			}
			start, end = v, v
		}

		if start > end {
			return 0, false, fmt.Errorf("range %d-%d invalid", start, end)
		}

		for i := start; i <= end; i += step {
			mask |= 1 << uint(i)
		}
	}
	return mask, restricted, nil
}

// resolveCronValue parses either a numeric literal or a named alias
// (jan, mon, etc.). Enforces [lo, hi] bounds.
func resolveCronValue(s string, aliases map[string]int, lo, hi int) (int, error) {
	s = strings.TrimSpace(s)
	if v, ok := aliases[s]; ok {
		return v, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", s)
	}
	if n < lo || n > hi {
		return 0, fmt.Errorf("value %d out of range [%d,%d]", n, lo, hi)
	}
	return n, nil
}

var monthAliases = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dowAliases = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	// Accept "7" too, which some vixie-cron variants use for Sunday.
}

var cronAliases = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}
