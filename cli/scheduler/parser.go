/*
 * ChatCLI - Scheduler: DSL parser for schedules and conditions.
 *
 * The user-facing / agent-facing surface of the scheduler accepts
 * short, ergonomic strings for the common cases and falls back to
 * structured JSON/YAML specs for complex ones.
 *
 * Schedule DSL:
 *
 *   "in 5m" / "+5m" / "after 30s"            → ScheduleRelative
 *   "at 2026-04-25T14:00" / "at now"         → ScheduleAbsolute
 *   "every 30s" / "every 2h"                 → ScheduleInterval
 *   "cron: 0 2 * * *" / "@daily"             → ScheduleCron
 *   "when-ready" / "on-condition"            → ScheduleOnCondition
 *   "manual" / "triggered"                   → ScheduleManual
 *
 * Condition DSL:
 *
 *   "http://host/health==200"                   → http_status expected=200
 *   "http://host/health~=/ok/"                  → http_status + regex
 *   "shell: <cmd>" / "shell[0]:cmd"             → shell_exit
 *   "file:/path"                                → file_exists
 *   "file:/path>=10"                            → file_exists min_size
 *   "k8s:pod/ns/name:ready"                     → k8s_resource_ready
 *   "docker:<container>:running"                → docker_running
 *   "tcp://host:port"                           → tcp_reachable
 *   "regex:<cmd>~=/pattern/"                    → regex_match
 *   "llm: <prompt>"                             → llm_check
 *   "and(<expr1>, <expr2>)" / "or(...)"         → composite
 *   "not <expr>"                                → apply Negate=true
 *
 * Ambiguous or complex inputs must be submitted as a JSON spec via
 * --wait-spec-file / --condition-json.
 */
package scheduler

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ─── Schedule DSL ─────────────────────────────────────────────

// ParseScheduleDSL interprets a short string as a Schedule.
func ParseScheduleDSL(input string) (Schedule, error) {
	in := strings.TrimSpace(input)
	if in == "" {
		return Schedule{}, fmt.Errorf("schedule: empty input")
	}
	lower := strings.ToLower(in)

	// Cron shorthand or explicit prefix.
	if strings.HasPrefix(lower, "@") {
		return Schedule{Kind: ScheduleCron, Cron: in}, nil
	}
	if strings.HasPrefix(lower, "cron:") {
		return Schedule{Kind: ScheduleCron, Cron: strings.TrimSpace(in[len("cron:"):])}, nil
	}
	// Five-field cron detection (no prefix) — tolerate "0 2 * * *" literal.
	if isLikelyCron(lower) {
		return Schedule{Kind: ScheduleCron, Cron: in}, nil
	}

	// Absolute: "at YYYY-MM-DD..." or RFC3339.
	if strings.HasPrefix(lower, "at ") {
		rest := strings.TrimSpace(in[len("at "):])
		if strings.EqualFold(rest, "now") {
			return Schedule{Kind: ScheduleAbsolute, ExactTime: time.Now()}, nil
		}
		t, err := parseTimeFuzzy(rest)
		if err != nil {
			return Schedule{}, fmt.Errorf("schedule at: %w", err)
		}
		return Schedule{Kind: ScheduleAbsolute, ExactTime: t}, nil
	}

	// Relative: "in 5m", "+5m", "after 10s".
	if strings.HasPrefix(in, "+") {
		d, err := time.ParseDuration(strings.TrimPrefix(in, "+"))
		if err != nil {
			return Schedule{}, fmt.Errorf("schedule: invalid duration %q: %w", in, err)
		}
		return Schedule{Kind: ScheduleRelative, Relative: d}, nil
	}
	for _, prefix := range []string{"in ", "after "} {
		if strings.HasPrefix(lower, prefix) {
			d, err := time.ParseDuration(strings.TrimSpace(in[len(prefix):]))
			if err != nil {
				return Schedule{}, fmt.Errorf("schedule: invalid duration: %w", err)
			}
			return Schedule{Kind: ScheduleRelative, Relative: d}, nil
		}
	}

	// Interval: "every 30s".
	if strings.HasPrefix(lower, "every ") {
		d, err := time.ParseDuration(strings.TrimSpace(in[len("every "):]))
		if err != nil {
			return Schedule{}, fmt.Errorf("schedule: invalid interval: %w", err)
		}
		return Schedule{Kind: ScheduleInterval, Interval: d}, nil
	}

	if lower == "when-ready" || lower == "on-condition" || lower == "oncondition" {
		return Schedule{Kind: ScheduleOnCondition}, nil
	}
	if lower == "manual" || lower == "triggered" {
		return Schedule{Kind: ScheduleManual}, nil
	}

	// Last resort — naked duration?
	if d, err := time.ParseDuration(in); err == nil {
		return Schedule{Kind: ScheduleRelative, Relative: d}, nil
	}
	return Schedule{}, fmt.Errorf("schedule: cannot parse %q", in)
}

