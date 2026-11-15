/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * handoff.go
 *
 * Chat → coder handoff over ACP and MCP. A chat turn has no tools; when the
 * request needs the workspace the assistant proposes a task for coder mode
 * (RunOpts.HandoffOut) and the surface asks the user through the dialog it
 * already has: ACP's session/request_permission, MCP's elicitation/create.
 * On yes, the coder run starts in the same turn. With no dialog to ask
 * through, nothing runs and the reply says how to start it.
 */
package rpcserve

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/i18n"
	"github.com/google/uuid"
)

// handoffEnabled mirrors the engine's CHATCLI_CHAT_CODER_HANDOFF reading:
// only an explicit false/0/off/no disables the handoff.
func handoffEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_CHAT_CODER_HANDOFF"))) {
	case "false", "0", "off", "no":
		return false
	}
	return true
}

// handoffCall is the proposal as the permission dialogs show it.
func handoffCall(title, task string) agentevents.ToolCall {
	return agentevents.ToolCall{
		ID:       "handoff-" + uuid.NewString(),
		Name:     "@coder",
		Title:    title,
		Kind:     agentevents.KindOther,
		Status:   agentevents.StatusPending,
		RawInput: task,
	}
}

// handoffPrompter is the chat call that can carry a handoff proposal back.
// Backends without it keep the plain Prompt and never propose.
type handoffPrompter interface {
	PromptWith(ctx context.Context, session, text string, opts RunOpts) (string, error)
}

// chatTurn runs an ACP chat turn and delivers its reply. When the reply
// proposes a coder handoff and the user accepts it, the coder run follows
// in this same turn: its sink and reply are returned for the prompt
// handler to finish like any structured run. sink is nil otherwise.
func (a *ACP) chatTurn(ctx context.Context, s *acpSession, sessionID, text string) (*acpSink, string, error) {
	var (
		reply, task string
		err         error
	)
	if hp, ok := a.backend.(handoffPrompter); ok {
		reply, err = hp.PromptWith(ctx, sessionID, text, RunOpts{HandoffOut: &task})
	} else {
		reply, err = a.backend.Prompt(ctx, sessionID, text)
	}
	if err != nil {
		return nil, reply, err
	}
	a.emitMessageChunk(sessionID, reply)
	if task == "" {
		return nil, reply, nil
	}
	// One sink for the dialog and for the run it may start.
	sink := newACPSink(a, sessionID, ctx)
	if !a.confirmHandoff(ctx, sink, sessionID, task) {
		return nil, reply, nil
	}
	a.mu.Lock()
	s.mode = "coder"
	a.mu.Unlock()
	a.emitCurrentMode(sessionID, "coder")
	a.emitMessageChunk(sessionID, "\n\n"+i18n.T("acp.mode_switched", "coder")+"\n\n")
	reply, err = a.backend.CoderStream(ctx, sessionID, task, RunOpts{Events: sink})
	return sink, reply, err
}

// handoffDialog is what the confirmation needs from the session's sink: the
// permission round-trip and the update that closes the dialog's entry.
type handoffDialog interface {
	agentevents.PermissionDecider
	ToolEnd(tc agentevents.ToolCall)
}

// confirmHandoff asks the client whether to run the proposed task in coder
// mode. Only an explicit allow confirms. A client with no dialog (or one
// that never answered) gets the way to start it by hand instead.
func (a *ACP) confirmHandoff(ctx context.Context, dialog handoffDialog, sessionID, task string) bool {
	tc := handoffCall(i18n.T("acp.handoff.title", task), task)
	decision, err := dialog.RequestPermissionDecision(agentevents.PermissionRequest{Tool: tc})
	switch {
	case ctx.Err() != nil:
		return false
	case err != nil:
		a.emitMessageChunk(sessionID, "\n\n"+i18n.T("acp.handoff.hint", task))
		return false
	case !decision.Allowed():
		tc.Status, tc.Output = agentevents.StatusFailed, i18n.T("acp.handoff.declined")
		dialog.ToolEnd(tc)
		return false
	}
	tc.Status = agentevents.StatusCompleted
	dialog.ToolEnd(tc)
	return true
}

// mcpHandoffNote tells the calling model how to act on a proposal the
// server could not confirm with the user. English on purpose: its reader
// is the client's model.
const mcpHandoffNote = "\n\n[coder handoff] This request needs the workspace (files, commands or tests), which ask_chatcli cannot touch. " +
	"To carry it out, call the coder_task tool with the same session and this task:\n"

// mcpHandoffDeclined closes the reply when the user refused the proposal,
// so the calling model does not start the task on its own.
const mcpHandoffDeclined = "\n\n[coder handoff] The user was asked whether to run this in coder mode and declined. Do not start it unless they ask again."

// mcpHandoffUnanswered closes the reply when the dialog got no answer.
const mcpHandoffUnanswered = "\n\n[coder handoff] The user was asked whether to run this in coder mode and did not answer. Nothing was run; do not start it unless they ask."

// handoffRuns reports whether an ask_chatcli call may end up starting a
// coder run: the handoff is on and the client answers elicitation.
func (m *MCP) handoffRuns() bool {
	return handoffEnabled() && m.elicitRequest() != nil
}

// The ask_chatcli description says what a handoff does on this connection.
const (
	askHandoffRunsNote = " It cannot touch the workspace: when the request needs files, commands or tests, " +
		"the user is asked whether to run it in coder mode and, on approval, the coder run happens in this same call."
	askHandoffHintNote = " It cannot touch the workspace: when the request needs files, commands or tests, " +
		"the reply ends with a [coder handoff] task to pass to coder_task."
)

// askChatCLI is the ask_chatcli tool. The reply may propose a coder
// handoff: a client that elicits is asked and, on yes, the coder run
// happens in this call and its transcript follows the reply. Otherwise the
// reply ends with the task for the caller to pass to coder_task.
func (m *MCP) askChatCLI(ctx context.Context, session, prompt string, opts RunOpts) (interface{}, *RPCError) {
	var task string
	if !opts.Plain {
		opts.HandoffOut = &task
	}
	reply, err := m.backend.PromptWith(ctx, session, prompt, opts)
	if err != nil || task == "" {
		return m.result(reply, err)
	}
	if opts.Permissions == nil {
		return m.result(reply+mcpHandoffNote+task, nil)
	}
	switch approved, err := opts.Permissions.RequestPermission(handoffCall(i18n.T("mcp.handoff.title", task), task), i18n.T("mcp.handoff.reason")); {
	case errors.Is(err, agentevents.ErrPermissionUnsupported):
		// The client has no dialog after all: same as not eliciting.
		return m.result(reply+mcpHandoffNote+task, nil)
	case err != nil:
		// The user was asked and gave no answer. That is not a yes, so
		// the caller is not pointed at coder_task either.
		return m.result(reply+mcpHandoffUnanswered, nil)
	case !approved:
		return m.result(reply+mcpHandoffDeclined, nil)
	}
	opts.HandoffOut = nil
	out, err := m.backend.Coder(ctx, session, task, opts)
	if err != nil {
		// The reply the approval was based on stays in the result.
		return map[string]interface{}{
			"content": []map[string]interface{}{{"type": "text", "text": reply + "\n\nerror: " + err.Error()}},
			"isError": true,
		}, nil
	}
	return m.result(reply+"\n\n"+out, nil)
}
