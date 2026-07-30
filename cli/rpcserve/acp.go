/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rpcserve

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"unicode"

	"github.com/diillson/chatcli/cli/palette"
	"github.com/diillson/chatcli/i18n"
	"github.com/google/uuid"
)

// ACPProtocolVersion is the Agent Client Protocol major version supported.
const ACPProtocolVersion = 1

// CommandInfo describes one slash command advertised to ACP clients via
// available_commands_update. Name carries no leading slash (ACP convention).
type CommandInfo struct {
	Name        string
	Description string
	InputHint   string
}

// ACPBackend is the capability surface the ACP server drives: plain chat
// plus the agent/coder loops. Structured runs receive an agentevents.Sink
// via opts.Events and return the clean final answer; opts.Emit remains the
// legacy line-scraper for callers that want the rendered transcript.
type ACPBackend interface {
	Backend
	AgentStream(ctx context.Context, session, task string, opts RunOpts) (string, error)
	CoderStream(ctx context.Context, session, task string, opts RunOpts) (string, error)
}

// ACPCommandBackend is an OPTIONAL capability (checked by type assertion,
// never required — expanding ACPBackend itself would break every existing
// implementer): backends that also expose the slash-command surface get
// available_commands_update advertising and headless execution.
type ACPCommandBackend interface {
	// ACPCommands lists the slash commands runnable over ACP (mode switches
	// plus the headless allowlist).
	ACPCommands() []CommandInfo
	// RunCommand executes one allowlisted slash command headless and returns
	// its captured output. Mode switches never reach it.
	RunCommand(ctx context.Context, session, line string) (string, error)
}

// acpModes are the session modes advertised to the client. The mode decides
// which engine a session/prompt drives.
var acpModes = []map[string]interface{}{
	{"id": "coder", "name": "Coder", "description": "Full agent loop with every ChatCLI tool: read/edit files, run commands, web, memory, MCP. Recommended for any task."},
	{"id": "chat", "name": "Chat", "description": "Direct model conversation, no tools."},
	{"id": "agent", "name": "Agent", "description": "Command-oriented loop that proposes shell commands step by step."},
}

// acpDefaultMode is "coder" on purpose: the coder loop covers conversation
// AND autonomous tool work, so it serves chat-like and agent-like prompts
// alike; the agent mode remains as an opt-in for step-by-step command flows.
const acpDefaultMode = "coder"

// acpSession tracks one client session: its mode and, while a prompt is in
// flight, the cancel function session/cancel fires.
type acpSession struct {
	mode   string
	cancel context.CancelFunc
}

// ACP implements the Agent Client Protocol server methods, letting editors
// (e.g. Zed) and other agents drive ChatCLI over stdio. Prompt turns stream
// progress as session/update notifications, then return a stopReason.
type ACP struct {
	backend  ACPBackend
	version  string
	notify   func(method string, params interface{}) error
	request  func(ctx context.Context, method string, params interface{}) (json.RawMessage, error)
	mu       sync.Mutex
	sessions map[string]*acpSession
}

// NewACP builds an ACP handler.
func NewACP(backend ACPBackend, version string) *ACP {
	return &ACP{backend: backend, version: version, sessions: map[string]*acpSession{}}
}

// SetNotifier wires the server's notification writer (call after NewServer).
func (a *ACP) SetNotifier(fn func(method string, params interface{}) error) {
	a.notify = fn
}

// SetRequester wires the server's client-request round-trip, used for
// session/request_permission. Without it, permission requests deny.
func (a *ACP) SetRequester(fn func(ctx context.Context, method string, params interface{}) (json.RawMessage, error)) {
	a.request = fn
}

