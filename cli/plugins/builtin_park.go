/*
 * BuiltinParkPlugin — exposes agent loop suspension as the @park ReAct
 * tool. When invoked, the plugin parses the request, validates it, and
 * returns it to the agent loop wrapped in park.NewParkError. The loop
 * (cli/agent_mode.go) detects the sentinel, snapshots state, schedules
 * the resume job, and returns to the user prompt.
 *
 * Subcommands (semantically the four park modes):
 *
 *   delay   {duration}              fixed timer
 *   until   {when}                  wallclock RFC3339 / "in 5m" / "+5m"
 *   for_url {url, interval, deadline, success_when?}  HTTP polling
 *   for_cmd {cmd,  interval, deadline, success_when?} shell polling
 *
 * The plugin does NOT touch the scheduler directly — that is the agent
 * loop's responsibility, because only the loop has the live snapshot of
 * the chat history and tool counters at the exact suspension point.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
)

// BuiltinParkPlugin is the @park tool.
type BuiltinParkPlugin struct{}

// NewBuiltinParkPlugin returns a registerable instance.
func NewBuiltinParkPlugin() *BuiltinParkPlugin { return &BuiltinParkPlugin{} }

// Name is the canonical tool name visible to the LLM.
func (*BuiltinParkPlugin) Name() string { return "@park" }

// Description surfaces the tool in /plugin list.
func (*BuiltinParkPlugin) Description() string {
	return "Suspend the agent loop until a timer elapses or a probe succeeds, freeing the terminal in the meantime. Resumes automatically with full context restored."
}

// Usage explains the canonical invocation forms.
func (*BuiltinParkPlugin) Usage() string {
	return `<tool_call name="@park" args='{"cmd":"delay","args":{"duration":"5m","note":"waiting for CI"}}' />

Subcommands:
  delay    {duration:"5m"|"30s"|"1h", note?}
  until    {when:"2026-05-04T18:00:00Z"|"in 5m"|"+5m", note?}
  for_url  {url:"https://...", interval:"30s", deadline:"10m",
            method?:"GET", headers?:{"Authorization":"Bearer ..."},
            success_when?:"status=200..299"|"body contains:OK"|"body matches:^done$",
            note?}
  for_cmd  {cmd:"gh run view --json status -q .status",
            interval:"30s", deadline:"10m",
            success_when?:"exit=0"|"body contains:completed"|"body matches:^success$",
            note?}

When the park completes (timer fires, probe matches, or deadline passes)
the agent resumes from where it stopped and a synthetic tool result with
the outcome is appended to the loop's context. The user can also force a
resume via /resume <token> or cancel via /cancel-park <token>.`
}

// Version is bumped whenever the surface changes.
func (*BuiltinParkPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinParkPlugin) Path() string { return "" }

// Schema returns the structured contract used by the agent prompt
// builder to inject per-subcommand flag lists into the system prompt.
func (*BuiltinParkPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": `JSON envelope {cmd, args} preferred; argv form (subcommand --flag value) also accepted`,
		"subcommands": []map[string]interface{}{
			{
				"name":        "delay",
				"description": "Park for a fixed duration. Single-shot.",
				"flags": []map[string]interface{}{
					{"name": "duration", "type": "string", "required": true, "description": "Go duration (5m, 30s, 1h). Max 14d."},
					{"name": "note", "type": "string", "description": "Human label shown in /parked"},
				},
				"examples": []string{
					`{"cmd":"delay","args":{"duration":"5m","note":"waiting for CI #1234"}}`,
					`{"cmd":"delay","args":{"duration":"30s"}}`,
				},
			},
			{
				"name":        "until",
				"description": "Park until an absolute wallclock time.",
				"flags": []map[string]interface{}{
					{"name": "when", "type": "string", "required": true, "description": `RFC3339 ("2026-05-04T18:00:00Z") or relative DSL ("in 5m", "+5m", "after 30s")`},
					{"name": "note", "type": "string", "description": "Human label"},
				},
				"examples": []string{
					`{"cmd":"until","args":{"when":"2026-05-04T18:00:00Z"}}`,
					`{"cmd":"until","args":{"when":"in 10m","note":"deploy window"}}`,
				},
			},
			{
				"name":        "for_url",
				"description": "Park while polling an HTTP endpoint until the response matches success_when (or the deadline elapses).",
				"flags": []map[string]interface{}{
					{"name": "url", "type": "string", "required": true},
					{"name": "interval", "type": "string", "required": true, "description": "Poll cadence (Go duration; minimum 5s)"},
					{"name": "deadline", "type": "string", "required": true, "description": "Total max wait (Go duration or RFC3339)"},
					{"name": "method", "type": "string", "description": `HTTP method; defaults to "GET"`},
					{"name": "headers", "type": "object", "description": `Optional headers, e.g. {"Authorization":"Bearer ..."}`},
					{"name": "success_when", "type": "string", "description": `Matcher: "status=200", "status=200..299", "body contains:foo", "body matches:^OK$". Empty = any 2xx.`},
					{"name": "note", "type": "string"},
				},
				"examples": []string{
					`{"cmd":"for_url","args":{"url":"https://api.github.com/repos/x/y/actions/runs/123","interval":"30s","deadline":"10m","success_when":"body contains:\"status\":\"completed\""}}`,
					`{"cmd":"for_url","args":{"url":"https://service/health","interval":"15s","deadline":"5m","success_when":"status=200..299"}}`,
				},
			},
			{
				"name":        "for_cmd",
				"description": "Park while polling a shell command until output matches success_when (or the deadline elapses). Subject to the same coder safety policy as @coder exec.",
				"flags": []map[string]interface{}{
					{"name": "cmd", "type": "string", "required": true},
					{"name": "interval", "type": "string", "required": true, "description": "Poll cadence (Go duration; minimum 5s)"},
					{"name": "deadline", "type": "string", "required": true, "description": "Total max wait"},
					{"name": "success_when", "type": "string", "description": `Matcher: "exit=0", "body contains:foo", "body matches:^done$". Empty = exit 0.`},
					{"name": "note", "type": "string"},
				},
				"examples": []string{
					`{"cmd":"for_cmd","args":{"cmd":"gh run view 1234 --json status -q .status","interval":"30s","deadline":"10m","success_when":"body matches:^completed$"}}`,
					`{"cmd":"for_cmd","args":{"cmd":"terraform plan -detailed-exitcode -no-color","interval":"45s","deadline":"15m","success_when":"exit=0"}}`,
				},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute is the legacy entry-point; defers to ExecuteWithStream.
func (p *BuiltinParkPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream parses the invocation, validates the request, and
// returns the sentinel error wrapping it. The stream callback is unused
// — park has no incremental output by design.
func (p *BuiltinParkPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	if len(args) == 0 {
		return "", errors.New(`@park: empty args. Example: <tool_call name="@park" args='{"cmd":"delay","args":{"duration":"5m"}}' />`)
	}

	cmd, inner, err := parseParkInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@park: %w", err)
	}

	req, err := buildParkRequest(cmd, inner)
	if err != nil {
		return "", fmt.Errorf("@park: %w", err)
	}
	if err := req.Validate(); err != nil {
		return "", fmt.Errorf("@park: %w", err)
	}

	// The agent loop catches the sentinel via park.AsParkError and
	// drives the snapshot + scheduler enqueue. The plugin itself stays
	// pure: no I/O, no globals, no scheduler dependency — purely
	// declarative. The empty string output means "no streamable output";
	// the loop's renderer skips empty-output tools.
	return "", park.NewParkError(req)
}

// parseParkInvocation accepts the same three input shapes as the
// scheduler plugin: JSON envelope, flat JSON, and argv form.
func parseParkInvocation(args []string) (string, string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))

	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", "", fmt.Errorf(
				`parse envelope: %w. Expected {"cmd":"delay","args":{"duration":"5m"}}`, err,
			)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		canon := canonicalParkCmd(cmdStr)
		if canon == "" {
			return "", "", fmt.Errorf("missing or unknown cmd %q (valid: delay|until|for_url|for_cmd)", cmdStr)
		}
		var inner string
		if rargs, ok := raw["args"]; ok && len(rargs) > 0 {
			inner = string(rargs)
		} else {
			delete(raw, "cmd")
			b, _ := json.Marshal(raw)
			inner = string(b)
		}
		return canon, inner, nil
	}

	canon := canonicalParkCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf(
			`expected JSON envelope or subcommand argv; got %q. Example: {"cmd":"delay","args":{"duration":"5m"}}`, args[0],
		)
	}
	inner, err := parkFlagsToJSON(args[1:])
	if err != nil {
		return "", "", err
	}
	return canon, inner, nil
}

// canonicalParkCmd folds aliases. The aliases mirror what an LLM is
// likely to emit — "wait" is rejected explicitly because that's a
// distinct scheduler concept and conflating them would be confusing.
func canonicalParkCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "delay", "sleep", "timer":
		return "delay"
	case "until", "at":
		return "until"
	case "for_url", "url", "http", "poll_url":
		return "for_url"
	case "for_cmd", "cmd", "shell", "poll_cmd":
		return "for_cmd"
	}
	return ""
}

// parkFlagsToJSON converts ["--duration","5m","--note","ci"] into a
// JSON object — the same surface shape the JSON envelope emits in its
// args field. Object/array values are parsed when they look like JSON
// so {"headers":{"X-Token":"abc"}} survives via flag form.
func parkFlagsToJSON(argv []string) (string, error) {
	obj := map[string]interface{}{}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if !strings.HasPrefix(a, "-") {
			return "", fmt.Errorf("unexpected positional %q (use --key value or pass a JSON envelope)", a)
		}
		key := strings.TrimLeft(a, "-")
		if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "--") {
			obj[key] = true
			continue
		}
		val := argv[i+1]
		i++
		// Try to parse JSON-ish values so headers/object/array survive
		// the round-trip. Strings remain strings.
		if v, ok := tryParseJSONScalar(val); ok {
			obj[key] = v
			continue
		}
		obj[key] = val
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// buildParkRequest decodes the inner-args JSON object into a typed
// park.Request. Per-mode fields are extracted carefully so accidental
// extras don't silently drop user intent.
func buildParkRequest(cmd, innerJSON string) (park.Request, error) {
	var raw map[string]any
	if strings.TrimSpace(innerJSON) == "" {
		raw = map[string]any{}
	} else if err := json.Unmarshal([]byte(innerJSON), &raw); err != nil {
		return park.Request{}, fmt.Errorf("parse args: %w", err)
	}

	r := park.Request{Note: asParkString(raw, "note")}

	switch cmd {
	case "delay":
		r.Mode = park.ModeDelay
		ds := asParkString(raw, "duration")
		if ds == "" {
			return park.Request{}, errors.New("delay: duration is required")
		}
		d, err := time.ParseDuration(ds)
		if err != nil {
			return park.Request{}, fmt.Errorf("delay: parse duration %q: %w", ds, err)
		}
		r.Delay = d

	case "until":
		r.Mode = park.ModeUntil
		when := asParkString(raw, "when")
		if when == "" {
			return park.Request{}, errors.New("until: when is required")
		}
		t, err := parseAbsoluteOrRelative(when, time.Now())
		if err != nil {
			return park.Request{}, fmt.Errorf("until: %w", err)
		}
		r.Until = t

	case "for_url", "for_cmd":
		if cmd == "for_url" {
			r.Mode = park.ModeForURL
			r.URL = strings.TrimSpace(asParkString(raw, "url"))
			r.HTTPMethod = strings.ToUpper(strings.TrimSpace(asParkString(raw, "method")))
			if h, ok := raw["headers"].(map[string]any); ok {
				r.HTTPHeaders = make(map[string]string, len(h))
				for k, v := range h {
					if vs, ok := v.(string); ok {
						r.HTTPHeaders[k] = vs
					}
				}
			}
		} else {
			r.Mode = park.ModeForCmd
			r.Command = strings.TrimSpace(asParkString(raw, "cmd"))
			if r.Command == "" {
				// Accept "command" as a more verbose alias.
				r.Command = strings.TrimSpace(asParkString(raw, "command"))
			}
		}
		intStr := asParkString(raw, "interval")
		if intStr == "" {
			return park.Request{}, errors.New("polling park: interval is required")
		}
		interval, err := time.ParseDuration(intStr)
		if err != nil {
			return park.Request{}, fmt.Errorf("polling park: parse interval %q: %w", intStr, err)
		}
		r.Interval = interval

		dl := asParkString(raw, "deadline")
		if dl == "" {
			// "timeout" is a common synonym; accept it.
			dl = asParkString(raw, "timeout")
		}
		if dl == "" {
			return park.Request{}, errors.New("polling park: deadline (or timeout) is required")
		}
		t, err := parseAbsoluteOrRelative(dl, time.Now())
		if err != nil {
			return park.Request{}, fmt.Errorf("polling park: parse deadline: %w", err)
		}
		r.Deadline = t

		r.SuccessWhen = strings.TrimSpace(asParkString(raw, "success_when"))

	default:
		return park.Request{}, fmt.Errorf("unknown cmd %q", cmd)
	}

	return r, nil
}

// parseAbsoluteOrRelative accepts RFC3339, common date/time layouts,
// and the relative DSL ("+5m", "in 5m", "after 30s", "5m"). Returns the
// resolved absolute time relative to now.
func parseAbsoluteOrRelative(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("empty time spec")
	}
	// Relative forms first — most common in agent-emitted args.
	rel := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(s, "+"), "in "), "after "))
	if d, err := time.ParseDuration(rel); err == nil {
		return now.Add(d), nil
	}
	// Absolute layouts.
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
		"15:04",
	} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			// "15:04" returns a date in year 0; project onto today.
			if t.Year() == 0 {
				now := now
				t = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, time.Local)
				if !t.After(now) {
					t = t.Add(24 * time.Hour)
				}
			}
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time format %q (try RFC3339, \"+5m\", or \"in 5m\")", s)
}

// asParkString safely extracts a string field from a map[string]any,
// returning "" when missing or when the field is non-string.
func asParkString(m map[string]any, k string) string {
	v, ok := m[k]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}
