/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * agent_events_bridge.go
 *
 * Bridges the ReAct loop (agent_mode.go) to a structured agentevents.Sink.
 * Protocol frontends (ACP today) install a sink for the duration of a run via
 * ChatCLI.agentEventSink; the loop keeps rendering its terminal UI as always
 * and ADDITIONALLY emits typed events from these helpers. Every helper is a
 * no-op when no sink is installed, so interactive/gateway/scheduler runs are
 * byte-identical.
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// coderSubcmdKinds maps @coder subcommands to ACP tool kinds.
var coderSubcmdKinds = map[string]agentevents.ToolKind{
	"read": agentevents.KindRead, "list": agentevents.KindRead, "tree": agentevents.KindRead,
	"search": agentevents.KindSearch, "grep": agentevents.KindSearch, "find": agentevents.KindSearch,
	"write": agentevents.KindEdit, "patch": agentevents.KindEdit, "append": agentevents.KindEdit,
	"replace": agentevents.KindEdit, "multipatch": agentevents.KindEdit, "diff": agentevents.KindRead,
	"delete": agentevents.KindDelete, "rm": agentevents.KindDelete,
	"move": agentevents.KindMove, "rename": agentevents.KindMove,
	"exec": agentevents.KindExecute, "run": agentevents.KindExecute, "test": agentevents.KindExecute,
}

// toolNameKinds maps plugin names (lowercase) to ACP tool kinds when the kind
// does not depend on the subcommand.
var toolNameKinds = map[string]agentevents.ToolKind{
	"@shell": agentevents.KindExecute, "@git": agentevents.KindExecute,
	"@docker": agentevents.KindExecute, "@kubectl": agentevents.KindExecute,
	"@file": agentevents.KindRead, "@lsp": agentevents.KindRead,
	"@websearch": agentevents.KindSearch, "@wikipedia": agentevents.KindSearch,
	"@registry-tags": agentevents.KindSearch,
	"@webfetch":      agentevents.KindFetch, "@http": agentevents.KindFetch,
	"@api-explorer": agentevents.KindFetch,
	"@memory":       agentevents.KindThink, "@recall": agentevents.KindThink,
	"@context": agentevents.KindThink, "@model": agentevents.KindThink,
}

// locationFlagNames are argv flags whose value is a filesystem path worth
// surfacing as an ACP tool-call location.
var locationFlagNames = map[string]bool{
	"--file": true, "-file": true, "--path": true, "-path": true,
	"--dir": true, "-dir": true, "--dest": true, "-dest": true,
}

// classifyToolCall derives the ACP kind and any file locations for one tool
// invocation from the plugin name and its parsed argv.
func classifyToolCall(toolName string, argv []string) (agentevents.ToolKind, []agentevents.Location) {
	name := strings.ToLower(strings.TrimSpace(toolName))
	kind := agentevents.KindOther
	if k, ok := toolNameKinds[name]; ok {
		kind = k
	}
	// Synthetic "execute:<lang>" names announce ```execute``` command blocks.
	if strings.HasPrefix(name, "execute:") {
		kind = agentevents.KindExecute
	}
	if name == "@coder" && len(argv) > 0 {
		if k, ok := coderSubcmdKinds[strings.ToLower(strings.TrimSpace(argv[0]))]; ok {
			kind = k
		}
	}
	var locs []agentevents.Location
	for i := 0; i+1 < len(argv); i++ {
		if locationFlagNames[strings.ToLower(argv[i])] {
			if p := strings.TrimSpace(argv[i+1]); p != "" && !strings.HasPrefix(p, "-") {
				locs = append(locs, agentevents.Location{Path: p})
			}
		}
	}
	return kind, locs
}

// planToEvent converts the tracker's plan snapshot into the ACP plan shape.
// ACP plans have no "failed" status, so failures surface as pending entries
// annotated with the error.
func planToEvent(p *agent.TaskPlan) agentevents.Plan {
	if p == nil {
		return agentevents.Plan{}
	}
	entries := make([]agentevents.PlanEntry, 0, len(p.Tasks))
	for _, t := range p.Tasks {
		if t == nil {
			continue
		}
		e := agentevents.PlanEntry{Content: t.Description, Priority: "medium", Status: string(t.Status)}
		if t.Status == agent.TaskFailed {
			e.Status = string(agent.TaskPending)
			if t.Error != "" {
				e.Content = fmt.Sprintf("%s (failed: %s)", t.Description, t.Error)
			} else {
				e.Content = t.Description + " (failed)"
			}
		}
		entries = append(entries, e)
	}
	return agentevents.Plan{Entries: entries}
}

