/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry wiring. The pulse bus is fed from the chokepoints that
 * already exist — the run registry (every agent, worker, subagent, MoA
 * member and task graph as a node with a parent link) and the request
 * auditor (every LLM request of every provider on every surface) — through
 * keyed observers, so the hub bridge and the audit log that own the default
 * slots keep working untouched.
 *
 * Every tap is a plain function call that returns after one atomic load
 * while the dashboard is off, and enqueues without blocking while it is on.
 * Events carry metadata only: the task text of a run and the content of a
 * request never leave the process through here.
 */
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/version"
	"go.uber.org/zap"
)

// pulseObserverKey is the slot the live telemetry taps register under.
const pulseObserverKey = "pulse"

// pulseSessionNodeID is the root node every other node of a process hangs
// from.
const pulseSessionNodeID = "session"

// pulseMCPSnapshotKey orders the MCP servers after the session and the runs
// when current state is replayed.
const pulseMCPSnapshotKey = "20-mcp"

// pulseProcSnapshotKey replays the supervised background processes.
const pulseProcSnapshotKey = "30-proc"

// pulseForced reports whether CHATCLI_DASH asks this process to record from
// boot, without waiting for a dashboard lease. It is how surfaces with no
// prompt (ACP, the MCP server, the gateway daemon) opt in.
func pulseForced() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(pulseDashEnv))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// initPulse installs the taps and starts the recording controller. It is
// called once from the shared bootstrap, so every surface gets it.
func (cli *ChatCLI) initPulse(ctx context.Context, surface string) {
	if cli == nil || cli.pulse != nil {
		return
	}
	logger := cli.logger
	ctl, started := startProcessPulse(ctx, surface, logger)
	if started {
		pulse.Default().RegisterSnapshotter("00-session", cli.pulseSessionSnapshot)
	}
	cli.pulse, cli.pulseOwner = ctl, started
}

// The process records through one controller, whichever entry point
// started it: a second ChatCLI in the same process (a server hosting the
// gateway and a pipeline backend) shares it, so no event is spooled twice.
var (
	pulseProcMu  sync.Mutex
	pulseProcCtl *pulse.Controller
)

// startProcessPulse installs the taps every surface needs — agent runs, LLM
// requests and the caller behind each request — and starts the recording
// controller once per process. started is false when one was running.
func startProcessPulse(ctx context.Context, surface string, logger *zap.Logger) (ctl *pulse.Controller, started bool) {
	pulseProcMu.Lock()
	defer pulseProcMu.Unlock()
	if pulseProcCtl != nil {
		return pulseProcCtl, false
	}
	bus := pulse.Default()
	bus.RegisterSnapshotter("10-runs", pulseRunsSnapshot)
	runs.Default().OnEventKeyed(pulseObserverKey, func(info runs.Info) { bus.Emit(pulseRunEvent(info)) })
	llmTap := newPulseLLMTap(bus)
	client.RegisterRequestAuditorKeyed(pulseObserverKey, llmTap.observe)
	// Names the agent run behind each LLM request, so a request hangs from
	// the agent that made it instead of from the session. It also lands in
	// the request log and the audit trail, where it was just as missing.
	client.SetCallerResolver(func(ctx context.Context) string { return runs.FromContext(ctx).ID() })
	// On the dashboard a request hangs from the innermost node instead (a
	// chat turn, a MoA panel, a tool call), never loose under the session.
	client.SetParentResolver(pulseCallerFrom)

	wd, _ := os.Getwd()
	// The controller outlives any request: bound by process shutdown, not
	// by the constructor's context.
	pulseProcCtl = pulse.Start(context.WithoutCancel(ctx), pulse.Options{
		Bus:      bus,
		ForcedFn: pulseForced,
		Meta: pulse.Meta{
			PID:     os.Getpid(),
			Surface: surface,
			Version: version.Version,
			WorkDir: filepath.Base(wd),
		},
		OnError: func(err error) {
			if logger != nil {
				logger.Debug("pulse: spool error", zap.Error(err))
			}
		},
	})
	return pulseProcCtl, true
}

// stopProcessPulse closes the process controller started by
// startProcessPulse, so a later entry point may start a new one.
func stopProcessPulse(ctl *pulse.Controller) {
	pulseProcMu.Lock()
	defer pulseProcMu.Unlock()
	if ctl == nil || ctl != pulseProcCtl {
		return
	}
	ctl.Close()
	pulseProcCtl = nil
}

