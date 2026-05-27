/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rpcserve

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// ACPProtocolVersion is the Agent Client Protocol major version supported.
const ACPProtocolVersion = 1

// ACP implements the Agent Client Protocol server methods, letting editors
// (e.g. Zed) drive ChatCLI as an agent over stdio. Prompt responses are
// streamed back as session/update notifications, then a final stopReason.
type ACP struct {
	backend  Backend
	version  string
	notify   func(method string, params interface{}) error
	mu       sync.Mutex
	sessions map[string]bool
}

// NewACP builds an ACP handler.
func NewACP(backend Backend, version string) *ACP {
	return &ACP{backend: backend, version: version, sessions: map[string]bool{}}
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
				"promptCapabilities": map[string]interface{}{"image": false, "audio": false},
			},
			"authMethods": []interface{}{},
		}, nil
	case "session/new":
		id := uuid.NewString()
		a.mu.Lock()
		a.sessions[id] = true
		a.mu.Unlock()
		return map[string]interface{}{"sessionId": id}, nil
	case "session/prompt":
		return a.prompt(ctx, params)
	case "session/cancel":
		return nil, nil // notification; best-effort no-op
	default:
		return nil, errf(CodeMethodNotFound, "unknown method %q", method)
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

	reply, err := a.backend.Prompt(ctx, p.SessionID, text)
	if err != nil {
		a.emitMessageChunk(p.SessionID, "error: "+err.Error())
		return map[string]interface{}{"stopReason": "refusal"}, nil
	}

	// Stream the answer as an agent message chunk, then end the turn.
	a.emitMessageChunk(p.SessionID, reply)
	return map[string]interface{}{"stopReason": "end_turn"}, nil
}

// emitMessageChunk sends an ACP session/update with an agent message chunk.
func (a *ACP) emitMessageChunk(sessionID, text string) {
	if a.notify == nil {
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