// nextEventCallID mints a run-unique tool-call id for the events channel.
func (a *AgentMode) nextEventCallID() string {
	a.eventToolSeq++
	return fmt.Sprintf("call-%d", a.eventToolSeq)
}

// emitThought forwards reasoning/explanation text to the sink, if any.
func (a *AgentMode) emitThought(text string) {
	if a.events == nil || strings.TrimSpace(text) == "" {
		return
	}
	a.events.Thought(strings.TrimSpace(text))
}

// emitMessage forwards assistant prose to the sink, if any.
func (a *AgentMode) emitMessage(text string) {
	if a.events == nil || strings.TrimSpace(text) == "" {
		return
	}
	a.events.Message(strings.TrimSpace(text))
}

// emitPlan forwards the current task-plan snapshot to the sink, if any.
func (a *AgentMode) emitPlan() {
	if a.events == nil || a.taskTracker == nil {
		return
	}
	plan := a.taskTracker.GetPlan()
	if plan == nil || len(plan.Tasks) == 0 {
		return
	}
	a.events.PlanUpdate(planToEvent(plan))
}

// emitToolStart announces a tool call entering execution and returns the
// partially-filled ToolCall the matching emitToolEnd should complete.
func (a *AgentMode) emitToolStart(toolName, title, rawInput string, argv []string) agentevents.ToolCall {
	kind, locs := classifyToolCall(toolName, argv)
	tc := agentevents.ToolCall{
		ID:        a.nextEventCallID(),
		Name:      toolName,
		Title:     title,
		Kind:      kind,
		Status:    agentevents.StatusInProgress,
		RawInput:  rawInput,
		Locations: locs,
	}
	if a.events != nil {
		a.events.ToolStart(tc)
	}
	return tc
}

// emitToolEnd delivers the terminal state of a tool call started via
// emitToolStart. Output must be the LLM-facing (already truncated) text.
func (a *AgentMode) emitToolEnd(tc agentevents.ToolCall, output string, execErr error, errorCode string, duration time.Duration) {
	if a.events == nil {
		return
	}
	tc.Status = agentevents.StatusCompleted
	tc.Output = output
	tc.Duration = duration
	tc.ErrorCode = errorCode
	if execErr != nil {
		tc.Status = agentevents.StatusFailed
		tc.IsError = true
		if strings.TrimSpace(tc.Output) == "" {
			tc.Output = execErr.Error()
		}
	}
	// Whole-content reads are surfaced as title+locations only: IDE clients
	// show the file natively, so dumping it as chat content is pure noise.
	tc.OmitContent = tc.Kind == agentevents.KindRead && !tc.IsError
	a.events.ToolEnd(tc)
}

// emitBlockedTool reports a tool call that was refused before execution
// (security policy, validation) as a failed tool_call the client never saw
// start — sinks emit the full tool_call shape for unseen ids.
func (a *AgentMode) emitBlockedTool(toolName, rawInput, msg string) {
	if a.events == nil {
		return
	}
	a.events.ToolEnd(agentevents.ToolCall{
		ID:       a.nextEventCallID(),
		Name:     toolName,
		Title:    toolName,
		Kind:     agentevents.KindOther,
		Status:   agentevents.StatusFailed,
		RawInput: rawInput,
		Output:   msg,
		IsError:  true,
	})
}

