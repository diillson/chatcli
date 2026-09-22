/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry of the model route: which provider/model pair serves the
 * next request, and what decided it. The session node used to learn its
 * model once, from the boot snapshot, and never again — after "@model use"
 * the graph showed one model on the session card while every request hub
 * lit up under another. Every path that changes the pair reports here:
 * the AI's own @model override, a skill's model hint, /switch, /provider,
 * /reload, /connect, the RPC override and the gateway runtime model. Each
 * agent and chat turn also reports the pair it resolved, so the card is
 * right before the request goes out even when no explicit switch ran.
 *
 * Reports are deduplicated: a turn that changes nothing stays silent.
 */
package cli

import (
	"strings"
	"sync"

	"github.com/diillson/chatcli/pkg/pulse"
)

// Route sources: what decided the pair that serves the next request.
const (
	pulseRouteSession  = "session"  // the pair the user chose (/switch, /provider, env)
	pulseRouteOverride = "override" // the AI's own "@model use"
	pulseRouteSkill    = "skill"    // a skill's model frontmatter or a command hint
)

// pulseRouteState remembers the last pair reported, so the same route is
// not announced again at every turn.
type pulseRouteState struct {
	mu       sync.Mutex
	provider string
	model    string
	source   string
}

// set records the pair and reports whether it differs from the last one.
func (st *pulseRouteState) set(provider, model, source string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.provider == provider && st.model == model && st.source == source {
		return false
	}
	st.provider, st.model, st.source = provider, model, source
	return true
}

// effectivePulseRoute is the pair the next request will use as far as the
// session knows: the @model override when one is set, else the session's
// own pair. A skill hint lives in the agent loop and is reported by the
// turn that resolves it.
func (cli *ChatCLI) effectivePulseRoute() (provider, model, source string) {
	if handle := cli.agentRouteOverrideHandle(); handle != "" {
		// The override is stored qualified ("PROVIDER:model") by Use.
		if p, m, ok := strings.Cut(handle, ":"); ok && p != "" && m != "" {
			return p, m, pulseRouteOverride
		}
	}
	return cli.Provider, cli.Model, pulseRouteSession
}

// pulseRouteChanged reports the effective route after something changed
// it. via names the actor, for the popover and the feed ("@model use",
// "/switch", "gateway", ...).
func (cli *ChatCLI) pulseRouteChanged(via string) {
	if cli == nil || !pulse.Enabled() {
		return
	}
	provider, model, source := cli.effectivePulseRoute()
	cli.pulseNoteRoute(provider, model, source, via)
}

// pulseNoteResolvedRoute reports the pair a turn resolved. source is what
// the hint was (override or skill); when the resolver kept the session's
// client the source is the session, whatever the hint said.
func (cli *ChatCLI) pulseNoteResolvedRoute(res SkillClientResolution, source, via string) {
	if !res.Changed {
		source = pulseRouteSession
	}
	cli.pulseNoteRoute(res.Provider, res.Model, source, via)
}

// pulseNoteRoute emits a session update carrying the pair when it differs
// from the last one reported. Off, or with nothing to say, it returns
// after one atomic load.
func (cli *ChatCLI) pulseNoteRoute(provider, model, source, via string) {
	if cli == nil || !pulse.Enabled() || provider == "" || model == "" {
		return
	}
	if !cli.pulseRoute.set(provider, model, source) {
		return
	}
	pulse.Emit(pulseRouteEvent(pulse.PhaseUpdate, provider, model, source, via))
}

// pulseRouteEvent is the session update that carries a route. The same
// attribute names are used by the boot snapshot, so the page reads one set.
func pulseRouteEvent(phase pulse.Phase, provider, model, source, via string) pulse.Event {
	ev := pulse.Event{Kind: pulse.KindSession, Phase: phase, ID: pulseSessionNodeID, Status: pulse.StatusRunning}
	return ev.With("provider", provider).With("model", model).With("route", source).With("via", via)
}
