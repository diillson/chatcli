/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * runs_hub_bridge — mirrors the agent run registry through the Conversation
 * Hub's SQLite store, giving /agents and @agents cross-process sight: a REPL
 * can watch runs executing inside the gateway daemon (and vice versa), and
 * request their cancellation.
 *
 * Writer side: an OnEvent observer marks runs dirty and a 1s flusher upserts
 * their JSON snapshots into the agent_runs table (see hub.AgentRunStore for
 * why runs are upserted, not appended). Every flush also re-upserts the
 * local active set, so updated_at doubles as a liveness heartbeat.
 *
 * Reader side: the bridge publishes a process-wide provider that lists runs
 * mirrored by OTHER instances, annotated with staleness (heartbeat silence),
 * and forwards cancel requests by flagging the row — the owning process
 * observes the flag on its next flush tick and cancels locally, since only
 * it holds the context.CancelFunc.
 */
package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/server/hub"
	"go.uber.org/zap"
)

const (
	// runsHubOpTimeout bounds each hub operation so a slow disk can never
	// stall the flusher (writes happen off the agent turn path).
	runsHubOpTimeout = 2 * time.Second

	// runsHubStaleAfter is how long a "running" mirrored run may go without
	// a heartbeat before readers flag it as stale (owner likely died).
	runsHubStaleAfter = 15 * time.Second

	// runsHubGCInterval is how often the writer sweeps the mirror table.
	runsHubGCInterval = 10 * time.Minute
	// runsHubTerminalRetention keeps finished runs visible cross-process
	// long enough to inspect outcomes without growing the table forever.
	runsHubTerminalRetention = 24 * time.Hour
	// runsHubRunningRetention removes "running" rows whose heartbeat
	// stopped — their process died without finalizing the run.
	runsHubRunningRetention = 5 * time.Minute
)

// runsBridgeActive guards against double-starting the bridge in one process.
var runsBridgeActive atomic.Bool

// hubRunsEnabled resolves the CHATCLI_HUB_RUNS kill switch (default on —
// the bridge only starts when the hub itself is enabled).
func hubRunsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_HUB_RUNS"))) {
	case "false", "0", "off":
		return false
	}
	return true
}

// remoteAgentRun is one run mirrored by another process, as consumed by
// /agents and the @agents adapter.
type remoteAgentRun struct {
	Info  runs.Info
	Stale bool // running, but heartbeat silent past runsHubStaleAfter
}

// remoteRunsProvider lists mirrored runs owned by other instances and
// forwards cross-process cancel requests. Set by the bridge; nil when the
// hub (or the runs mirror) is disabled.
type remoteRunsProvider struct {
	list   func() []remoteAgentRun
	cancel func(runID string) bool
}

var (
	remoteRunsMu     sync.RWMutex
	remoteRunsSource *remoteRunsProvider
)

// setRemoteRunsProvider installs (or clears, with nil) the process-wide
// remote runs source.
func setRemoteRunsProvider(p *remoteRunsProvider) {
	remoteRunsMu.Lock()
	remoteRunsSource = p
	remoteRunsMu.Unlock()
}

// listRemoteAgentRuns returns runs mirrored by other processes (empty when
// the hub runs mirror is off).
func listRemoteAgentRuns() []remoteAgentRun {
	remoteRunsMu.RLock()
	p := remoteRunsSource
	remoteRunsMu.RUnlock()
	if p == nil || p.list == nil {
		return nil
	}
	return p.list()
}

// requestRemoteAgentRunCancel flags a mirrored run for cancellation by its
// owning process. Returns false when no provider is active or the run is
// unknown/terminal.
func requestRemoteAgentRunCancel(runID string) bool {
	remoteRunsMu.RLock()
	p := remoteRunsSource
	remoteRunsMu.RUnlock()
	if p == nil || p.cancel == nil {
		return false
	}
	return p.cancel(runID)
}

