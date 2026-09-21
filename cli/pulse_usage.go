/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for usage and cost. RecordRealUsageIn is the funnel every
 * real usage report converges on — main turns, squad workers, background
 * jobs — already normalized across the cache schemas of the fifteen providers
 * and already priced. Reporting from there puts on each model node what the
 * call consumed and what the model has cost so far, and on the session node
 * what the whole session has cost, with the same numbers /cost shows.
 *
 * The request taps cannot carry this: the request chokepoint never sees the
 * provider's usage, only sizes known before the call.
 */
package cli

import (
	"strconv"
	"strings"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseModelNode names the dashboard hub of a model. The request taps and
// the usage reports must agree on it, or one model would show as two nodes;
// providers are upper-cased because call sites differ in case.
func pulseModelNode(provider, model string) string {
	return strings.ToUpper(provider) + ":" + model
}

// formatPulseUSD renders a cost with enough digits to be non-zero for a
// cheap call and without noise for an expensive session.
func formatPulseUSD(usd float64) string {
	switch {
	case usd <= 0:
		return ""
	case usd < 0.01:
		return "$" + strconv.FormatFloat(usd, 'f', 4, 64)
	default:
		return "$" + strconv.FormatFloat(usd, 'f', 2, 64)
	}
}

// pulseUsageEventsLocked builds the usage report of one call: an update on
// the model node and one on the session node. Caller holds ct.mu; the events
// are emitted after it is released. Returns nil while the dashboard is off.
func (ct *CostTracker) pulseUsageEventsLocked(lane UsageLane, rec *ModelUsageRecord, inputTokens int, usage *models.UsageInfo) []pulse.Event {
	if !pulse.Enabled() || rec == nil || usage == nil {
		return nil
	}
	itoa := func(n int64) string {
		if n <= 0 {
			return ""
		}
		return strconv.FormatInt(n, 10)
	}
	model := pulse.Event{
		Kind:   pulse.KindLLM,
		Phase:  pulse.PhaseUpdate,
		ID:     "usage:" + rec.Provider + ":" + rec.Model,
		Parent: pulseSessionNodeID,
		Name:   pulseModelNode(rec.Provider, rec.Model),
		Status: pulse.StatusOK,
	}
	model = model.With("lane", string(lane)).
		With("last_input_tokens", itoa(int64(inputTokens))).
		With("last_output_tokens", itoa(int64(usage.CompletionTokens))).
		With("last_cache_read", itoa(int64(usage.CacheReadInputTokens))).
		With("last_cache_write", itoa(int64(usage.CacheCreationInputTokens))).
		With("tokens", itoa(recordInputTokens(rec)+rec.CompletionTokens)).
		With("cache_read", itoa(rec.CacheReadTokens)).
		With("requests", strconv.Itoa(rec.Requests)).
		With("cost", formatPulseUSD(rec.TotalCostUSD))

	session := pulse.Event{
		Kind:   pulse.KindSession,
		Phase:  pulse.PhaseUpdate,
		ID:     pulseSessionNodeID,
		Status: pulse.StatusRunning,
	}
	session = session.With("cost", formatPulseUSD(ct.totalCostUSD)).
		With("tokens", itoa(ct.totalInputTokens+ct.totalCompletionTokens)).
		With("requests", strconv.Itoa(ct.totalRequests))
	return []pulse.Event{model, session}
}

// pulseContextWindow reports how full the context window is, from the same
// projection the turn footer prints (it may exceed 100: that is the signal
// the next turn compacts).
func pulseContextWindow(pct, window int) {
	if !pulse.Enabled() || window <= 0 {
		return
	}
	ev := pulse.Event{Kind: pulse.KindSession, Phase: pulse.PhaseUpdate, ID: pulseSessionNodeID, Status: pulse.StatusRunning}
	pulse.Emit(ev.With("ctx", strconv.Itoa(pct)+"%").With("window", strconv.Itoa(window)))
}