// Handle dispatches an ACP method.
func (a *ACP) Handle(ctx context.Context, method string, params json.RawMessage) (interface{}, *RPCError) {
	switch method {
	case "initialize":
		return map[string]interface{}{
			"protocolVersion": ACPProtocolVersion,
			"agentInfo":       map[string]interface{}{"name": "chatcli", "version": a.version},
			"agentCapabilities": map[string]interface{}{
				"loadSession":        false,
				"promptCapabilities": map[string]interface{}{"image": false, "audio": false, "embeddedContext": true},
			},
			"authMethods": []interface{}{},
		}, nil
	case "session/new":
		id := uuid.NewString()
		a.mu.Lock()
		a.sessions[id] = &acpSession{mode: acpDefaultMode}
		a.mu.Unlock()
		result := map[string]interface{}{
			"sessionId": id,
			"modes": map[string]interface{}{
				"currentModeId":  acpDefaultMode,
				"availableModes": acpModes,
			},
		}
		// available_commands_update must trail the response on the wire —
		// PostReplyResult defers the notification until after the write.
		return PostReplyResult{Result: result, Post: func() { a.sendAvailableCommands(id) }}, nil
	case "session/set_mode":
		return a.setMode(params)
	case "session/prompt":
		return a.prompt(ctx, params)
	case "session/cancel":
		a.cancelSession(params)
		return nil, nil // notification
	default:
		return nil, errf(CodeMethodNotFound, "unknown method %q", method)
	}
}

