/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/cli/coder"
	"go.uber.org/zap"
)

// permRecorder is a sinkRecorder that also implements PermissionRequester,
// with a scripted decision.
type permRecorder struct {
	sinkRecorder
	grant   bool
	err     error
	asked   []agentevents.ToolCall
	reasons []string
}

func (p *permRecorder) RequestPermission(tc agentevents.ToolCall, reason string) (bool, error) {
	p.asked = append(p.asked, tc)
	p.reasons = append(p.reasons, reason)
	return p.grant, p.err
}

func newUnattendedAgent(sink agentevents.Sink) (*AgentMode, *ChatCLI) {
	c := &ChatCLI{unattended: true}
	a := NewAgentMode(c, zap.NewNop())
	a.events = sink
	return a, c
}

// TestUnattendedAskBlocked_DeniedByClient: with a PermissionRequester
// installed (ACP), a policy "ask" rule must surface the client's dialog —
// and a denial must block the action, tell the model, and close the
// tool_call for the client.
func TestUnattendedAskBlocked_DeniedByClient(t *testing.T) {
	rec := &permRecorder{grant: false}
	a, c := newUnattendedAgent(rec)

	blocked := a.unattendedAskBlocked("@coder", `{"cmd":"exec","args":{"cmd":"docker rm x"}}`, nil, func(string) {})
	if !blocked {
		t.Fatal("denied ask rule must block the action")
	}
	if len(rec.asked) != 1 {
		t.Fatalf("requester consulted %d times, want 1", len(rec.asked))
	}
	found := false
	for _, m := range c.history {
		if strings.Contains(m.Content, "DENIED") {
			found = true
		}
	}
	if !found {
		t.Fatal("denial must reach the model through history so it can replan")
	}
	if len(rec.ends) != 1 {
		t.Fatalf("expected 1 blocked tool_call event for the client, got %d", len(rec.ends))
	}
	if !rec.ends[0].IsError {
		t.Error("blocked tool_call must be reported as error")
	}
}

// TestUnattendedAskBlocked_AllowedByClient: an explicit approval in the
// client dialog runs the action.
func TestUnattendedAskBlocked_AllowedByClient(t *testing.T) {
	rec := &permRecorder{grant: true}
	a, c := newUnattendedAgent(rec)

	blocked := a.unattendedAskBlocked("@coder", `{"cmd":"write","args":{"file":"x"}}`, nil, func(string) {})
	if blocked {
		t.Fatal("approved ask rule must not block")
	}
	if len(rec.asked) != 1 {
		t.Fatalf("requester consulted %d times, want 1", len(rec.asked))
	}
	if len(c.history) != 0 {
		t.Fatalf("approval must not pollute history, got %d messages", len(c.history))
	}
}

// TestUnattendedAskBlocked_NoRequester: without any requester (gateway
// daemon, MCP client without elicitation) the historical unattended contract
// holds — ask auto-approves.
func TestUnattendedAskBlocked_NoRequester(t *testing.T) {
	a, c := newUnattendedAgent(&sinkRecorder{}) // sink without PermissionRequester
	if a.unattendedAskBlocked("@coder", `{"cmd":"exec"}`, nil, func(string) {}) {
		t.Fatal("without a requester, ask must keep the legacy auto-approve")
	}
	if len(c.history) != 0 {
		t.Fatal("legacy auto-approve must not touch history")
	}
}

// TestUnattendedAskBlocked_UnsupportedClientFallsBack: a client that answers
// "method not found" (wrapped/minimal ACP clients) falls back to the legacy
// auto-approve instead of bricking every ask-gated action.
func TestUnattendedAskBlocked_UnsupportedClientFallsBack(t *testing.T) {
	rec := &permRecorder{err: agentevents.ErrPermissionUnsupported}
	a, c := newUnattendedAgent(rec)
	if a.unattendedAskBlocked("@coder", `{"cmd":"exec"}`, nil, func(string) {}) {
		t.Fatal("unsupported permission method must fall back to legacy auto-approve")
	}
	if len(c.history) != 0 {
		t.Fatal("fallback must not touch history")
	}
}

