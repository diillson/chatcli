/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for the coder's security policy: every verdict is a point
 * on the "policy" node (which tool, allow/ask/deny, what decided it, the
 * rule that matched), and a confirmation prompt is a span on the "approval"
 * node, so the dashboard shows how long the agent waited for a human and
 * what they answered. Tool arguments never travel.
 */
package cli

import (
	"errors"
	"strings"

	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/pkg/pulse"
)

// What settled a policy verdict.
const (
	pulsePolicySourceDefault  = "default"  // no rule matched: the built-in default or a read-only capability
	pulsePolicySourceRule     = "rule"     // a policy rule matched
	pulsePolicySourceScope    = "scope"    // a slash command's allowed-tools escalated it to ask
	pulsePolicySourceAutomode = "automode" // session automode approved an ask
)

// pulsePolicyVerdict reports one verdict of the coder policy.
func (a *AgentMode) pulsePolicyVerdict(toolName string, action coder.Action, source string) {
	if !pulse.Enabled() {
		return
	}
	status := pulse.StatusOK
	switch action {
	case coder.ActionDeny:
		status = pulse.StatusBlocked
	case coder.ActionAsk:
		status = pulse.StatusRunning
	}
	attrs := map[string]string{"tool": toolName, "decision": string(action), "source": source}
	if source == pulsePolicySourceRule && a.lastPolicyMatch != nil {
		attrs["rule"] = pulseRuleLabel(a.lastPolicyMatch.Pattern)
	}
	pulse.Point(pulse.KindPattern, "policy", a.orchRun.ID(), status, attrs)
}

// pulseApprovalBegin opens the span of a confirmation prompt.
func (a *AgentMode) pulseApprovalBegin(toolName string) *pulse.Span {
	return pulse.Begin(pulse.KindPattern, "approval", a.orchRun.ID()).With("tool", toolName)
}

// pulseApprovalEnd closes it with what the human answered.
func (a *AgentMode) pulseApprovalEnd(span *pulse.Span, decision coder.SecurityDecision) {
	answer, status := "run_once", pulse.StatusOK
	switch decision {
	case coder.DecisionAllowAlways:
		answer = "allow_always"
	case coder.DecisionDenyOnce:
		answer, status = "deny_once", pulse.StatusBlocked
	case coder.DecisionDenyForever:
		answer, status = "deny_forever", pulse.StatusBlocked
	case coder.DecisionCanceled:
		answer, status = "cancelled", pulse.StatusCancelled
	}
	span.With("answer", strings.ToLower(answer)).End(status)
}

// pulseClientApprovalEnd closes an approval asked through the connected
// client's permission dialog (ACP, MCP elicitation). Only an answer the
// human gave reads as allowed or denied: no dialog, a timeout or a failed
// round-trip are blocked as what they are.
func pulseClientApprovalEnd(span *pulse.Span, decision agentevents.PermissionDecision, asked bool, err error) {
	switch {
	case !asked:
		span.With("answer", "no_dialog").End(pulse.StatusBlocked)
	case errors.Is(err, agentevents.ErrPermissionTimeout):
		span.With("answer", "timeout").End(pulse.StatusBlocked)
	case err != nil:
		span.With("answer", "failed").End(pulse.StatusError)
	case decision.Allowed():
		span.With("answer", "allowed").End(pulse.StatusOK)
	default:
		span.With("answer", "denied").End(pulse.StatusBlocked)
	}
}

// pulseRuleLabel names a policy rule by its tool and subcommand only
// ("@coder exec"): the rest of a pattern is a command's arguments, which
// may carry a host, a path or a token.
func pulseRuleLabel(pattern string) string {
	fields := strings.Fields(pattern)
	if len(fields) > 2 {
		fields = fields[:2]
	}
	return dashClip(strings.Join(fields, " "), 48)
}