// isLikelyCron heuristically checks whether a string looks like a
// 5-field cron expression so users can pass them without the `cron:`
// prefix.
func isLikelyCron(s string) bool {
	fields := strings.Fields(s)
	if len(fields) != 5 {
		return false
	}
	for _, f := range fields {
		if !isCronFieldShape(f) {
			return false
		}
	}
	return true
}

var cronFieldRE = regexp.MustCompile(`^[*/,0-9A-Za-z\-]+$`)

func isCronFieldShape(f string) bool { return cronFieldRE.MatchString(f) }

// parseTimeFuzzy accepts RFC3339, date, datetime. Falls back to
// "2006-01-02 15:04" / "2006-01-02 15:04:05".
func parseTimeFuzzy(s string) (time.Time, error) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
		"15:04",
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			// If the user gave a time-of-day, project onto today or
			// tomorrow if already passed.
			if l == "15:04" {
				now := time.Now()
				cand := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, time.Local)
				if !cand.After(now) {
					cand = cand.Add(24 * time.Hour)
				}
				return cand, nil
			}
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse time %q", s)
}

// ─── Condition DSL ────────────────────────────────────────────

var (
	dslHTTPEq     = regexp.MustCompile(`^(https?://\S+?)(==|~=)(.+)$`)
	dslTCP        = regexp.MustCompile(`^tcp://([^:/\s]+):(\d+)$`)
	dslK8sColon   = regexp.MustCompile(`^k8s:([^:/]+)(?:/([^:/]+))?/([^:/]+)(?::([^:]+))?$`)
	dslDockerItem = regexp.MustCompile(`^docker:([^:]+)(?::(running|healthy))?$`)
	dslFileItem   = regexp.MustCompile(`^file:([^<>=]+)(?:(>=|>)(\d+))?$`)
	dslNot        = regexp.MustCompile(`^not\s+(.+)$`)
	dslCombinator = regexp.MustCompile(`^(and|or|all_of|any_of)\((.*)\)$`)
)