// StartPulseRecorder starts live telemetry recording for an entry point that
// never builds a ChatCLI — the standalone scheduler daemon, a bare server. It gets the same
// lease-driven lifecycle and the same CHATCLI_DASH switch as every other
// surface, and a session node of its own so its jobs have a root to hang
// from. The returned func stops recording and flushes the spool.
func StartPulseRecorder(ctx context.Context, surface string, logger *zap.Logger) func() {
	ctl, started := startProcessPulse(ctx, surface, logger)
	if !started {
		// Another entry point already records this process; it owns the
		// session node and the controller.
		return func() {}
	}
	bus := pulse.Default()
	bus.RegisterSnapshotter("00-session", func() []pulse.Event {
		return []pulse.Event{{
			Kind:   pulse.KindSession,
			Phase:  pulse.PhaseStart,
			ID:     pulseSessionNodeID,
			Name:   surface,
			Status: pulse.StatusRunning,
		}}
	})
	return func() {
		bus.RegisterSnapshotter("00-session", nil)
		stopProcessPulse(ctl)
	}
}

// pulseSurface renames the process role once the entry point knows it.
func (cli *ChatCLI) pulseSurface(surface string) {
	if cli == nil {
		return
	}
	cli.pulse.SetSurface(surface)
}

// shutdownPulse detaches the taps and flushes the spool.
func (cli *ChatCLI) shutdownPulse() {
	if cli == nil || cli.pulse == nil {
		return
	}
	if !cli.pulseOwner {
		// Another entry point owns the process recorder and its taps.
		cli.pulse = nil
		return
	}
	runs.Default().OnEventKeyed(pulseObserverKey, nil)
	client.RegisterRequestAuditorKeyed(pulseObserverKey, nil)
	bus := pulse.Default()
	bus.RegisterSnapshotter("00-session", nil)
	bus.RegisterSnapshotter("10-runs", nil)
	bus.RegisterSnapshotter(pulseMCPSnapshotKey, nil)
	bus.RegisterSnapshotter(pulseProcSnapshotKey, nil)
	stopProcessPulse(cli.pulse)
	cli.pulse = nil
}

// pulseSessionSnapshot describes the process itself: the root of its graph.
// The pair it carries is the one that serves the next request — the @model
// override when the AI set one — and it seeds the route state, so the turn
// that follows only speaks up if the pair changes.
func (cli *ChatCLI) pulseSessionSnapshot() []pulse.Event {
	provider, model, source := cli.effectivePulseRoute()
	cli.pulseRoute.set(provider, model, source)
	ev := pulseRouteEvent(pulse.PhaseStart, provider, model, source, "")
	ev.Name = "chatcli"
	return []pulse.Event{ev}
}

// pulseRunsSnapshot replays the runs that are live right now, so a dashboard
// opened in the middle of a dispatch shows the workers already in flight.
func pulseRunsSnapshot() []pulse.Event {
	active := runs.Default().Active()
	out := make([]pulse.Event, 0, len(active))
	for _, info := range active {
		out = append(out, pulseRunEvent(info))
	}
	return out
}

// pulseRunEvent projects a run snapshot onto the bus. Task (the prompt given
// to the agent) and Err (free-form text) are deliberately left out.
func pulseRunEvent(info runs.Info) pulse.Event {
	ev := pulse.Event{
		Kind:   pulse.KindAgent,
		ID:     info.ID,
		Parent: info.ParentID,
		Name:   info.Agent,
	}
	// A run hangs from the span it started under when that span is an
	// entry point (a chat turn, an inbound RPC), which the dashboard draws
	// to the left of the agents. Under a tool or a pattern it keeps its run
	// parent, so the map keeps reading left to right.
	switch pulse.KindOf(info.PulseParent) {
	case pulse.KindTurn, pulse.KindRPC:
		ev.Parent = info.PulseParent
	}
	if ev.Parent == "" {
		ev.Parent = pulseSessionNodeID
	}
	switch {
	case info.Status.Terminal():
		ev.Phase = pulse.PhaseEnd
		ev = ev.Took(info.Elapsed())
	case info.Turn == 0 && info.Action == "" && info.ToolCalls == 0:
		ev.Phase = pulse.PhaseStart
	default:
		ev.Phase = pulse.PhaseUpdate
	}
	switch info.Status {
	case runs.StatusCompleted:
		ev.Status = pulse.StatusOK
	case runs.StatusFailed:
		ev.Status = pulse.StatusError
	case runs.StatusCancelled:
		ev.Status = pulse.StatusCancelled
	default:
		ev.Status = pulse.StatusRunning
	}
	ev = ev.With("run_kind", string(info.Kind)).With("origin", info.Origin).With("call_id", info.CallID).With("action", info.Action)
	if info.MaxTurns > 0 {
		ev = ev.With("turn", strconv.Itoa(info.Turn)).With("max_turns", strconv.Itoa(info.MaxTurns))
	}
	if info.ToolCalls > 0 {
		ev = ev.With("tool_calls", strconv.Itoa(info.ToolCalls))
	}
	return ev
}

