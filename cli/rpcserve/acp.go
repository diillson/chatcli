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

	"github.com/google/uuid"
)

// ACPProtocolVersion is the Agent Client Protocol major version supported.
const ACPProtocolVersion = 1

// ACPBackend is the capability surface the ACP server drives: plain chat
// plus the streaming agent/coder loops. Streaming loops forward the rendered
// transcript line by line through opts.Emit while they work.
type ACPBackend interface {
	Backend
	AgentStream(ctx context.Context, session, task string, opts RunOpts) (string, error)
	CoderStream(ctx context.Context, session, task string, opts RunOpts) (string, error)
}

// acpModes are the session modes advertised to the client. The mode decides
// which engine a session/prompt drives.
var acpModes = []map[string]interface{}{
	{"id": "chat", "name": "Chat", "description": "Direct model conversation, no tools."},
	{"id": "agent", "name": "Agent", "description": "Full autonomous agent (ReAct) loop with every ChatCLI tool."},
	{"id": "coder", "name": "Coder", "description": "Coding-focused agent loop: read/edit files and run commands in the workspace."},
}

const acpDefaultMode = "agent"

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

// Handle dispatches an ACP method.
func (a *ACP) Handle(ctx context.Context, method string, params json.RawMessage) (interface{}, *RPCError) {
	switch method {
	case "initialize":
		return map[string]interface{}{
			"protocolVersion": ACPProtocolVersion,
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
		return map[string]interface{}{
			"sessionId": id,
			"modes": map[string]interface{}{
				"currentModeId":  acpDefaultMode,
				"availableModes": acpModes,
			},
		}, nil
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
	return map[string]interface{}{}, nil
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
	} `json:"prompt"`
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

	var sb strings.Builder
	for _, part := range p.Prompt {
		if part.Type == "text" {
			sb.WriteString(part.Text)
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return map[string]interface{}{"stopReason": "end_turn"}, nil
	}

	// Register a cancelable context so session/cancel can interrupt.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.mu.Lock()
	s.cancel = cancel
	mode := s.mode
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		s.cancel = nil
		a.mu.Unlock()
	}()

	emit := func(line string) { a.emitMessageChunk(p.SessionID, line+"\n") }

	var reply string
	var err error
	switch mode {
	case "chat":
		reply, err = a.backend.Prompt(runCtx, p.SessionID, text)
		if err == nil {
			a.emitMessageChunk(p.SessionID, reply)
		}
	case "coder":
		_, err = a.backend.CoderStream(runCtx, p.SessionID, text, RunOpts{Emit: emit})
	default: // agent
		_, err = a.backend.AgentStream(runCtx, p.SessionID, text, RunOpts{Emit: emit})
	}

	switch {
	case errors.Is(runCtx.Err(), context.Canceled):
		return map[string]interface{}{"stopReason": "cancelled"}, nil
	case err != nil:
		a.emitMessageChunk(p.SessionID, "error: "+err.Error())
		return map[string]interface{}{"stopReason": "refusal"}, nil
	default:
		return map[string]interface{}{"stopReason": "end_turn"}, nil
	}
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
