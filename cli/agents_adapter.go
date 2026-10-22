/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent/runs"
)

// liveAgentsAdapter implements plugins.AgentsAdapter over the process-wide
// agent run registry. Output is compact English key=value text — it is
// consumed by the LLM, not rendered to the user (the /agents command has its
// own i18n rendering).
type liveAgentsAdapter struct {
	reg *runs.Registry
}

// newLiveAgentsAdapter builds the adapter over a registry (nil = Default()).
func newLiveAgentsAdapter(reg *runs.Registry) *liveAgentsAdapter {
	if reg == nil {
		reg = runs.Default()
	}
	return &liveAgentsAdapter{reg: reg}
}

// agentsAdapterRecentN caps how many finished runs List reports to the model.
const agentsAdapterRecentN = 10

// List implements plugins.AgentsAdapter.
func (a *liveAgentsAdapter) List() (string, error) {
	active := a.reg.Active()
	recent := a.reg.Recent(agentsAdapterRecentN)

	var b strings.Builder
	fmt.Fprintf(&b, "ACTIVE RUNS (%d):\n", len(active))
	if len(active) == 0 {
		b.WriteString("(none)\n")
	}
	for _, info := range active {
		b.WriteString(formatRunLine(info))
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nRECENT FINISHED (last %d):\n", len(recent))
	if len(recent) == 0 {
		b.WriteString("(none)\n")
	}
	for _, info := range recent {
		b.WriteString(formatRunLine(info))
		b.WriteString("\n")
	}
	if remote := listRemoteAgentRuns(); len(remote) > 0 {
		fmt.Fprintf(&b, "\nOTHER PROCESSES (%d, via hub):\n", len(remote))
		for _, r := range remote {
			b.WriteString(formatRunLine(r.Info))
			fmt.Fprintf(&b, " instance=%s", r.Info.Instance)
			if r.Stale {
				b.WriteString(" stale=true")
			}
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

// Show implements plugins.AgentsAdapter.
func (a *liveAgentsAdapter) Show(id string) (string, error) {
	info, ok := a.reg.Get(id)
	if !ok {
		if r, found := findRemoteAgentRun(id); found {
			line := formatRunLine(r.Info) + fmt.Sprintf(" instance=%s", r.Info.Instance)
			if r.Stale {
				line += " stale=true (owner heartbeat silent — its process may have died)"
			}
			return line + "\n", nil
		}
		return "", fmt.Errorf("@agents show: no run with id %q (use list to see IDs)", id)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "id=%s\nkind=%s\nagent=%s\nstatus=%s\n", info.ID, info.Kind, info.Agent, info.Status)
	if info.ParentID != "" {
		fmt.Fprintf(&b, "parent=%s\n", info.ParentID)
	}
	if info.Origin != "" {
		fmt.Fprintf(&b, "origin=%s\n", info.Origin)
	}
	if info.Turn > 0 {
		fmt.Fprintf(&b, "turn=%d/%d\n", info.Turn, info.MaxTurns)
	}
	if info.Action != "" {
		fmt.Fprintf(&b, "current_action=%s\n", info.Action)
	}
	fmt.Fprintf(&b, "tool_calls=%d\nelapsed=%s\n", info.ToolCalls, info.Elapsed().Round(time.Second))
	if info.Err != "" {
		fmt.Fprintf(&b, "error=%s\n", info.Err)
	}
	fmt.Fprintf(&b, "task=%s\n", strings.TrimSpace(info.Task))
	if children := a.reg.Children(info.ID); len(children) > 0 {
		fmt.Fprintf(&b, "live_children=%d\n", len(children))
		for _, child := range children {
			b.WriteString("  " + formatRunLine(child) + "\n")
		}
	}
	return b.String(), nil
}

// Cancel implements plugins.AgentsAdapter.
func (a *liveAgentsAdapter) Cancel(id string) (string, error) {
	if a.reg.Cancel(id) {
		return fmt.Sprintf("cancellation requested for %s (the run ends when it observes the signal)", id), nil
	}
	if _, ok := a.reg.Get(id); ok {
		return "", fmt.Errorf("@agents cancel: run %q already finished", id)
	}
	if r, found := findRemoteAgentRun(id); found {
		if !r.Info.Status.Terminal() && requestRemoteAgentRunCancel(id) {
			return fmt.Sprintf("cancellation forwarded to the owning process for %s (honored on its next sync tick)", id), nil
		}
		return "", fmt.Errorf("@agents cancel: run %q already finished", id)
	}
	return "", fmt.Errorf("@agents cancel: no run with id %q (use list to see IDs)", id)
}

// agentsWaitPoll is how often Wait re-reads the registry. Observers would
// be cheaper per tick, but polling also covers runs mirrored from other
// processes (the hub has no event stream) and keeps the wait trivially
// cancellable.
const agentsWaitPoll = 250 * time.Millisecond

// Model-facing wait outcomes (English on purpose: tool output for the LLM).
const (
	agentsMsgWaitTimedOut = "WAIT TIMED OUT after %s: %s is still %s. Current state:\n%sWait again or cancel it."
	agentsMsgRunFinished  = "RUN %s FINISHED status=%s after %s%s\n%s"
)

// Wait implements plugins.AgentsWatcher: blocks until the run is terminal,
// ctx ends or timeout elapses. A finished run returns its Show detail; a
// wait that runs out returns the current state with a hint, not an error.
func (a *liveAgentsAdapter) Wait(ctx context.Context, id string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	lookup := func() (runs.Info, bool, bool) {
		if info, ok := a.reg.Get(id); ok {
			return info, false, true
		}
		if r, found := findRemoteAgentRun(id); found {
			return r.Info, true, true
		}
		return runs.Info{}, false, false
	}
	info, remote, ok := lookup()
	if !ok {
		return "", fmt.Errorf("@agents wait: no run with id %q (use list to see IDs)", id)
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(agentsWaitPoll)
	defer tick.Stop()
	started := time.Now()
	for !info.Status.Terminal() {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("@agents wait: interrupted after %s while %s was still %s: %w",
				time.Since(started).Round(time.Second), id, info.Status, ctx.Err())
		case <-deadline.C:
			detail, _ := a.Show(id)
			return fmt.Sprintf(agentsMsgWaitTimedOut, timeout, id, info.Status, detail), nil
		case <-tick.C:
		}
		if info, remote, ok = lookup(); !ok {
			return "", fmt.Errorf("@agents wait: run %q disappeared from the registry while waiting", id)
		}
	}
	detail, err := a.Show(id)
	if err != nil {
		return "", err
	}
	scope := ""
	if remote {
		scope = " (mirrored from another process)"
	}
	return fmt.Sprintf(agentsMsgRunFinished, id, info.Status, info.Elapsed().Round(time.Second), scope, detail), nil
}

// Transcript implements plugins.AgentsWatcher: the last tail entries of the
// run's activity log (turns, actions, final status, output excerpt). The
// registry keeps that log, not the run's LLM conversation. A run owned by
// another process is read from the tail its owner mirrors through the hub
// (the newest runsHubActivityTail entries, refreshed as the run advances).
func (a *liveAgentsAdapter) Transcript(id string, tail int) (string, error) {
	log, ok := a.reg.ActivityLog(id)
	info, _ := a.reg.Get(id)
	scope := ""
	if !ok {
		remote, found := findRemoteAgentRun(id)
		if !found {
			return "", fmt.Errorf("@agents transcript: no run with id %q (use list to see IDs)", id)
		}
		entries, mirrored := remoteAgentRunActivity(id)
		if !mirrored {
			return "", fmt.Errorf("@agents transcript: run %q belongs to another process (%s) and has mirrored no activity yet; retry in a moment or use show", id, remoteRunOrigin(remote))
		}
		log, info = entries, remote.Info
		scope = fmt.Sprintf(", mirrored from %s via the hub (newest %d entries at most)", remoteRunOrigin(remote), runsHubActivityTail)
		if remote.Stale {
			scope += ", owner heartbeat silent"
		}
	}
	total := len(log)
	if tail > 0 && tail < total {
		log = log[total-tail:]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ACTIVITY LOG %s status=%s (%d of %d entries, oldest first%s; the runtime keeps turns/actions/status, not the LLM conversation):\n",
		id, info.Status, len(log), total, scope)
	if total == 0 {
		b.WriteString("(nothing recorded yet)\n")
	}
	for _, e := range log {
		fmt.Fprintf(&b, "%s %s\n", e.At.Format("15:04:05"), e.Text)
	}
	return b.String(), nil
}

// remoteRunOrigin names the process that mirrored a run: its origin
// ("gateway", "scheduler"…), falling back to its instance token.
func remoteRunOrigin(r remoteAgentRun) string {
	if r.Info.Origin != "" {
		return r.Info.Origin
	}
	if r.Info.Instance != "" {
		return "instance " + r.Info.Instance
	}
	return "another process"
}

// formatRunLine renders one run as a single compact key=value line.
func formatRunLine(info runs.Info) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s kind=%s agent=%s status=%s", info.ID, info.Kind, info.Agent, info.Status)
	if info.Turn > 0 && info.Status == runs.StatusRunning {
		fmt.Fprintf(&b, " turn=%d/%d", info.Turn, info.MaxTurns)
	}
	if info.Action != "" && info.Status == runs.StatusRunning {
		fmt.Fprintf(&b, " action=%q", info.Action)
	}
	if info.ToolCalls > 0 {
		fmt.Fprintf(&b, " tools=%d", info.ToolCalls)
	}
	fmt.Fprintf(&b, " elapsed=%s", info.Elapsed().Round(time.Second))
	if info.ParentID != "" {
		fmt.Fprintf(&b, " parent=%s", info.ParentID)
	}
	if info.Err != "" {
		fmt.Fprintf(&b, " error=%q", truncateForUI(info.Err, 80))
	}
	fmt.Fprintf(&b, " task=%q", truncateForUI(info.Task, 100))
	return b.String()
}
