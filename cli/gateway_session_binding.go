/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * gateway_session_binding.go
 *
 * Cross-surface session continuity for the gateway daemon: a hub principal
 * can be bound to a named saved session, making the durable store — not just
 * the ephemeral hub — the conversation's home. While bound:
 *
 *   - the turn's preamble comes from the named session file (which carries
 *     REPL/MCP/ACP turns written through by those surfaces), replacing the
 *     hub-backlog preamble so the two threads are never interleaved;
 *   - every completed turn is appended to the named session file
 *     (load+append+save; the store's atomic write keeps it consistent);
 *   - the hub keeps receiving events as before, so channel fan-out and the
 *     co-running CLI's hub pull are unchanged.
 *
 * Bindings persist as hub runtime settings ("gateway_session:<principal>"),
 * the same live-read mechanism as CHATCLI_HUB_ISOLATE — a restart keeps them,
 * and a co-running CLI could inspect them via /config hub.
 *
 * Channel users control the binding with /session commands in the chat
 * (attach/detach/status/save/list/new). delete is deliberately NOT exposed to
 * channels: a gateway conversation may be multi-user, and destroying store
 * state stays an operator/REPL decision.
 */
package cli

import (
	"context"
	"strings"

	"github.com/diillson/chatcli/cli/gateway"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// gatewaySessionBindingPrefix keys the per-principal binding in the hub's
// runtime settings table.
const gatewaySessionBindingPrefix = "gateway_session:"

// sessionBindingFor returns the saved-session name the principal is bound
// to, or "" (no store, no binding, or invalid stored name).
func (s *hubSessions) sessionBindingFor(ctx context.Context, principal string) string {
	if s.store == nil || principal == "" {
		return ""
	}
	v, ok, err := s.store.GetSetting(ctx, gatewaySessionBindingPrefix+principal)
	if err != nil || !ok {
		return ""
	}
	name := strings.TrimSpace(v)
	if validateSessionName(name) != nil {
		return ""
	}
	return name
}

// setSessionBinding persists the principal→session binding.
func (s *hubSessions) setSessionBinding(ctx context.Context, principal, name string) error {
	return s.store.SetSetting(ctx, gatewaySessionBindingPrefix+principal, name)
}

// clearSessionBinding removes the principal's binding.
func (s *hubSessions) clearSessionBinding(ctx context.Context, principal string) error {
	return s.store.DeleteSetting(ctx, gatewaySessionBindingPrefix+principal)
}

// renderNamedSessionPreamble builds the turn preamble from the named
// session's tail — the bound replacement for the hub-backlog preamble.
func (cli *ChatCLI) renderNamedSessionPreamble(name string) string {
	if cli.sessionManager == nil {
		return ""
	}
	sd, err := cli.sessionManager.LoadSessionV2(name)
	if err != nil || len(sd.ChatHistory) == 0 {
		return ""
	}
	// Conversational turns only: session files written by coder/agent runs
	// carry system prompts, tool-call envelopes and raw tool output — none
	// of it belongs in a chat-channel preamble (a single tool result can be
	// tens of KB). Filter FIRST, then take the tail, so plumbing does not
	// eat the window.
	msgs := make([]models.Message, 0, len(sd.ChatHistory))
	for _, m := range sd.ChatHistory {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		if len(m.ToolCalls) > 0 || strings.TrimSpace(m.Content) == "" {
			continue
		}
		msgs = append(msgs, m)
	}
	if len(msgs) > gatewayContextTurns {
		msgs = msgs[len(msgs)-gatewayContextTurns:]
	}
	var b strings.Builder
	b.WriteString("Earlier in this conversation (across all channels):")
	wrote := false
	for _, m := range msgs {
		text := gatewayPreambleClip(strings.TrimSpace(m.Content))
		wrote = true
		if m.Role == "user" {
			b.WriteString("\n- user: ")
		} else {
			b.WriteString("\n- assistant: ")
		}
		b.WriteString(text)
	}
	if !wrote {
		return ""
	}
	return b.String()
}

// gatewayPreambleClipRunes bounds one preamble message: chat context, not a
// document dump. Rune-safe cut (session files carry UTF-8 Portuguese).
const gatewayPreambleClipRunes = 1000

func gatewayPreambleClip(s string) string {
	r := []rune(s)
	if len(r) <= gatewayPreambleClipRunes {
		return s
	}
	return string(r[:gatewayPreambleClipRunes]) + "…"
}

// appendNamedSessionTurn writes a completed gateway turn through to the named
// session file: load, append the user/assistant pair, save (atomic). Best
// effort — a store failure never fails the turn.
func (cli *ChatCLI) appendNamedSessionTurn(name, userText, reply string) {
	if cli.sessionManager == nil || name == "" {
		return
	}
	sd, err := cli.sessionManager.LoadSessionV2(name)
	if err != nil {
		sd = &SessionData{Version: 2}
	}
	if strings.TrimSpace(userText) != "" {
		sd.ChatHistory = append(sd.ChatHistory, models.Message{Role: "user", Content: userText})
	}
	if strings.TrimSpace(reply) != "" {
		sd.ChatHistory = append(sd.ChatHistory, models.Message{Role: "assistant", Content: reply})
	}
	if err := cli.sessionManager.SaveSessionV2(name, sd); err != nil {
		cli.logger.Warn("gateway: session write-through failed",
			zap.String("session", name), zap.Error(err))
	}
}

// handleGatewaySessionCommand intercepts "/session …" messages from a channel
// before they reach the engine. Returns (reply, true) when the message was a
// session command; (_, false) hands the text to the model untouched — a
// message merely starting with "/" is user input, never hijacked.
func (cli *ChatCLI) handleGatewaySessionCommand(ctx context.Context, sessions *hubSessions, msg gateway.InboundMessage) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(msg.Text))
	if len(fields) == 0 || fields[0] != "/session" {
		return "", false
	}
	if sessions == nil || sessions.store == nil || cli.sessionManager == nil {
		return i18n.T("gateway.session.unavailable"), true
	}
	principal := sessions.principalFor(ctx, msg.Platform, msg.UserID)

	sub := ""
	if len(fields) > 1 {
		sub = fields[1]
	}
	var name string
	if len(fields) > 2 {
		name = fields[2]
	}

	switch sub {
	case "attach", "load":
		return cli.gatewaySessionAttach(ctx, sessions, principal, name), true
	case "detach":
		return cli.gatewaySessionDetach(ctx, sessions, principal), true
	case "status":
		if bound := sessions.sessionBindingFor(ctx, principal); bound != "" {
			return i18n.T("session.status_bound", bound), true
		}
		return i18n.T("session.status_unbound"), true
	case "save":
		return cli.gatewaySessionSave(ctx, sessions, principal, name), true
	case "list":
		names, err := cli.sessionManager.ListSessions()
		if err != nil || len(names) == 0 {
			return i18n.T("session.list_empty"), true
		}
		return i18n.T("session.list_header") + "\n- " + strings.Join(names, "\n- "), true
	case "new":
		return cli.gatewaySessionNew(ctx, sessions, principal), true
	default:
		// Includes delete: not exposed to channels on purpose.
		return i18n.T("gateway.session.usage"), true
	}
}