func (a *ACP) setMode(params json.RawMessage) (interface{}, *RPCError) {
	var p struct {
		SessionID string `json:"sessionId"`
		ModeID    string `json:"modeId"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.SessionID == "" {
		return nil, errf(CodeInvalidParams, "sessionId is required")
	}
	valid := false
	for _, m := range acpModes {
		if m["id"] == p.ModeID {
			valid = true
			break
		}
	}
	if !valid {
		return nil, errf(CodeInvalidParams, "unknown mode %q", p.ModeID)
	}
	a.mu.Lock()
	s, ok := a.sessions[p.SessionID]
	if ok {
		s.mode = p.ModeID
	}
	a.mu.Unlock()
	if !ok {
		return nil, errf(CodeInvalidParams, "unknown session %q", p.SessionID)
	}
	// Confirm after the response: clients treat current_mode_update as the
	// authoritative mode signal (also emitted on slash-driven switches).
	return PostReplyResult{
		Result: map[string]interface{}{},
		Post:   func() { a.emitCurrentMode(p.SessionID, p.ModeID) },
	}, nil
}

// emitCurrentMode notifies the client that the session's mode changed.
func (a *ACP) emitCurrentMode(sessionID, modeID string) {
	if a.notify == nil {
		return
	}
	_ = a.notify("session/update", map[string]interface{}{
		"sessionId": sessionID,
		"update": map[string]interface{}{
			"sessionUpdate": "current_mode_update",
			"currentModeId": modeID,
		},
	})
}

// sendAvailableCommands advertises the backend's slash-command surface,
// when the backend implements the optional ACPCommandBackend capability.
func (a *ACP) sendAvailableCommands(sessionID string) {
	cb, ok := a.backend.(ACPCommandBackend)
	if a.notify == nil || !ok {
		return
	}
	cmds := cb.ACPCommands()
	list := make([]map[string]interface{}, 0, len(cmds))
	for _, c := range cmds {
		entry := map[string]interface{}{"name": c.Name, "description": c.Description}
		if c.InputHint != "" {
			entry["input"] = map[string]interface{}{"hint": c.InputHint}
		}
		list = append(list, entry)
	}
	_ = a.notify("session/update", map[string]interface{}{
		"sessionId": sessionID,
		"update": map[string]interface{}{
			"sessionUpdate":     "available_commands_update",
			"availableCommands": list,
		},
	})
}

// cancelSession fires the in-flight prompt's cancel function, if any.
func (a *ACP) cancelSession(params json.RawMessage) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.SessionID == "" {
		return
	}
	a.mu.Lock()
	s, ok := a.sessions[p.SessionID]
	var cancel context.CancelFunc
	if ok && s.cancel != nil {
		cancel = s.cancel
	}
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

type acpPromptParams struct {
	SessionID string `json:"sessionId"`
	Prompt    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
		// resource_link fields (mandatory-support content block).
		URI  string `json:"uri"`
		Name string `json:"name"`
		// resource carries embedded context (embeddedContext capability).
		Resource *struct {
			URI  string `json:"uri"`
			Text string `json:"text"`
		} `json:"resource"`
	} `json:"prompt"`
}

// flattenPromptParts renders the prompt's content blocks as the model-facing
// text. text blocks pass through; resource_link becomes a file reference the
// agent's tools can open; resource embeds the provided contents inline.
// Parts are joined with newlines — concatenating them raw glued words from
// adjacent blocks together ("/coder" + "do X" = "/coderdo X").
func flattenPromptParts(p acpPromptParams) string {
	var parts []string
	for _, part := range p.Prompt {
		switch part.Type {
		case "text":
			if t := strings.TrimSpace(part.Text); t != "" {
				parts = append(parts, t)
			}
		case "resource_link":
			ref := part.URI
			if ref == "" {
				ref = part.Name
			}
			if ref != "" {
				parts = append(parts, "Referenced file: "+strings.TrimPrefix(ref, "file://"))
			}
		case "resource":
			if part.Resource != nil && strings.TrimSpace(part.Resource.Text) != "" {
				name := strings.TrimPrefix(part.Resource.URI, "file://")
				parts = append(parts, "Attached context ("+name+"):\n"+part.Resource.Text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func (a *ACP) prompt(ctx context.Context, params json.RawMessage) (interface{}, *RPCError) {
	var p acpPromptParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, errf(CodeInvalidParams, "invalid params: %v", err)
	}
	if p.SessionID == "" {
		return nil, errf(CodeInvalidParams, "sessionId is required")
	}
	a.mu.Lock()
	s, ok := a.sessions[p.SessionID]
	a.mu.Unlock()
	if !ok {
		return nil, errf(CodeInvalidParams, "unknown session %q — call session/new first", p.SessionID)
	}

	text := flattenPromptParts(p)
	if text == "" {
		return map[string]interface{}{"stopReason": "end_turn"}, nil
	}

	// Register a cancelable context so session/cancel can interrupt. The
	// spec forbids overlapping prompts on one session; a second in-flight
	// prompt is rejected instead of silently clobbering the first one's
	// cancel bookkeeping (which would make session/cancel a no-op).
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.mu.Lock()
	if s.cancel != nil {
		a.mu.Unlock()
		return nil, errf(CodeInvalidRequest, "a prompt is already in flight for session %q", p.SessionID)
	}
	s.cancel = cancel
	mode := s.mode
	a.mu.Unlock()
	defer func() {
		// Overlapping prompts are rejected above, so the registered cancel
		// is necessarily ours — safe to clear unconditionally.
		a.mu.Lock()
		s.cancel = nil
		a.mu.Unlock()
	}()

	// Slash interception: mode switches are handled here; allowlisted
	// commands run headless through the backend; a KNOWN REPL command that
	// is not headless-capable gets a clear "not available over ACP" answer.
	// Anything else falls through to the model — a prompt that merely
	// starts with "/" (a filesystem path, a typo'd word) is user input, and
	// user input is never hijacked.
	if strings.HasPrefix(text, "/") {
		token, rest := splitSlashCommand(text)
		if modeID, isMode := acpSlashModes[token]; isMode {
			a.mu.Lock()
			s.mode = modeID
			a.mu.Unlock()
			a.emitCurrentMode(p.SessionID, modeID)
			a.emitMessageChunk(p.SessionID, i18n.T("acp.mode_switched", modeID))
			if rest == "" {
				return map[string]interface{}{"stopReason": "end_turn"}, nil
			}
			// "/coder fix the tests": switch the session, then run the task
			// in the new mode within this same turn.
			mode = modeID
			text = rest
		} else if cb, isCmd := a.commandAdvertised(token); isCmd {
			out, cmdErr := cb.RunCommand(runCtx, p.SessionID, text)
			if cmdErr != nil {
				a.emitMessageChunk(p.SessionID, i18n.T("acp.command_failed", cmdErr.Error()))
			} else {
				a.emitMessageChunk(p.SessionID, out)
			}
			return map[string]interface{}{"stopReason": "end_turn"}, nil
		} else if _, known := palette.RootSummary(token); known {
			a.emitMessageChunk(p.SessionID, i18n.T("acp.command_unsupported", token))
			return map[string]interface{}{"stopReason": "end_turn"}, nil
		}
		// Unknown token: plain user text — reaches the model untouched.
	}

	var reply string
	var err error
	var sink *acpSink
	switch mode {
	case "chat":
		reply, err = a.backend.Prompt(runCtx, p.SessionID, text)
		if err == nil {
			a.emitMessageChunk(p.SessionID, reply)
		}
	case "coder":
		sink = newACPSink(a, p.SessionID, runCtx)
		reply, err = a.backend.CoderStream(runCtx, p.SessionID, text, RunOpts{Events: sink})
	default: // agent
		sink = newACPSink(a, p.SessionID, runCtx)
		reply, err = a.backend.AgentStream(runCtx, p.SessionID, text, RunOpts{Events: sink})
	}

	switch {
	case errors.Is(runCtx.Err(), context.Canceled):
		if sink != nil {
			// Never leave the client with an eternally in-progress call.
			sink.closeOpen("cancelled")
		}
		return map[string]interface{}{"stopReason": "cancelled"}, nil
	case err != nil:
		if sink != nil {
			sink.closeOpen("error: " + err.Error())
		}
		a.emitMessageChunk(p.SessionID, "error: "+err.Error())
		return map[string]interface{}{"stopReason": "refusal"}, nil
	default:
		// Safety net: a structured run whose loop emitted no prose still
		// delivers its final answer (the backend's return value).
		if sink != nil && !sink.hasMessage() && strings.TrimSpace(reply) != "" {
			a.emitMessageChunk(p.SessionID, reply)
		}
		if sink != nil {
			// Normally a no-op; heals abnormal-but-non-error loop exits
			// (e.g. an @park sentinel) that left a tool call announced —
			// the client must never keep an eternal spinner.
			sink.closeOpen("not completed")
		}
		return map[string]interface{}{"stopReason": "end_turn"}, nil
	}
}

// acpSlashModes maps mode-switch slash tokens to session mode ids.
var acpSlashModes = map[string]string{
	"/chat":  "chat",
	"/agent": "agent",
	"/run":   "agent",
	"/coder": "coder",
}

// splitSlashCommand splits "/cmd rest of line" into its token and remainder.
// The token ends at ANY whitespace (space, tab, newline) — editor-style
// prompt boxes routinely produce "/coder\nfix the tests".
func splitSlashCommand(text string) (token, rest string) {
	idx := strings.IndexFunc(text, unicode.IsSpace)
	if idx < 0 {
		return strings.TrimSpace(text), ""
	}
	return strings.TrimSpace(text[:idx]), strings.TrimSpace(text[idx:])
}

// commandAdvertised reports whether token (with leading slash) is in the
// backend's advertised command surface (mode switches excluded — the caller
// drains those first). Returns the capability so the caller can execute
// without re-asserting; nil/false when the backend lacks the capability.
func (a *ACP) commandAdvertised(token string) (ACPCommandBackend, bool) {
	cb, ok := a.backend.(ACPCommandBackend)
	if !ok {
		return nil, false
	}
	name := strings.TrimPrefix(token, "/")
	for _, c := range cb.ACPCommands() {
		if c.Name == name {
			return cb, true
		}
	}
	return nil, false
}

// emitMessageChunk sends an ACP session/update with an agent message chunk.
func (a *ACP) emitMessageChunk(sessionID, text string) {
	if a.notify == nil || text == "" {
		return
	}
	_ = a.notify("session/update", map[string]interface{}{
		"sessionId": sessionID,
		"update": map[string]interface{}{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]interface{}{"type": "text", "text": text},
		},
	})
}