// executeCommandBlocksUnattended runs ```execute``` blocks without the
// interactive menu, for unattended runs (ACP, MCP agent_task). Before this
// path existed those runs fell into handleCommandBlocks, whose menu blocks
// forever reading a dead stdin. Dangerous commands follow the same contract
// as the one-shot auto-exec gate (agent.oneshot): ask the sink's
// PermissionRequester when one is installed (ACP surfaces the IDE's native
// dialog); otherwise honor dangerBlocked() exactly like RunOnce does.
// Returns the feedback text to append to history for the model's next turn.
func (a *AgentMode) executeCommandBlocksUnattended(ctx context.Context, blocks []CommandBlock) string {
	var fb strings.Builder
	for bi, block := range blocks {
		fmt.Fprintf(&fb, "--- Resultado do Bloco %d (%s) ---\n", bi+1, block.Language)

		blocked := ""
		for _, cmd := range block.Commands {
			if !a.validator.IsDangerous(cmd) {
				continue
			}
			allowed, asked := a.requestActionPermission(agentevents.ToolCall{
				Name:     "execute:" + block.Language,
				Title:    cmd,
				Kind:     agentevents.KindExecute,
				RawInput: cmd,
			}, i18n.T("agent.oneshot.auto_exec_aborted", cmd))
			if asked {
				if allowed {
					continue
				}
				blocked = cmd
				break
			}
			// No PermissionRequester installed (MCP agent_task): refuse the
			// dangerous command in-band so the model can replan. This is
			// deliberately STRICTER than the RunOnce unattended contract:
			// before this path existed these runs never executed anything
			// (they hung on a dead-stdin menu), so no operator has ever
			// relied on dangerous blocks auto-running here — don't let a
			// hang-fix quietly become remote dangerous-command execution.
			blocked = cmd
			break
		}
		if blocked != "" {
			msg := i18n.T("agent.oneshot.auto_exec_aborted", blocked)
			fb.WriteString("ERRO: " + msg + "\n\n")
			a.emitBlockedTool("execute:"+block.Language, blocked, msg)
			continue
		}

		start := time.Now()
		tcEv := a.emitToolStart("execute:"+block.Language, block.Description,
			strings.Join(block.Commands, "\n"), nil)
		output, errorMsg := a.executeCommandsFunc(ctx, block)
		var execErr error
		if errorMsg != "" {
			execErr = fmt.Errorf("%s", errorMsg)
		}
		out := plugins.TruncateForLLM(output, plugins.DefaultMaxResultChars)
		a.emitToolEnd(tcEv, out, execErr, "", time.Since(start))

		if errorMsg != "" {
			fmt.Fprintf(&fb, "ERRO: %s\nSaída parcial: %s\n\n", errorMsg, out)
		} else {
			fb.WriteString(out + "\n\n")
		}
	}
	return strings.TrimSpace(fb.String())
}

// permissionSource resolves the run's permission surface with the historical
// precedence — the sink's own capability first (ACP), else the per-run
// requester installed on the ChatCLI (MCP elicitation bridge). A source
// counts if it implements EITHER the four-way PermissionDecider or the
// boolean PermissionRequester; requestActionDecision prefers the decider and
// maps a boolean answer to the once-only decisions.
func (a *AgentMode) permissionSource() (interface{}, bool) {
	switch a.events.(type) {
	case agentevents.PermissionDecider, agentevents.PermissionRequester:
		return a.events, true
	}
	if a.cli != nil && a.cli.rpcPermissions != nil {
		return a.cli.rpcPermissions, true
	}
	return nil, false
}

// requestActionDecision asks the run's permission surface to resolve a gated
// action with the terminal prompt's four-way vocabulary. req.Tool should be
// the already announced tool call when one exists, so the client can anchor
// its dialog to the same toolCallId. ok reports whether a dialog was
// consulted; the decision is only meaningful when ok is true. Transport
// errors deny fail-safe (deny_once, consulted) and surface as reqErr so
// callers can distinguish an unanswered dialog (ErrPermissionTimeout) from a
// human denial. A client that reports the round-trip unsupported
// (ErrPermissionUnsupported) counts as NOT consulted: no dialog exists, so
// callers apply the same contract as runs without a requester.
func (a *AgentMode) requestActionDecision(req agentevents.PermissionRequest) (decision agentevents.PermissionDecision, ok bool, reqErr error) {
	pr, has := a.permissionSource()
	if !has {
		return agentevents.PermissionDenyOnce, false, nil
	}
	if req.Tool.ID == "" {
		req.Tool.ID = a.nextEventCallID()
	}
	req.Tool.Status = agentevents.StatusPending

	var d agentevents.PermissionDecision
	var err error
	switch impl := pr.(type) {
	case agentevents.PermissionDecider:
		d, err = impl.RequestPermissionDecision(req)
	case agentevents.PermissionRequester:
		req.OfferAlways = false // boolean surface cannot express "always"
		var granted bool
		granted, err = impl.RequestPermission(req.Tool, req.Reason)
		if granted {
			d = agentevents.PermissionAllowOnce
		} else {
			d = agentevents.PermissionDenyOnce
		}
	default:
		return agentevents.PermissionDenyOnce, false, nil
	}
	if err != nil {
		if errors.Is(err, agentevents.ErrPermissionUnsupported) {
			if a.logger != nil {
				a.logger.Info("client does not support permission requests; applying unattended fallback")
			}
			return agentevents.PermissionDenyOnce, false, nil
		}
		if a.logger != nil {
			a.logger.Warn("permission request failed; denying action", zap.Error(err))
		}
		return agentevents.PermissionDenyOnce, true, err
	}
	return d, true, nil
}