// gatewaySessionAttach binds the principal to a named session and rotates
// the hub thread so the old backlog is not interleaved with the bound file.
func (cli *ChatCLI) gatewaySessionAttach(ctx context.Context, sessions *hubSessions, principal, name string) string {
	if name == "" {
		return i18n.T("session.error_name_required_load")
	}
	if err := validateSessionName(name); err != nil {
		return err.Error()
	}
	if err := sessions.setSessionBinding(ctx, principal, name); err != nil {
		cli.logger.Warn("gateway: session attach failed", zap.Error(err))
		return i18n.T("gateway.session.unavailable")
	}
	if _, err := sessions.store.NewConversation(ctx, principal); err != nil {
		cli.logger.Warn("gateway: hub rotation on attach failed", zap.Error(err))
	}
	if cli.sessionManager.SessionExists(name) {
		return i18n.T("session.status_bound", name)
	}
	return i18n.T("session.rpc.attached_new", name)
}

// gatewaySessionDetach drops the principal's binding.
func (cli *ChatCLI) gatewaySessionDetach(ctx context.Context, sessions *hubSessions, principal string) string {
	prev := sessions.sessionBindingFor(ctx, principal)
	if prev == "" {
		return i18n.T("session.detach_none")
	}
	if err := sessions.clearSessionBinding(ctx, principal); err != nil {
		cli.logger.Warn("gateway: session detach failed", zap.Error(err))
		return i18n.T("gateway.session.unavailable")
	}
	return i18n.T("session.detached", prev)
}

// gatewaySessionSave snapshots the principal's current hub conversation into
// the store under name, then binds so subsequent turns keep it updated.
func (cli *ChatCLI) gatewaySessionSave(ctx context.Context, sessions *hubSessions, principal, name string) string {
	if name == "" {
		return i18n.T("session.error_name_required_save")
	}
	if err := validateSessionName(name); err != nil {
		return err.Error()
	}
	convID, err := sessions.store.Resolve(ctx, principal)
	if err != nil {
		return i18n.T("gateway.session.unavailable")
	}
	events, err := sessions.store.Read(ctx, convID, 0, 0)
	if err != nil || len(events) == 0 {
		return i18n.T("session.rpc.nothing_to_save")
	}
	sd := &SessionData{Version: 2}
	for _, ev := range events {
		if m := ev.ToMessage(); strings.TrimSpace(m.Content) != "" {
			sd.ChatHistory = append(sd.ChatHistory, m)
		}
	}
	if err := cli.sessionManager.SaveSessionV2(name, sd); err != nil {
		return i18n.T("session.error_save", err)
	}
	if err := sessions.setSessionBinding(ctx, principal, name); err != nil {
		cli.logger.Warn("gateway: session bind after save failed", zap.Error(err))
	}
	return i18n.T("session.save_success", name)
}

// gatewaySessionNew unbinds and rotates the principal's hub thread.
func (cli *ChatCLI) gatewaySessionNew(ctx context.Context, sessions *hubSessions, principal string) string {
	if err := sessions.clearSessionBinding(ctx, principal); err != nil {
		cli.logger.Warn("gateway: session unbind on new failed", zap.Error(err))
	}
	if _, err := sessions.store.NewConversation(ctx, principal); err != nil {
		cli.logger.Warn("gateway: hub rotation failed", zap.Error(err))
		return i18n.T("gateway.session.unavailable")
	}
	return i18n.T("session.new_session_started")
}