// ParseConditionDSL interprets a short string as a Condition.
func ParseConditionDSL(input string) (Condition, error) {
	in := strings.TrimSpace(input)
	if in == "" {
		return Condition{}, fmt.Errorf("condition: empty input")
	}

	// Negation.
	if m := dslNot.FindStringSubmatch(in); m != nil {
		child, err := ParseConditionDSL(m[1])
		if err != nil {
			return Condition{}, err
		}
		child.Negate = !child.Negate
		return child, nil
	}

	// Combinators.
	if m := dslCombinator.FindStringSubmatch(in); m != nil {
		op := m[1]
		if op == "and" {
			op = "all_of"
		}
		if op == "or" {
			op = "any_of"
		}
		parts := splitTopLevelArgs(m[2])
		if len(parts) < 2 {
			return Condition{}, fmt.Errorf("%s: need >=2 children", op)
		}
		children := make([]Condition, 0, len(parts))
		for _, p := range parts {
			c, err := ParseConditionDSL(p)
			if err != nil {
				return Condition{}, fmt.Errorf("%s child: %w", op, err)
			}
			children = append(children, c)
		}
		return Condition{Type: op, Children: children}, nil
	}

	// HTTP.
	if m := dslHTTPEq.FindStringSubmatch(in); m != nil {
		url, op, rhs := m[1], m[2], strings.TrimSpace(m[3])
		spec := map[string]any{"url": url}
		switch op {
		case "==":
			n, err := strconv.Atoi(rhs)
			if err != nil {
				return Condition{}, fmt.Errorf("http condition: expected numeric status, got %q", rhs)
			}
			spec["expected"] = n
		case "~=":
			if strings.HasPrefix(rhs, "/") && strings.HasSuffix(rhs, "/") && len(rhs) >= 2 {
				rhs = rhs[1 : len(rhs)-1]
			}
			spec["expected_regex"] = rhs
		}
		return Condition{Type: "http_status", Spec: spec}, nil
	}

	// TCP.
	if m := dslTCP.FindStringSubmatch(in); m != nil {
		port, _ := strconv.Atoi(m[2])
		return Condition{Type: "tcp_reachable", Spec: map[string]any{"host": m[1], "port": port}}, nil
	}

	// Kubernetes.
	if m := dslK8sColon.FindStringSubmatch(in); m != nil {
		kind := m[1]
		ns := m[2]
		name := m[3]
		condT := m[4]
		if condT == "" {
			condT = "Ready"
		}
		spec := map[string]any{"kind": kind, "name": name, "condition": condT}
		if ns != "" {
			spec["namespace"] = ns
		}
		return Condition{Type: "k8s_resource_ready", Spec: spec}, nil
	}

	// Docker.
	if m := dslDockerItem.FindStringSubmatch(in); m != nil {
		spec := map[string]any{"container": m[1]}
		if m[2] == "healthy" {
			spec["healthy"] = true
		}
		return Condition{Type: "docker_running", Spec: spec}, nil
	}

	// File.
	if m := dslFileItem.FindStringSubmatch(in); m != nil {
		spec := map[string]any{"path": strings.TrimSpace(m[1])}
		if m[2] != "" {
			size, _ := strconv.Atoi(m[3])
			spec["min_size"] = size
		}
		return Condition{Type: "file_exists", Spec: spec}, nil
	}

	// Shell.
	if strings.HasPrefix(in, "shell:") || strings.HasPrefix(in, "sh:") {
		cmd := strings.TrimSpace(in[strings.Index(in, ":")+1:])
		return Condition{Type: "shell_exit", Spec: map[string]any{"cmd": cmd}}, nil
	}

	// LLM.
	if strings.HasPrefix(in, "llm:") {
		prompt := strings.TrimSpace(in[len("llm:"):])
		return Condition{Type: "llm_check", Spec: map[string]any{"prompt": prompt}}, nil
	}

	// Regex match (cmd~=/pattern/).
	if idx := strings.Index(in, "~="); idx > 0 && !strings.HasPrefix(in, "http") {
		cmd := strings.TrimSpace(in[:idx])
		rest := strings.TrimSpace(in[idx+2:])
		if strings.HasPrefix(rest, "/") && strings.HasSuffix(rest, "/") && len(rest) >= 2 {
			rest = rest[1 : len(rest)-1]
		}
		return Condition{Type: "regex_match", Spec: map[string]any{"cmd": cmd, "pattern": rest}}, nil
	}

	return Condition{}, fmt.Errorf("condition: cannot parse %q", in)
}

// splitTopLevelArgs splits "a, b, c(d, e), f" into ["a", "b", "c(d, e)", "f"].
func splitTopLevelArgs(s string) []string {
	out := []string{}
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	tail := strings.TrimSpace(s[start:])
	if tail != "" {
		out = append(out, tail)
	}
	return out
}

// ─── Action DSL ───────────────────────────────────────────────