// TestUnattendedAskBlocked_TransportErrorDenies: any other requester failure
// denies (fail-safe) — the user may have wanted to say no.
func TestUnattendedAskBlocked_TransportErrorDenies(t *testing.T) {
	rec := &permRecorder{err: errFixed("client disconnected")}
	a, _ := newUnattendedAgent(rec)
	if !a.unattendedAskBlocked("@coder", `{"cmd":"exec"}`, nil, func(string) {}) {
		t.Fatal("transport error must deny fail-safe")
	}
}

// TestRequestActionPermission_UnsupportedNotAsked: ErrPermissionUnsupported
// means "no dialog exists", which callers must treat as not-consulted — the
// dangerous-exec guard then blocks in-band exactly like the MCP contract.
func TestRequestActionPermission_UnsupportedNotAsked(t *testing.T) {
	rec := &permRecorder{err: agentevents.ErrPermissionUnsupported}
	a, _ := newUnattendedAgent(rec)
	allowed, asked := a.requestActionPermission(agentevents.ToolCall{Name: "@coder"}, "r")
	if allowed || asked {
		t.Fatalf("unsupported must report not-consulted, got allowed=%v asked=%v", allowed, asked)
	}
}

// decisionRecorder is a sinkRecorder that implements PermissionDecider with a
// scripted decision, recording every request it receives.
type decisionRecorder struct {
	sinkRecorder
	decision agentevents.PermissionDecision
	err      error
	reqs     []agentevents.PermissionRequest
}

func (d *decisionRecorder) RequestPermissionDecision(req agentevents.PermissionRequest) (agentevents.PermissionDecision, error) {
	d.reqs = append(d.reqs, req)
	return d.decision, d.err
}

// newSandboxedPolicyManager isolates the policy file under a temp HOME so
// persistence assertions never touch the real ~/.chatcli/coder_policy.json.
func newSandboxedPolicyManager(t *testing.T) *coder.PolicyManager {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	pm, err := coder.NewPolicyManager(zap.NewNop())
	if err != nil {
		t.Fatalf("NewPolicyManager: %v", err)
	}
	return pm
}

// hasRule reports whether the manager holds an exact pattern→action rule.
func hasRule(pm *coder.PolicyManager, pattern string, action coder.Action) bool {
	for _, r := range pm.RulesSnapshot() {
		if r.Pattern == pattern && r.Action == action {
			return true
		}
	}
	return false
}

// TestUnattendedAskBlocked_AllowAlwaysPersistsRule: "always allow" in the
// client dialog must reach terminal parity — run the action AND persist the
// suggested pattern as an allow rule.
func TestUnattendedAskBlocked_AllowAlwaysPersistsRule(t *testing.T) {
	pm := newSandboxedPolicyManager(t)
	rec := &decisionRecorder{decision: agentevents.PermissionAllowAlways}
	a, c := newUnattendedAgent(rec)

	blocked := a.unattendedAskBlocked("@coder", `{"cmd":"write","args":{"file":"x"}}`, pm, func(string) {})
	if blocked {
		t.Fatal("allow_always must run the action")
	}
	if !hasRule(pm, "@coder write", coder.ActionAllow) {
		t.Fatalf("allow rule not persisted, rules: %+v", pm.RulesSnapshot())
	}
	if len(c.history) != 0 {
		t.Fatalf("approval must not pollute history, got %d messages", len(c.history))
	}
	if len(rec.reqs) != 1 || !rec.reqs[0].OfferAlways {
		t.Fatalf("dialog must offer the always choices for a persistable pattern, reqs: %+v", rec.reqs)
	}
}