// pulseLLMTap turns the send/recv audit pair into one node per request. The
// audit event carries no request id; it carries the provider, the model and,
// since the adapters pass client.CallerField, the agent run that asked. A
// response is matched to the oldest open request with the same three.
type pulseLLMTap struct {
	bus *pulse.Bus

	mu   sync.Mutex
	seq  uint64
	open map[string][]pulseOpenRequest
}

// pulseOpenRequest is a request waiting for its response. The parent is kept
// because only the send carries the caller, and the end event must hang from
// the same node as the start.
type pulseOpenRequest struct {
	id, parent string
}

func newPulseLLMTap(bus *pulse.Bus) *pulseLLMTap {
	return &pulseLLMTap{bus: bus, open: make(map[string][]pulseOpenRequest)}
}

// pulseLLMAttrs are the audit fields worth showing on a request node. All of
// them are sizes, counts or flags; none is content.
var pulseLLMAttrs = []string{
	"kind", "payload_bytes", "history_len", "tool_count", "cache_markers", "max_tokens",
	"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens", "stop_reason",
}

func (t *pulseLLMTap) observe(ev client.RequestAuditEvent) {
	if !t.bus.Enabled() {
		return
	}
	caller := ev.Fields[client.CallerFieldKey]
	if p := ev.Fields[client.ParentFieldKey]; p != "" {
		caller = p
	}
	out := pulse.Event{
		Kind:   pulse.KindLLM,
		Parent: pulseSessionNodeID,
		Name:   pulseModelNode(ev.Provider, ev.Model),
		TS:     ev.Time,
	}
	if caller != "" {
		out.Parent = caller
	}
	// Send and receive both name their caller, so a response is matched to
	// the request of the same agent on the same model: with parallel workers
	// one agent's response never closes a sibling's node. Requests with no
	// caller (chat, background) still pair oldest first per model.
	key := out.Name + "|" + caller

	t.mu.Lock()
	if ev.Phase == "send" {
		t.seq++
		out.ID = "llm-" + strconv.FormatUint(t.seq, 10)
		t.open[key] = append(t.open[key], pulseOpenRequest{id: out.ID, parent: out.Parent})
	} else if ids := t.open[key]; len(ids) > 0 {
		out.ID, out.Parent = ids[0].id, ids[0].parent
		if len(ids) == 1 {
			delete(t.open, key)
		} else {
			t.open[key] = ids[1:]
		}
	}
	t.mu.Unlock()

	if ev.Phase == "send" {
		out.Phase, out.Status = pulse.PhaseStart, pulse.StatusRunning
	} else {
		if out.ID == "" {
			return // response to a request sent before recording started
		}
		out.Phase = pulse.PhaseEnd
		out = out.Took(ev.Duration)
		switch ev.Status {
		case "success":
			out.Status = pulse.StatusOK
		case "canceled":
			out.Status = pulse.StatusCancelled
		default:
			out.Status = pulse.StatusError
		}
	}
	out = out.With("provider", ev.Provider).With("model", ev.Model)
	for _, k := range pulseLLMAttrs {
		out = out.With(k, ev.Fields[k])
	}
	t.bus.Emit(out)
}

// pulseCallerFrom names the node a request made under ctx hangs from: the
// innermost span or run, else the run attached to ctx.
func pulseCallerFrom(ctx context.Context) string {
	if id := pulse.ParentFrom(ctx); id != "" {
		return id
	}
	return runs.FromContext(ctx).ID()
}
