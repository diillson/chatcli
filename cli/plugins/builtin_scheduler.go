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

// Usage explains how the ReAct loop invokes the tool.
func (*BuiltinSchedulerPlugin) Usage() string {
	return `<tool_call name="@scheduler" args='{"cmd":"schedule","args":{"name":"x","when":"+5m","do":"/run tests"}}' />

Subcommands:
  schedule  args: {name, when, do, wait?, timeout?, poll?, max_polls?, max_retries?, depends_on?, triggers?, ttl?, tags?}
  wait      args: {until, every?, timeout?, max_polls?, async?, then?, on_timeout?, name?}
  query     args: {id}
  list      args: {filter?:{owner,statuses,tag,name_substr,include_terminal}}
  cancel    args: {id, reason?}`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinSchedulerPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinSchedulerPlugin) Path() string { return "" }

// Schema exposes the JSON args shape for model hint purposes.
func (*BuiltinSchedulerPlugin) Schema() string {
	return `{
  "type": "object",
  "required": ["cmd"],
  "properties": {
    "cmd":  {"type": "string", "enum": ["schedule", "wait", "query", "list", "cancel"]},
    "args": {"type": "object"}
  }
}`
}

// schedulerArgs decodes the envelope.
type schedulerArgs struct {
	Cmd  string          `json:"cmd"`
	Args json.RawMessage `json:"args"`
}

// Execute parses the args and dispatches to the adapter.
func (p *BuiltinSchedulerPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream mirrors Execute — this plugin does not produce
// incremental output, so stream is ignored.
func (p *BuiltinSchedulerPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentSchedulerAdapter()
	if adapter == nil {
		return "", errors.New("@scheduler: scheduler not initialized in this session")
	}
	if len(args) == 0 {
		return "", fmt.Errorf("@scheduler: empty args; expected JSON envelope")
	}
	payload := strings.Join(args, " ")
	var envelope schedulerArgs
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return "", fmt.Errorf("@scheduler: parse envelope: %w", err)
	}
	var inner string
	if len(envelope.Args) > 0 {
		inner = string(envelope.Args)
	} else {
		inner = "{}"
	}
	switch strings.ToLower(strings.TrimSpace(envelope.Cmd)) {
	case "schedule":
		return adapter.ScheduleJob(ctx, adapter.Owner(), inner)
	case "wait", "wait_until":
		return adapter.WaitUntil(ctx, adapter.Owner(), inner)
	case "query", "get":
		return adapter.QueryJob(ctx, adapter.Owner(), inner)
	case "list":
		return adapter.ListJobs(ctx, adapter.Owner(), inner)
	case "cancel":
		return adapter.CancelJob(ctx, adapter.Owner(), inner)
	default:
		return "", fmt.Errorf("@scheduler: unknown cmd %q (valid: schedule|wait|query|list|cancel)", envelope.Cmd)
	}
}