// requestActionPermission is the boolean view over requestActionDecision,
// kept for callers with once-only semantics (the dangerous-exec guard).
func (a *AgentMode) requestActionPermission(tc agentevents.ToolCall, reason string) (allowed, ok bool) {
	d, ok, _ := a.requestActionDecision(agentevents.PermissionRequest{Tool: tc, Reason: reason})
	return ok && d.Allowed(), ok
}

// unattendedAskBlocked resolves a coder-policy ActionAsk verdict in an
// unattended run. When the connected client offers a permission dialog (ACP
// session/request_permission, MCP elicitation), the human decides with the
// terminal prompt's full vocabulary: the "always" variants persist a policy
// rule through pm exactly like the interactive flow, and a denial blocks the
// action, renders the refusal, tells the model (so it can replan) and closes
// the tool_call for the client. The persistent choices are only offered when
// a suggested pattern exists — exec commands never get one, by design.
// Without a reachable dialog the historical unattended contract holds —
// "ask" auto-approves (the operator opted into autonomy; explicit deny rules
// still block upstream).
func (a *AgentMode) unattendedAskBlocked(toolName, rawArgs string, pm *coder.PolicyManager, renderError func(string)) bool {
	title := agent.CompactToolLabel(extractSubcmdFromArgs(rawArgs), rawArgs)
	if strings.TrimSpace(title) == "" {
		title = toolName
	}
	kind, _ := classifyToolCall(toolName, nil)
	pattern := ""
	if pm != nil {
		pattern = coder.GetSuggestedPattern(toolName, rawArgs)
	}
	decision, asked, reqErr := a.requestActionDecision(agentevents.PermissionRequest{
		Tool: agentevents.ToolCall{
			Name:     toolName,
			Title:    title,
			Kind:     kind,
			RawInput: rawArgs,
		},
		Reason:      i18n.T("agent.permission.policy_ask", toolName),
		OfferAlways: pattern != "",
	})
	if !asked {
		if a.logger != nil {
			a.logger.Info("coder policy: 'ask' auto-approved (unattended, no permission dialog)",
				zap.String("tool", toolName))
		}
		return false
	}
	// An unanswered dialog (client declared the capability but never
	// rendered/answered it) is not a human denial: block fail-safe, but tell
	// the model the truth so it explains the situation instead of inventing a
	// refusal — its reply is the one surface the user is guaranteed to see.
	if errors.Is(reqErr, agentevents.ErrPermissionTimeout) {
		msg := i18n.T("agent.policy.ask_timeout", title)
		feedback := fmt.Sprintf(
			"SECURITY BLOCK: The permission request for %q got NO RESPONSE from the user (the approval dialog timed out — the client may not display permission dialogs). The action was NOT executed. DO NOT retry it; continue without it, or tell the user their client did not surface the approval dialog and that session policy automode (the /policy command, or the manage_session policy_mode action over MCP) restores unattended auto-approval.",
			title)
		renderError(msg)
		a.emitBlockedTool(toolName, rawArgs, msg)
		a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: feedback})
		return true
	}
	if decision.Persistent() && pm != nil && pattern != "" {
		action := coder.ActionDeny
		if decision.Allowed() {
			action = coder.ActionAllow
		}
		// A persistence failure must not change the run's outcome: the
		// user's allow/deny still applies this once, so log and continue.
		if err := pm.AddRule(pattern, action); err != nil && a.logger != nil {
			a.logger.Warn("failed to persist policy rule from permission dialog",
				zap.String("pattern", pattern), zap.String("action", string(action)), zap.Error(err))
		}
	}
	if decision.Allowed() {
		return false
	}
	msg := i18n.T("agent.policy.ask_denied", title)
	feedback := fmt.Sprintf(
		"SECURITY BLOCK: Action %q was DENIED by the user through the security-policy prompt. DO NOT retry it; choose a different approach or explain what you need.",
		title)
	if decision == agentevents.PermissionDenyAlways {
		msg = i18n.T("agent.policy.ask_denied_always", title)
		feedback = fmt.Sprintf(
			"SECURITY BLOCK: Action %q was PERMANENTLY DENIED by the user; a deny rule now blocks it for every future attempt. DO NOT retry it; choose a different approach or explain what you need.",
			title)
	}
	renderError(msg)
	a.emitBlockedTool(toolName, rawArgs, msg)
	a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: feedback})
	return true
}