// ParseActionDSL interprets a short string as an Action.
//
//	"/foo bar"              → slash_cmd command=/foo bar
//	"shell: <cmd>"          → shell command=<cmd>
//	"agent: <task>"         → agent_task task=<task>
//	"worker <name>: <task>" → worker_dispatch
//	"llm: <prompt>"         → llm_prompt prompt=<prompt>
//	"POST https://...| body" → webhook
//	"hook:<event>"          → hook event=<event>
//	"noop"                  → noop
func ParseActionDSL(input string) (Action, error) {
	in := strings.TrimSpace(input)
	if in == "" {
		return Action{}, fmt.Errorf("action: empty input")
	}
	lower := strings.ToLower(in)
	switch {
	case strings.HasPrefix(in, "/") || strings.HasPrefix(in, "@"):
		return Action{Type: ActionSlashCmd, Payload: map[string]any{"command": in}}, nil
	case strings.HasPrefix(lower, "shell:") || strings.HasPrefix(lower, "sh:"):
		cmd := strings.TrimSpace(in[strings.Index(in, ":")+1:])
		return Action{Type: ActionShell, Payload: map[string]any{"command": cmd}}, nil
	case strings.HasPrefix(lower, "agent:"):
		task := strings.TrimSpace(in[len("agent:"):])
		return Action{Type: ActionAgentTask, Payload: map[string]any{"task": task}}, nil
	case strings.HasPrefix(lower, "worker "):
		// "worker planner: plan X" — split name and task.
		rest := strings.TrimSpace(in[len("worker "):])
		colon := strings.Index(rest, ":")
		if colon < 0 {
			return Action{}, fmt.Errorf("action worker: missing ':' between name and task")
		}
		return Action{Type: ActionWorkerDispatch, Payload: map[string]any{
			"agent_type": strings.TrimSpace(rest[:colon]),
			"task":       strings.TrimSpace(rest[colon+1:]),
		}}, nil
	case strings.HasPrefix(lower, "llm:"):
		prompt := strings.TrimSpace(in[len("llm:"):])
		return Action{Type: ActionLLMPrompt, Payload: map[string]any{"prompt": prompt}}, nil
	case strings.HasPrefix(lower, "hook:"):
		evt := strings.TrimSpace(in[len("hook:"):])
		return Action{Type: ActionHook, Payload: map[string]any{"event": evt}}, nil
	case strings.EqualFold(in, "noop"):
		return Action{Type: ActionNoop, Payload: map[string]any{}}, nil
	case strings.HasPrefix(strings.ToUpper(in), "POST ") || strings.HasPrefix(strings.ToUpper(in), "GET ") || strings.HasPrefix(strings.ToUpper(in), "PUT "):
		// "<METHOD> <url> [| body]"
		fields := strings.SplitN(in, " ", 2)
		if len(fields) != 2 {
			return Action{}, fmt.Errorf("action webhook: need method + url")
		}
		method := strings.ToUpper(fields[0])
		rest := fields[1]
		var body string
		if idx := strings.Index(rest, "|"); idx > 0 {
			body = strings.TrimSpace(rest[idx+1:])
			rest = strings.TrimSpace(rest[:idx])
		}
		payload := map[string]any{"url": rest, "method": method}
		if body != "" {
			payload["body"] = body
		}
		return Action{Type: ActionWebhook, Payload: payload}, nil
	}
	return Action{}, fmt.Errorf("action: cannot parse %q", in)
}

// ParseConditionJSON loads a condition from a JSON string.
func ParseConditionJSON(raw string) (Condition, error) {
	var c Condition
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Condition{}, fmt.Errorf("condition json: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Condition{}, err
	}
	return c, nil
}

// ParseActionJSON loads an action from a JSON string.
func ParseActionJSON(raw string) (Action, error) {
	var a Action
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return Action{}, fmt.Errorf("action json: %w", err)
	}
	if err := a.Validate(); err != nil {
		return Action{}, err
	}
	return a, nil
}
