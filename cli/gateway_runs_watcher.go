/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * gateway_runs_watcher — per-agent progress lines for gateway turns.
 *
 * The events sink covers the orchestrator's own tool calls, but dispatched
 * workers/subagents report progress only to the run registry (their tool
 * calls happen inside their own ReAct loops). This watcher polls the
 * registry during a gateway turn and emits a line whenever an agent's
 * state changes — the chat-platform equivalent of the terminal's live
 * dispatch panel.
 */
package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/i18n"
)

// gatewayRunsWatchInterval is how often the watcher samples the registry.
// The runner batches progress flushes at ~3s, so 2s sampling adds no spam.
const gatewayRunsWatchInterval = 2 * time.Second

// watchRunsProgress emits a progress line per agent-run state change until
// stopped. Returns a stop func that blocks until the watcher goroutine
// exits (so emit is never called after the turn finishes).
func watchRunsProgress(ctx context.Context, reg *runs.Registry, emit func(string), interval time.Duration) func() {
	if reg == nil || emit == nil {
		return func() {}
	}
	if interval <= 0 {
		interval = gatewayRunsWatchInterval
	}
	watchCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)
		lastLine := make(map[string]string) // run ID → last emitted line
		live := make(map[string]bool)       // run IDs seen active
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		sample := func() {
			active := reg.Active()
			seen := make(map[string]bool, len(active))
			for _, info := range active {
				if info.Kind == runs.KindOrchestrator {
					continue // the sink already narrates the orchestrator
				}
				seen[info.ID] = true
				live[info.ID] = true
				line := formatGatewayRunLine(info)
				if lastLine[info.ID] != line {
					lastLine[info.ID] = line
					emit(line)
				}
			}
			// Runs that were live and vanished from the active set finished:
			// emit their terminal state once.
			for id := range live {
				if seen[id] {
					continue
				}
				delete(live, id)
				if info, ok := reg.Get(id); ok && info.Status.Terminal() {
					emit(formatGatewayRunFinal(info))
				}
				delete(lastLine, id)
			}
		}

		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				sample()
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

// formatGatewayRunLine renders a live run as one chat progress line.
func formatGatewayRunLine(info runs.Info) string {
	line := fmt.Sprintf("🤖 [%s]", info.Agent)
	if info.Turn > 0 && info.MaxTurns > 0 {
		line += " " + i18n.T("agents.turn", info.Turn, info.MaxTurns)
	}
	if info.Action != "" {
		line += " · " + oneLine(info.Action, 60)
	} else if info.Task != "" {
		line += " · " + oneLine(info.Task, 60)
	}
	return line
}

// formatGatewayRunFinal renders a finished run's terminal state.
func formatGatewayRunFinal(info runs.Info) string {
	icon := "✓"
	switch info.Status {
	case runs.StatusFailed:
		icon = "✗"
	case runs.StatusCancelled:
		icon = "⊘"
	}
	return fmt.Sprintf("%s [%s] %s (%s)", icon, info.Agent,
		i18n.T("agents.status."+string(info.Status)),
		info.Elapsed().Round(time.Second))
}
