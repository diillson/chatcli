/*
 * BuiltinSchedulerPlugin — exposes the scheduler as an @scheduler
 * ReAct tool. Subcommands:
 *
 *   schedule   { name, when, do, wait?, timeout?, poll?, …}  → job_id
 *   wait       { until, every?, timeout?, async?, then? }    → outcome
 *   query      { id }                                         → job
 *   list       { filter? }                                    → []summary
 *   cancel     { id, reason? }                                → ok
 *
 * Because the top-level ChatCLI owns the scheduler but the plugin is
 * instantiated before it, the plugin uses a package-level adapter
 * supplied via SetSchedulerAdapter (called from NewChatCLI after
 * initScheduler).
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// SchedulerAdapter is the interface the BuiltinSchedulerPlugin uses to
// reach the live scheduler. The chatcli top-level package provides an
// implementation bound to the current session.
type SchedulerAdapter interface {
	// Owner returns the principal for this invocation (e.g. agent name,
	// session id). Agents typically call with their own owner.
	Owner() SchedulerOwner
	ScheduleJob(ctx context.Context, owner SchedulerOwner, inputJSON string) (string, error)
	WaitUntil(ctx context.Context, owner SchedulerOwner, inputJSON string) (string, error)
	QueryJob(ctx context.Context, owner SchedulerOwner, inputJSON string) (string, error)
	ListJobs(ctx context.Context, owner SchedulerOwner, inputJSON string) (string, error)
	CancelJob(ctx context.Context, owner SchedulerOwner, inputJSON string) (string, error)
}

// SchedulerOwner is the package-local mirror of scheduler.Owner (the
// plugins package cannot import cli/scheduler without an import cycle).
type SchedulerOwner struct {
	Kind string
	ID   string
	Tag  string
}

var schedulerAdapterAtom atomic.Value // stores SchedulerAdapter

// SetSchedulerAdapter wires the live adapter. Called from the top-level
// cli package after initScheduler.
func SetSchedulerAdapter(a SchedulerAdapter) {
	schedulerAdapterAtom.Store(a)
}

// currentSchedulerAdapter returns the wired adapter or nil.
func currentSchedulerAdapter() SchedulerAdapter {
	v := schedulerAdapterAtom.Load()
	if v == nil {
		return nil
	}
	a, _ := v.(SchedulerAdapter)
	return a
}

// BuiltinSchedulerPlugin is the @scheduler tool.
type BuiltinSchedulerPlugin struct{}

// NewBuiltinSchedulerPlugin returns a ready-to-register plugin.
func NewBuiltinSchedulerPlugin() *BuiltinSchedulerPlugin { return &BuiltinSchedulerPlugin{} }

// Name returns "@scheduler".
func (*BuiltinSchedulerPlugin) Name() string { return "@scheduler" }

// Description surfaces the tool in /plugin list and the help.
func (*BuiltinSchedulerPlugin) Description() string {
	return "Durable job scheduler — schedule, wait on conditions, query or cancel jobs programmatically from the ReAct loop."
}

// Usage explains how the ReAct loop invokes the tool. Shows the
// canonical JSON envelope first because it is the most copy-pasteable
// form, then summarizes the per-subcommand fields with concrete value
// examples so the LLM can pattern-match without guessing.
//
// Action DSL (do=) — all forms below fire from a scheduled job:
//
//	"/run <task>"      task delegated to the agent ReAct loop
//	"/agent <task>"    same as /run; explicit agent invocation
//	"/coder <task>"    runs the agent in coder profile (CoderSystemPrompt)
//	"shell: <cmd>"     raw shell command (policy-classified, captured)
//	"agent: <task>"    boots the agent loop with the given task (DSL form)
//	"@<tool> <args>"   invokes a registered tool (e.g. "@coder exec ls")
//	"POST <url> | b"   webhook (also GET/PUT, with optional body)
//	"llm: <prompt>"    single-shot LLM call, no tools
//	"hook:<event>"     fires a chatcli hook event by name
//	"noop"             do nothing (used for pure wait/dependency jobs)
func (*BuiltinSchedulerPlugin) Usage() string {
	return `<tool_call name="@scheduler" args='{"cmd":"schedule","args":{"name":"docker-up","when":"+5m","do":"/run open -a Docker"}}' />

Subcommands (use cmd + args; do/when are required for schedule):
  schedule  {name, when:"+5m"|"in 10s"|"at 14:00"|"@every 1m"|"0 9 * * *",
             do:"/run <task>"|"/agent <task>"|"/coder <task>"|"shell: <cmd>"|"agent: <task>"|"@<tool> <args>"|"POST <url>"|"noop",
             wait?:{condition:"docker:NAME:running"|"http://...==200"|"file:/path"},
             until?:"<condition DSL>", timeout?, poll?, max_polls?,
             max_retries?, depends_on?, triggers?, ttl?, tags?, i_know?}
  wait      {until:"<condition>", every?, timeout?, max_polls?, async?, name?}
  query     {id}
  list      {filter?:{owner, statuses, tag, name_substr, include_terminal}}
  cancel    {id, reason?}

Aliases accepted for natural phrasing: delay→when, command/cmd/exec/run→do,
  in/after→when, i-know/iKnow/iknow/know→i_know. Bare durations like "5m"
  auto-prefix to "+5m".

For shell commands that would normally need approval, set i_know:true so
the job pre-authorizes itself at fire time (denylist rules still reject):
  {"cmd":"schedule","args":{"when":"+1m","do":"shell: open -a Docker","i_know":true}}`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinSchedulerPlugin) Version() string { return "1.1.0" }

// Path is empty for builtin plugins.
func (*BuiltinSchedulerPlugin) Path() string { return "" }

// Schema exposes a structured description that the agent prompt builder
// in cli/agent_mode.go (getToolContextString) renders into per-subcommand
// flag lists with examples. Keep examples concrete and copy-pasteable —
// the LLM uses these to learn field names without reinvention.
func (*BuiltinSchedulerPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred; argv form (subcommand --flag value) also accepted",
		"subcommands": []map[string]interface{}{
			{
				"name": "schedule",
				"description": "Enqueue a durable job. when= sets the trigger (relative/absolute/cron); do= sets the action. " +
					"Use wait= or until= when the action must block on a precondition.",
				"flags": []map[string]interface{}{
					{"name": "name", "type": "string", "description": "Human-readable job name (auto-generated if omitted)"},
					{"name": "when", "type": "string", "required": true,
						"description": "Trigger DSL: \"+5m\", \"in 10s\", \"after 1h\", \"5m\" (bare duration), \"at 14:00\", \"at 2026-04-25T10:00\", \"@every 30s\", \"0 9 * * *\" (cron)"},
					{"name": "do", "type": "string", "required": true,
						"description": "Action DSL: \"/run <task>\" (agent loop), \"/agent <task>\" (same), \"/coder <task>\" (agent in coder profile), \"shell: <cmd>\" (raw shell, policy-classified), \"agent: <task>\" (DSL alias), \"@<tool> <args>\" (e.g. \"@coder exec ls\"), \"POST <url> | <body>\" (webhook), \"llm: <prompt>\", \"hook:<event>\", \"/<other slash>\" (e.g. \"/jobs list\"), or \"noop\""},
					{"name": "until", "type": "string",
						"description": "Optional precondition before the action fires (e.g. \"docker:my-container:running\", \"http://host/health==200\", \"file:/tmp/done\", \"tcp://host:5432\")"},
					{"name": "wait", "type": "object", "description": "Explicit wait spec (condition, timeout, on_timeout). Prefer until= for the common case."},
					{"name": "timeout", "type": "string", "description": "Action timeout (Go duration: 30s, 5m, 1h)"},
					{"name": "poll", "type": "string", "description": "Wait poll interval (Go duration)"},
					{"name": "max_polls", "type": "integer", "description": "Cap wait iterations"},
					{"name": "max_retries", "type": "integer", "description": "Retry the action this many times on failure"},
					{"name": "depends_on", "type": "array", "description": "Job IDs that must finish before this one runs"},
					{"name": "triggers", "type": "array", "description": "Job IDs that should run when this one finishes"},
					{"name": "ttl", "type": "string", "description": "Auto-cancel after this duration"},
					{"name": "tags", "type": "object", "description": "Free-form key/value tags for filtering"},
					{"name": "i_know", "type": "boolean", "description": "Pre-authorize shell commands that would otherwise hit ShellPolicyAsk (no human at fire-time to approve). Denylist rules still reject. Mirrors --i-know on the /schedule slash command. Aliases: i-know, iKnow, iknow, know — all map to this field."},
				},
				"examples": []string{
					`{"cmd":"schedule","args":{"name":"docker-up","when":"+5m","do":"shell: open -a Docker","i_know":true}}`,
					`{"cmd":"schedule","args":{"name":"validate","when":"+6m","do":"shell: docker info","until":"docker:Docker:running","i_know":true}}`,
					`{"cmd":"schedule","args":{"name":"nightly","when":"0 2 * * *","do":"shell: ./backup.sh","i_know":true}}`,
					`{"cmd":"schedule","args":{"name":"refactor","when":"+30s","do":"/coder refactor cli/agent_mode.go to extract dispatch loop"}}`,
					`schedule --name docker-up --when +5m --do "shell: open -a Docker" --i_know`,
				},
			},
			{
				"name":        "wait",
				"description": "Block (or schedule asynchronously) until a condition holds. async=true returns the job id immediately.",
				"flags": []map[string]interface{}{
					{"name": "until", "type": "string", "required": true, "description": "Condition DSL (same shapes as schedule.until)"},
					{"name": "every", "type": "string", "description": "Poll interval"},
					{"name": "timeout", "type": "string", "description": "Maximum total wait time"},
					{"name": "max_polls", "type": "integer", "description": "Iteration cap"},
					{"name": "async", "type": "boolean", "description": "Return immediately with the job id instead of blocking"},
					{"name": "name", "type": "string", "description": "Optional name for the wait job"},
				},
				"examples": []string{
					`{"cmd":"wait","args":{"until":"docker:Docker:running","timeout":"2m","every":"5s"}}`,
					`{"cmd":"wait","args":{"until":"http://localhost:8080/health==200","async":true}}`,
				},
			},
			{
				"name":        "query",
				"description": "Inspect a single job by id.",
				"flags": []map[string]interface{}{
					{"name": "id", "type": "string", "required": true, "description": "Job id returned by schedule/wait"},
				},
				"examples": []string{`{"cmd":"query","args":{"id":"job-abc123"}}`},
			},
			{
				"name":        "list",
				"description": "List jobs the current owner can see, optionally filtered.",
				"flags": []map[string]interface{}{
					{"name": "filter", "type": "object", "description": "{owner, statuses:[\"queued\",\"running\"], tag, name_substr, include_terminal}"},
				},
				"examples": []string{
					`{"cmd":"list","args":{}}`,
					`{"cmd":"list","args":{"filter":{"statuses":["queued","running"]}}}`,
				},
			},
			{
				"name":        "cancel",
				"description": "Cancel a queued or running job.",
				"flags": []map[string]interface{}{
					{"name": "id", "type": "string", "required": true, "description": "Job id to cancel"},
					{"name": "reason", "type": "string", "description": "Free-form reason (audited)"},
				},
				"examples": []string{`{"cmd":"cancel","args":{"id":"job-abc123","reason":"superseded"}}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses the args and dispatches to the adapter.
func (p *BuiltinSchedulerPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream mirrors Execute — this plugin does not produce
// incremental output, so stream is ignored.
//
// The agent's tool dispatcher (cli/agent_tool_sanitizer.go,
// buildArgvFromJSONMap) flattens {"cmd":"schedule","args":{...}} into
// argv form ["schedule","--name","x","--when","+5m",...] before this
// plugin is invoked. Historically the plugin only json.Unmarshaled the
// joined argv string, so the agent's call always failed with
// "parse envelope: invalid character 's'". parseSchedulerInvocation
// now accepts JSON envelopes, flat JSON without args wrapper, and the
// flattened argv form, then applies field aliases (delay→when,
// command→do, …) so common LLM phrasings work first try.
func (p *BuiltinSchedulerPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentSchedulerAdapter()
	if adapter == nil {
		return "", errors.New("@scheduler: scheduler not initialized in this session")
	}
	if len(args) == 0 {
		return "", errors.New(`@scheduler: empty args. Example: <tool_call name="@scheduler" args='{"cmd":"schedule","args":{"name":"docker-up","when":"+5m","do":"/run open -a Docker"}}' />`)
	}

	cmd, inner, err := parseSchedulerInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@scheduler: %w", err)
	}

	switch cmd {
	case "schedule":
		return adapter.ScheduleJob(ctx, adapter.Owner(), inner)
	case "wait":
		return adapter.WaitUntil(ctx, adapter.Owner(), inner)
	case "query":
		return adapter.QueryJob(ctx, adapter.Owner(), inner)
	case "list":
		return adapter.ListJobs(ctx, adapter.Owner(), inner)
	case "cancel":
		return adapter.CancelJob(ctx, adapter.Owner(), inner)
	default:
		return "", fmt.Errorf(
			"@scheduler: unknown cmd %q (valid: schedule|wait|query|list|cancel). "+
				`Example: {"cmd":"schedule","args":{"name":"x","when":"+5m","do":"/run echo hi"}}`,
			cmd,
		)
	}
}

// parseSchedulerInvocation accepts three input shapes and returns the
// canonical (cmd, innerJSON):
//
//  1. JSON envelope:  {"cmd":"schedule","args":{"when":"+5m","do":"..."}}
//  2. Flat JSON:      {"cmd":"schedule","when":"+5m","do":"..."}
//  3. Argv form:      ["schedule","--when","+5m","--do","..."]
//
// The argv form is what the agent's tool sanitizer produces when it
// converts a JSON envelope (with args.command) into CLI flags — the
// plugin must accept it because the conversion is unconditional.
func parseSchedulerInvocation(args []string) (string, string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))

	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", "", fmt.Errorf(
				`parse envelope: %w. Expected {"cmd":"schedule","args":{"when":"+5m","do":"/run cmd"}}`, err,
			)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		canon := canonicalSchedulerCmd(cmdStr)
		if canon == "" {
			return "", "", fmt.Errorf(
				"missing or unknown cmd %q (valid: schedule|wait|query|list|cancel)", cmdStr,
			)
		}
		var inner string
		if rargs, ok := raw["args"]; ok && len(rargs) > 0 {
			inner = string(rargs)
		} else {
			delete(raw, "cmd")
			b, _ := json.Marshal(raw)
			inner = string(b)
		}
		return canon, applySchedulerAliases(inner), nil
	}

	canon := canonicalSchedulerCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf(
			`expected JSON envelope or subcommand argv; got %q. Example: {"cmd":"schedule","args":{"when":"+5m","do":"/run cmd"}}`,
			args[0],
		)
	}
	inner, err := schedulerFlagsToJSON(args[1:])
	if err != nil {
		return "", "", err
	}
	return canon, applySchedulerAliases(inner), nil
}

// canonicalSchedulerCmd folds aliases into the five canonical names.
// Returns "" for unknown values.
func canonicalSchedulerCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "schedule":
		return "schedule"
	case "wait", "wait_until":
		return "wait"
	case "query", "get":
		return "query"
	case "list":
		return "list"
	case "cancel":
		return "cancel"
	}
	return ""
}

// schedulerFlagsToJSON converts ["--key","value","--bool",...] into a
// JSON object. Values that look like JSON scalars/objects/arrays are
// parsed so the underlying ToolInput receives the right Go type
// (e.g. depends_on=["a","b"] survives the round-trip).
func schedulerFlagsToJSON(argv []string) (string, error) {
	obj := map[string]interface{}{}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if !strings.HasPrefix(a, "-") {
			return "", fmt.Errorf(
				"unexpected positional argument %q (use --key value pairs or pass JSON envelope)", a,
			)
		}
		key := strings.TrimLeft(a, "-")
		if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "--") {
			obj[key] = true
			continue
		}
		val := argv[i+1]
		i++
		if v, ok := tryParseJSONScalar(val); ok {
			obj[key] = v
		} else {
			obj[key] = val
		}
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// tryParseJSONScalar returns the JSON-typed value if s is a valid JSON
// scalar / object / array. Bare strings without quotes ("docker-up")
// stay as Go strings — only literal JSON tokens (numbers, true/false/null,
// {…}, […], "quoted") are upgraded. This keeps int fields like
// max_polls=5 typed correctly while not double-parsing free text.
func tryParseJSONScalar(s string) (interface{}, bool) {
	t := strings.TrimSpace(s)
	if t == "" {
		return nil, false
	}
	c := t[0]
	jsonish := c == '{' || c == '[' || c == '"' ||
		c == '-' || (c >= '0' && c <= '9') ||
		t == "true" || t == "false" || t == "null"
	if !jsonish {
		return nil, false
	}
	var v interface{}
	if err := json.Unmarshal([]byte(t), &v); err == nil {
		return v, true
	}
	return nil, false
}

// applySchedulerAliases rewrites natural-language field names that LLMs
// commonly produce into the canonical schema names. Operates on the
// inner JSON only and only when the canonical name is absent — explicit
// values always win.
//
// Why: the @scheduler tool's first failure mode is the LLM reaching for
// "delay"/"command" instead of "when"/"do". Aliasing here makes the
// natural phrasing work without weakening the canonical schema.
func applySchedulerAliases(inner string) string {
	t := strings.TrimSpace(inner)
	if t == "" || !strings.HasPrefix(t, "{") {
		return inner
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(t), &m); err != nil {
		return inner
	}
	aliases := map[string]string{
		"delay":     "when",
		"command":   "do",
		"cmd":       "do",
		"exec":      "do",
		"run":       "do",
		"interval":  "every",
		"condition": "until",
		"wait_for":  "until",
		"job_id":    "id",
		"jobid":     "id",
		// IKnow pre-authorization. The Go field is ToolInput.IKnow with
		// json tag "i_know" — but the LLM (and the slash-CLI form) reach
		// for hyphenated/camelCase variants. Alias all of them so
		// {"i-know":true}, {"iKnow":true}, --i-know argv (decoded as
		// "i-know") all wire through to the canonical i_know field.
		"i-know": "i_know",
		"iknow":  "i_know",
		"iKnow":  "i_know",
		"know":   "i_know",
	}
	for from, to := range aliases {
		if v, ok := m[from]; ok {
			if _, exists := m[to]; !exists {
				m[to] = v
			}
			delete(m, from)
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return inner
	}
	return string(b)
}
