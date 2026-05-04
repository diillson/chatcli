/*
 * ParkRequest + sentinel error.
 *
 * A ParkRequest describes WHAT the agent is waiting for: a fixed delay,
 * a wallclock deadline, or a polling probe (HTTP/shell) with a success
 * condition. Plugins emit ParkRequest values; the agent loop consumes
 * them via the ErrAgentParked sentinel.
 */
package park

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Mode selects the timing semantics of a park.
type Mode string

const (
	// ModeDelay parks for a fixed duration measured from the moment the
	// snapshot is taken. Single-shot.
	ModeDelay Mode = "delay"
	// ModeUntil parks until an absolute wallclock time. Single-shot.
	ModeUntil Mode = "until"
	// ModeForURL parks until an HTTP probe matches SuccessWhen, polling
	// every Interval up to Deadline.
	ModeForURL Mode = "for_url"
	// ModeForCmd parks until a shell command's output matches
	// SuccessWhen, polling every Interval up to Deadline.
	ModeForCmd Mode = "for_cmd"
)

// Request is the value an agent tool returns wrapped in ErrAgentParked.
// All fields are optional; Validate enforces the per-mode invariants.
type Request struct {
	Mode Mode `json:"mode"`

	// Delay applies to ModeDelay only.
	Delay time.Duration `json:"delay,omitempty"`

	// Until applies to ModeUntil only.
	Until time.Time `json:"until,omitempty"`

	// URL applies to ModeForURL only.
	URL string `json:"url,omitempty"`

	// HTTPMethod is the verb used for ModeForURL probes. Defaults to
	// "GET" if empty.
	HTTPMethod string `json:"http_method,omitempty"`

	// HTTPHeaders are forwarded with the probe request. Optional.
	HTTPHeaders map[string]string `json:"http_headers,omitempty"`

	// Command applies to ModeForCmd only.
	Command string `json:"command,omitempty"`

	// Interval is how often the poll fires for ModeForURL / ModeForCmd.
	// Required for those modes; the scheduler clamps to a sane minimum.
	Interval time.Duration `json:"interval,omitempty"`

	// Deadline is the absolute time at which polling gives up and the
	// agent resumes with a "timeout" status. Required for poll modes.
	Deadline time.Time `json:"deadline,omitempty"`

	// SuccessWhen is the matcher for poll modes. Free-form expression:
	//   - "status=200"            (HTTP status equals 200)
	//   - "status=200..299"       (HTTP status in range)
	//   - "body contains:foo"     (response body or stdout contains "foo")
	//   - "body matches:^OK$"     (regexp on response body or stdout)
	//   - "exit=0"                (shell exit code equals 0; ModeForCmd)
	// Empty means "any successful probe", i.e. HTTP 2xx or exit 0.
	SuccessWhen string `json:"success_when,omitempty"`

	// Note is a human-readable label shown in /parked. Optional.
	Note string `json:"note,omitempty"`
}

// MinPollInterval is the lower bound enforced on poll-mode Interval —
// guards against polling storms when the model emits "interval=1ms".
const MinPollInterval = 5 * time.Second

// MaxParkDuration caps the absolute time horizon any single park can
// span. Two weeks is plenty for CI/Terraform jobs and protects the
// scheduler from runaway snapshots that never resume.
const MaxParkDuration = 14 * 24 * time.Hour

// Validate returns a non-nil error when the request is malformed.
// Callers (the @park plugin and bridge) MUST run this before persisting.
func (r Request) Validate() error {
	switch r.Mode {
	case ModeDelay:
		if r.Delay <= 0 {
			return errors.New("park: delay must be > 0")
		}
		if r.Delay > MaxParkDuration {
			return fmt.Errorf("park: delay exceeds max (%s)", MaxParkDuration)
		}
	case ModeUntil:
		if r.Until.IsZero() {
			return errors.New("park: until is required")
		}
		if d := time.Until(r.Until); d <= 0 {
			return errors.New("park: until is in the past")
		}
		if d := time.Until(r.Until); d > MaxParkDuration {
			return fmt.Errorf("park: until exceeds max horizon (%s)", MaxParkDuration)
		}
	case ModeForURL:
		if strings.TrimSpace(r.URL) == "" {
			return errors.New("park: url is required for mode=for_url")
		}
		if !strings.HasPrefix(r.URL, "http://") && !strings.HasPrefix(r.URL, "https://") {
			return errors.New("park: url must be http(s)://")
		}
		if err := validatePollFields(r.Interval, r.Deadline); err != nil {
			return err
		}
	case ModeForCmd:
		if strings.TrimSpace(r.Command) == "" {
			return errors.New("park: command is required for mode=for_cmd")
		}
		if err := validatePollFields(r.Interval, r.Deadline); err != nil {
			return err
		}
	default:
		return fmt.Errorf("park: unknown mode %q", r.Mode)
	}
	return nil
}

func validatePollFields(interval time.Duration, deadline time.Time) error {
	if interval <= 0 {
		return errors.New("park: interval must be > 0")
	}
	if interval < MinPollInterval {
		return fmt.Errorf("park: interval below minimum (%s)", MinPollInterval)
	}
	if deadline.IsZero() {
		return errors.New("park: deadline (timeout) is required for poll modes")
	}
	d := time.Until(deadline)
	if d <= 0 {
		return errors.New("park: deadline is in the past")
	}
	if d > MaxParkDuration {
		return fmt.Errorf("park: deadline exceeds max horizon (%s)", MaxParkDuration)
	}
	return nil
}

// FireAt returns the next time the scheduler should fire (or poll).
// For ModeDelay / ModeUntil it's the actual resume time. For poll modes
// it's "now + first interval".
func (r Request) FireAt(now time.Time) time.Time {
	switch r.Mode {
	case ModeDelay:
		return now.Add(r.Delay)
	case ModeUntil:
		return r.Until
	case ModeForURL, ModeForCmd:
		return now.Add(r.Interval)
	}
	return now
}

// IsPolling reports whether the request requires a polling driver
// (park_poll action) rather than a one-shot timer (agent_resume action).
func (r Request) IsPolling() bool {
	return r.Mode == ModeForURL || r.Mode == ModeForCmd
}

// errAgentParked is the sentinel returned by tool plugins to suspend
// the agent loop. It carries the validated Request so the loop knows
// what to schedule.
type errAgentParked struct {
	Req Request
}

// Error satisfies the error interface; the message is informational
// only — the loop matches via errors.As, not the text.
func (e *errAgentParked) Error() string {
	return fmt.Sprintf("agent parked (mode=%s)", e.Req.Mode)
}

// NewParkError wraps a Request as a sentinel error. Tools return this
// instead of an actual failure to ask the loop to suspend.
func NewParkError(r Request) error {
	return &errAgentParked{Req: r}
}

// AsParkError unwraps a sentinel and returns (request, true) when the
// error was produced by NewParkError. Returns (zero, false) otherwise.
func AsParkError(err error) (Request, bool) {
	var p *errAgentParked
	if errors.As(err, &p) {
		return p.Req, true
	}
	return Request{}, false
}
