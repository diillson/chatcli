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
	bus := pulse.Default()
	bus.RegisterSnapshotter("00-session", cli.pulseSessionSnapshot)
	bus.RegisterSnapshotter("10-runs", pulseRunsSnapshot)
	runs.Default().OnEventKeyed(pulseObserverKey, func(info runs.Info) { bus.Emit(pulseRunEvent(info)) })
	llmTap := newPulseLLMTap(bus)
	client.RegisterRequestAuditorKeyed(pulseObserverKey, llmTap.observe)

	wd, _ := os.Getwd()
	logger := cli.logger
	// The controller outlives any request: bound by process shutdown, not
	// by the constructor's context.
	cli.pulse = pulse.Start(context.WithoutCancel(ctx), pulse.Options{
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
	runs.Default().OnEventKeyed(pulseObserverKey, nil)
	client.RegisterRequestAuditorKeyed(pulseObserverKey, nil)
	bus := pulse.Default()
	bus.RegisterSnapshotter("00-session", nil)
	bus.RegisterSnapshotter("10-runs", nil)
	bus.RegisterSnapshotter(pulseMCPSnapshotKey, nil)
	cli.pulse.Close()
	cli.pulse = nil
}

// pulseSessionSnapshot describes the process itself: the root of its graph.
func (cli *ChatCLI) pulseSessionSnapshot() []pulse.Event {
	ev := pulse.Event{
		Kind:   pulse.KindSession,
		Phase:  pulse.PhaseStart,
		ID:     pulseSessionNodeID,
		Name:   "chatcli",
		Status: pulse.StatusRunning,
	}
	return []pulse.Event{ev.With("provider", cli.Provider).With("model", cli.Model)}
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
// audit event carries no request id, so an open request is matched to its
// response by provider and model, oldest first; with parallel workers on the
// same model a response may close a sibling's node, which is harmless — the
// duration shown is the one the response itself reports.
type pulseLLMTap struct {
	bus *pulse.Bus

	mu   sync.Mutex
	seq  uint64
	open map[string][]string
}

func newPulseLLMTap(bus *pulse.Bus) *pulseLLMTap {
	return &pulseLLMTap{bus: bus, open: make(map[string][]string)}
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
	key := ev.Provider + ":" + ev.Model
	out := pulse.Event{
		Kind:   pulse.KindLLM,
		Parent: pulseSessionNodeID,
		Name:   key,
		TS:     ev.Time,
	}

	t.mu.Lock()
	if ev.Phase == "send" {
		t.seq++
		out.ID = "llm-" + strconv.FormatUint(t.seq, 10)
		t.open[key] = append(t.open[key], out.ID)
	} else if ids := t.open[key]; len(ids) > 0 {
		out.ID = ids[0]
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