// startRunsHubBridge wires the run registry to the hub store and starts the
// flusher. Returns a stop func (never nil). No-ops when the store lacks the
// AgentRunStore capability, the mirror is disabled, or a bridge is already
// active in this process.
func startRunsHubBridge(ctx context.Context, store hub.Store, reg *runs.Registry, logger *zap.Logger) func() {
	if store == nil || reg == nil || !hubRunsEnabled() {
		return func() {}
	}
	runStore, ok := store.(hub.AgentRunStore)
	if !ok {
		return func() {}
	}
	if !runsBridgeActive.CompareAndSwap(false, true) {
		return func() {}
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	bridgeCtx, cancel := context.WithCancel(ctx)

	// Dirty set: runs touched since the last flush. The observer only
	// records — all I/O happens on the flusher goroutine.
	var (
		dirtyMu sync.Mutex
		dirty   = make(map[string]runs.Info)
	)
	reg.OnEvent(func(info runs.Info) {
		dirtyMu.Lock()
		dirty[info.ID] = info
		dirtyMu.Unlock()
	})

	setRemoteRunsProvider(&remoteRunsProvider{
		list: func() []remoteAgentRun {
			opCtx, opCancel := context.WithTimeout(bridgeCtx, runsHubOpTimeout)
			defer opCancel()
			recs, err := runStore.ListAgentRuns(opCtx)
			if err != nil {
				logger.Debug("runs hub: list failed", zap.Error(err))
				return nil
			}
			local := reg.Instance()
			out := make([]remoteAgentRun, 0, len(recs))
			for _, rec := range recs {
				if rec.Instance == local {
					continue
				}
				var info runs.Info
				if err := json.Unmarshal([]byte(rec.Payload), &info); err != nil || info.ID == "" {
					continue
				}
				stale := !info.Status.Terminal() && time.Since(rec.UpdatedAt) > runsHubStaleAfter
				out = append(out, remoteAgentRun{Info: info, Stale: stale})
			}
			return out
		},
		cancel: func(runID string) bool {
			opCtx, opCancel := context.WithTimeout(bridgeCtx, runsHubOpTimeout)
			defer opCancel()
			ok, err := runStore.RequestAgentRunCancel(opCtx, runID)
			if err != nil {
				logger.Warn("runs hub: cancel request failed", zap.String("run_id", runID), zap.Error(err))
				return false
			}
			return ok
		},
	})

	flush := func() {
		// Snapshot dirty runs, then merge the live set on top: active runs
		// are re-upserted every tick so updated_at works as a heartbeat.
		dirtyMu.Lock()
		batch := dirty
		dirty = make(map[string]runs.Info)
		dirtyMu.Unlock()
		for _, info := range reg.Active() {
			batch[info.ID] = info
		}

		for _, info := range batch {
			payload, err := json.Marshal(info)
			if err != nil {
				continue
			}
			opCtx, opCancel := context.WithTimeout(bridgeCtx, runsHubOpTimeout)
			err = runStore.UpsertAgentRun(opCtx, hub.AgentRunRecord{
				RunID:    info.ID,
				Instance: info.Instance,
				Origin:   info.Origin,
				Status:   string(info.Status),
				Payload:  string(payload),
				EndedAt:  info.EndedAt,
			})
			opCancel()
			if err != nil {
				logger.Debug("runs hub: upsert failed", zap.String("run_id", info.ID), zap.Error(err))
			}
		}

		// Honor cancel requests targeting runs owned by this process.
		opCtx, opCancel := context.WithTimeout(bridgeCtx, runsHubOpTimeout)
		recs, err := runStore.ListAgentRuns(opCtx)
		opCancel()
		if err != nil {
			return
		}
		local := reg.Instance()
		for _, rec := range recs {
			if rec.Instance == local && rec.CancelRequested {
				if reg.Cancel(rec.RunID) {
					logger.Info("runs hub: cross-process cancel honored", zap.String("run_id", rec.RunID))
				}
			}
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(squadMailPollInterval())
		defer ticker.Stop()
		gcTicker := time.NewTicker(runsHubGCInterval)
		defer gcTicker.Stop()

		gc := func() {
			opCtx, opCancel := context.WithTimeout(bridgeCtx, runsHubOpTimeout)
			defer opCancel()
			if n, err := runStore.PurgeAgentRuns(opCtx, runsHubTerminalRetention, runsHubRunningRetention); err == nil && n > 0 {
				logger.Debug("runs hub: purged stale mirrored runs", zap.Int64("count", n))
			}
		}
		gc()
		for {
			select {
			case <-bridgeCtx.Done():
				flush() // final flush so terminal states land before shutdown
				return
			case <-ticker.C:
				flush()
			case <-gcTicker.C:
				gc()
			}
		}
	}()

	return func() {
		cancel()
		<-done
		reg.OnEvent(nil)
		setRemoteRunsProvider(nil)
		runsBridgeActive.Store(false)
	}
}
