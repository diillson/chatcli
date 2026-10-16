/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// seedPolicyAutoModeFromEnv starts the session in policy automode when the
// operator set CHATCLI_POLICY_AUTOMODE=true. It is the only process-level
// opt-in to autonomy: without it, surfaces with no human to ask refuse every
// policy "ask". The opt-in is logged at warn level once per process.
func (cli *ChatCLI) seedPolicyAutoModeFromEnv() {
	if coder.OperatorPolicyAutoMode(cli.logger) {
		cli.policyAutoMode.Store(true)
	}
}

// toolPolicyVerdict resolves the coder policy for one orchestrator tool call
// before any human is consulted: the policy check (recording the matched
// rule for /config), the slash-command allowed-tools overlay and the session
// automode. A remaining ActionAsk still needs an approver; unattended runs
// refuse it when no permission dialog exists.
func (a *AgentMode) toolPolicyVerdict(pm *coder.PolicyManager, toolName, args string) (action coder.Action) {
	source := pulsePolicySourceDefault
	defer func() { a.pulsePolicyVerdict(toolName, action, source) }()
	action = pm.Check(toolName, args)
	if rule, ok := pm.LastMatchedRule(); ok {
		a.lastPolicyMatch = &rule
		source = pulsePolicySourceRule
	} else {
		a.lastPolicyMatch = nil
	}
	// Slash-command allowed-tools overlay: a tool outside the command's
	// declared list escalates to "ask" even when policy would allow — the
	// command author scoped the run, the human (or policy automode)
	// arbitrates the exception. Never a silent widen, never a silent deny.
	if action == coder.ActionAllow && !a.commandScopeAllows(toolName) {
		if a.logger != nil {
			a.logger.Info("tool outside slash-command allowed-tools scope, escalating to ask",
				zap.String("tool", toolName))
		}
		action = coder.ActionAsk
		source = pulsePolicySourceScope
	}
	// Session automode (/policy mode auto, or CHATCLI_POLICY_AUTOMODE):
	// "ask" verdicts auto-approve. Deny rules were already resolved above ask
	// by the policy check, and safety-immune operations never take this
	// shortcut.
	if action == coder.ActionAsk && a.askAutoApproved(toolName, args) {
		if a.logger != nil {
			a.logger.Info("coder policy: 'ask' auto-approved (session automode)",
				zap.String("tool", toolName))
		}
		action = coder.ActionAllow
		source = pulsePolicySourceAutomode
	}
	return action
}

// policyUnavailableBlocked refuses a tool call when the security policy
// failed to load: an unreadable policy is never permission. Returns true
// when the call was blocked.
func (a *AgentMode) policyUnavailableBlocked(err error, toolName, rawArgs string, renderError func(string)) bool {
	if err == nil {
		return false
	}
	if a.logger != nil {
		a.logger.Error("coder policy unavailable; denying tool call", zap.String("tool", toolName), zap.Error(err))
	}
	msg := i18n.T("agent.policy.manager_unavailable", err, toolName)
	renderError(msg)
	a.emitBlockedTool(toolName, rawArgs, msg)
	a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: fmt.Sprintf(
		"SECURITY BLOCK: the security policy could not be loaded, so %q was NOT executed. DO NOT retry it; tell the user the policy setup is broken.",
		toolName)})
	return true
}

// policyUnavailableChecker is the worker PolicyChecker installed when the
// security policy failed to load: it refuses every call.
type policyUnavailableChecker struct{ err error }

// CheckAndPrompt implements workers.PolicyChecker.
func (c policyUnavailableChecker) CheckAndPrompt(_ context.Context, toolName, _ string) (bool, string) {
	return false, i18n.T("agent.policy.manager_unavailable", c.err, toolName)
}

// rpcToolPolicyGate applies the coder security policy to a tool invoked
// through the RPC tool surface (MCP/ACP passthrough, the gRPC pipeline's
// CallTool and `chatcli tool`). Nobody can answer a prompt there, so "ask"
// is refused unless the session is in policy automode; deny rules and
// safety-immune operations always refuse. A policy that fails to load
// refuses too.
func (cli *ChatCLI) rpcToolPolicyGate(name, policyArgs string) error {
	toolName := "@" + strings.TrimPrefix(name, "@")
	pm, err := coder.NewPolicyManager(cli.logger)
	if err != nil {
		return errors.New(i18n.T("rpc.tool.policy_unavailable", toolName, err))
	}
	switch coder.ResolveWithoutApprover(pm, toolName, policyArgs, cli.PolicyAutoMode()) {
	case coder.ActionAllow:
		return nil
	case coder.ActionDeny:
		return errors.New(i18n.T("rpc.tool.policy_denied", toolName))
	default:
		return errors.New(i18n.T("rpc.tool.policy_needs_approval", toolName))
	}
}