// TestUnattendedAskBlocked_DenyAlwaysPersistsRuleAndBlocks: "always reject"
// must block the action, tell the model, and persist the deny rule so the
// next attempt never reaches a dialog.
func TestUnattendedAskBlocked_DenyAlwaysPersistsRuleAndBlocks(t *testing.T) {
	pm := newSandboxedPolicyManager(t)
	rec := &decisionRecorder{decision: agentevents.PermissionDenyAlways}
	a, c := newUnattendedAgent(rec)

	blocked := a.unattendedAskBlocked("@coder", `{"cmd":"delete","args":{"file":"x"}}`, pm, func(string) {})
	if !blocked {
		t.Fatal("deny_always must block the action")
	}
	if !hasRule(pm, "@coder delete", coder.ActionDeny) {
		t.Fatalf("deny rule not persisted, rules: %+v", pm.RulesSnapshot())
	}
	found := false
	for _, m := range c.history {
		if strings.Contains(m.Content, "DENIED") {
			found = true
		}
	}
	if !found {
		t.Fatal("denial must reach the model through history so it can replan")
	}
	if len(rec.ends) != 1 || !rec.ends[0].IsError {
		t.Fatalf("blocked tool_call must close as error for the client, ends: %+v", rec.ends)
	}
}

// TestUnattendedAskBlocked_ExecNeverOffersAlways: exec has no persistable
// pattern (terminal hides allow/deny-always for it on purpose) — the client
// dialog must not offer privilege the interactive flow denies, and a stray
// allow_always answer must degrade to allow-once with no rule written.
func TestUnattendedAskBlocked_ExecNeverOffersAlways(t *testing.T) {
	pm := newSandboxedPolicyManager(t)
	before := len(pm.RulesSnapshot())
	rec := &decisionRecorder{decision: agentevents.PermissionAllowAlways}
	a, _ := newUnattendedAgent(rec)

	blocked := a.unattendedAskBlocked("@coder", `{"cmd":"exec","args":{"cmd":"docker rm x"}}`, pm, func(string) {})
	if blocked {
		t.Fatal("allow decision must run the action")
	}
	if len(rec.reqs) != 1 || rec.reqs[0].OfferAlways {
		t.Fatalf("exec must never offer the always choices, reqs: %+v", rec.reqs)
	}
	if len(pm.RulesSnapshot()) != before {
		t.Fatalf("no rule may be persisted for exec, rules: %+v", pm.RulesSnapshot())
	}
}

// TestUnattendedAskBlocked_DeciderErrorContracts: the decision surface must
// honor the same error contracts as the boolean one — unsupported falls back
// to legacy auto-approve, transport errors deny fail-safe.
func TestUnattendedAskBlocked_DeciderErrorContracts(t *testing.T) {
	pm := newSandboxedPolicyManager(t)

	uns := &decisionRecorder{err: agentevents.ErrPermissionUnsupported}
	a, c := newUnattendedAgent(uns)
	if a.unattendedAskBlocked("@coder", `{"cmd":"write"}`, pm, func(string) {}) {
		t.Fatal("unsupported decider must fall back to legacy auto-approve")
	}
	if len(c.history) != 0 {
		t.Fatal("fallback must not touch history")
	}

	boom := &decisionRecorder{err: errFixed("client disconnected")}
	a2, _ := newUnattendedAgent(boom)
	if !a2.unattendedAskBlocked("@coder", `{"cmd":"write"}`, pm, func(string) {}) {
		t.Fatal("transport error must deny fail-safe")
	}
}

// TestRequestActionPermission_LegacyRequesterStillServes: sinks implementing
// only the boolean PermissionRequester (older frontends) must keep working
// through the decision bridge unchanged.
func TestRequestActionPermission_LegacyRequesterStillServes(t *testing.T) {
	pm := newSandboxedPolicyManager(t)
	before := len(pm.RulesSnapshot())
	rec := &permRecorder{grant: true}
	a, _ := newUnattendedAgent(rec)
	if a.unattendedAskBlocked("@coder", `{"cmd":"write"}`, pm, func(string) {}) {
		t.Fatal("boolean grant must run the action")
	}
	if len(rec.asked) != 1 {
		t.Fatalf("legacy requester consulted %d times, want 1", len(rec.asked))
	}
	if len(pm.RulesSnapshot()) != before {
		t.Fatal("a boolean grant is once-only; no rule may be persisted")
	}
}

// TestRequestActionPermission_FallbackRequester: runs without an events sink
// (MCP agent_task/coder_task) reach the per-run requester installed on the
// ChatCLI (elicitation bridge).
func TestRequestActionPermission_FallbackRequester(t *testing.T) {
	rec := &permRecorder{grant: true}
	c := &ChatCLI{unattended: true, rpcPermissions: rec}
	a := NewAgentMode(c, zap.NewNop()) // a.events stays nil
	allowed, asked := a.requestActionPermission(agentevents.ToolCall{Name: "@coder"}, "r")
	if !allowed || !asked {
		t.Fatalf("fallback requester must be consulted, got allowed=%v asked=%v", allowed, asked)
	}
	if len(rec.asked) != 1 {
		t.Fatalf("requester consulted %d times, want 1", len(rec.asked))
	}
}

// TestUnattendedAskBlocked_TimeoutDeniesWithDistinctFeedback: an unanswered
// permission dialog (ErrPermissionTimeout) must deny fail-safe — but the
// model must be told the request went unanswered, NOT that the user denied
// it, so it can explain the situation instead of inventing a refusal.
func TestUnattendedAskBlocked_TimeoutDeniesWithDistinctFeedback(t *testing.T) {
	rec := &decisionRecorder{err: agentevents.ErrPermissionTimeout}
	a, c := newUnattendedAgent(rec)

	blocked := a.unattendedAskBlocked("@coder", `{"cmd":"exec","args":{"cmd":"docker rm x"}}`, nil, func(string) {})
	if !blocked {
		t.Fatal("an unanswered dialog must block the action fail-safe")
	}
	var feedback string
	for _, m := range c.history {
		if strings.Contains(m.Content, "SECURITY BLOCK") {
			feedback = m.Content
		}
	}
	if feedback == "" {
		t.Fatal("timeout must reach the model through history so it can replan")
	}
	if !strings.Contains(feedback, "NO RESPONSE") {
		t.Fatalf("feedback must say the request went unanswered, got: %s", feedback)
	}
	if strings.Contains(feedback, "DENIED by the user") {
		t.Fatalf("timeout must not be reported as a user denial, got: %s", feedback)
	}
	if len(rec.ends) != 1 || !rec.ends[0].IsError {
		t.Fatalf("blocked tool_call must close as error for the client, ends: %+v", rec.ends)
	}
}

// TestUnattendedAskBlocked_TransportErrorIsNotUserDenial: a dialog round-trip
// failure (client disconnected, write error, cancelled turn) blocks fail-safe
// but must never be reported to the model as an explicit user denial — the
// user never saw the dialog, and the misattribution made the model tell them
// they refused something they were never shown.
func TestUnattendedAskBlocked_TransportErrorIsNotUserDenial(t *testing.T) {
	rec := &permRecorder{err: errFixed("client disconnected")}
	a, c := newUnattendedAgent(rec)
	if !a.unattendedAskBlocked("@coder", `{"cmd":"exec"}`, nil, func(string) {}) {
		t.Fatal("transport error must deny fail-safe")
	}
	var feedback strings.Builder
	for _, m := range c.history {
		feedback.WriteString(m.Content)
		feedback.WriteString("\n")
	}
	if strings.Contains(feedback.String(), "DENIED by the user") {
		t.Fatal("transport failure must not be attributed to the user")
	}
	if !strings.Contains(feedback.String(), "could not be delivered") {
		t.Fatalf("model feedback must state the dialog round-trip failed, got: %q", feedback.String())
	}
}
