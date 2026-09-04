/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ChatCLI - session_adapter.go
 *
 * Implements plugins.SessionAdapter so the @session tool can search the saved
 * conversation store through the live SessionManager. Supplied to
 * plugins.SetSessionAdapter at startup.
 */
package cli

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
)

// sessionPluginAdapter is the concrete plugins.SessionAdapter.
type sessionPluginAdapter struct {
	cli *ChatCLI
}

// Search runs a free-text search across saved sessions.
func (a *sessionPluginAdapter) Search(_ context.Context, query string, limit int) (string, error) {
	if a.cli == nil || a.cli.sessionManager == nil {
		return "", fmt.Errorf("%s", i18n.T("session.tool.unavailable"))
	}
	hits, err := a.cli.sessionManager.SearchSessions(query, limit)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return i18n.T("session.tool.no_match", query), nil
	}

	var b strings.Builder
	b.WriteString(i18n.T("session.tool.match_header", query))
	b.WriteByte('\n')
	for _, h := range hits {
		label := h.Session
		if h.Title != "" {
			label += " — " + h.Title
		}
		fmt.Fprintf(&b, "\n• %s (%s)\n", label, i18n.T("session.tool.match_count", h.Matches))
		for _, sn := range h.Snippets {
			sn = strings.TrimSpace(sn)
			if sn != "" {
				b.WriteString("    … " + sn + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// getPageLimit is the default @session get page size, in messages.
const getPageLimit = 20

// Role-aware caps for one message's rendered length inside a page. Tool
// dumps stay tightly bounded (a single giant command output cannot blow the
// reply budget), but user/assistant prose is exactly what a recap needs —
// the old flat 600-char cap on assistant analysis messages was the "partial
// recall" failure mode. A single message fetched by index gets the largest
// ceiling.
const (
	getMessageCapTool   = 600
	getMessageCapProse  = 2000
	getSingleMessageCap = 8000
)

// getMessageCapFor returns the page-render cap for a message role.
func getMessageCapFor(role string) int {
	switch strings.ToLower(role) {
	case "tool", "function", "system":
		return getMessageCapTool
	default:
		return getMessageCapProse
	}
}

// sessionSystemOmitted replaces stored system prompts in @session output.
// They are internal instructions, not conversation: echoing them wastes the
// reply budget and — worse — reads as an answer ("here is what the session
// contains: You are a senior software engineer…") while saying nothing about
// what was actually discussed.
const sessionSystemOmitted = "[system prompt omitted — internal instructions, not conversation]"

// renderSessionMessage renders one message for @session output, masking
// system prompts and truncating rune-safely to the role's cap.
func renderSessionMessage(m models.Message, capBytes int) string {
	if strings.EqualFold(m.Role, "system") {
		return sessionSystemOmitted
	}
	return truncateRunesafe(strings.TrimSpace(m.Content), capBytes)
}

// Get implements plugins.SessionReader: one page of a saved session's
// messages, with absolute indices so the model can paginate deterministically.
// A non-empty query centers the page on the BM25-best-matching message.
func (a *sessionPluginAdapter) Get(_ context.Context, name string, offset, limit int, query string) (string, error) {
	if a.cli == nil || a.cli.sessionManager == nil {
		return "", fmt.Errorf("%s", i18n.T("session.tool.unavailable"))
	}
	if limit <= 0 {
		limit = getPageLimit
	}
	sm := a.cli.sessionManager

	if q := strings.TrimSpace(query); q != "" {
		all, _, err := sm.GetSessionMessages(name, 0, math.MaxInt32)
		if err != nil {
			return "", err
		}
		// System messages are masked out of the ranking: a stored system
		// prompt is huge and generic, so BM25 loved to center the page on it
		// — the model asked "what did we discuss?" and got its own prompt
		// back. Indices stay aligned because masking keeps positions.
		contents := make([]string, len(all))
		for i, m := range all {
			if m.Role == "system" {
				continue
			}
			contents[i] = m.Content
		}
		if hits := ctxmgr.RankDocsBM25(contents, q, 1); len(hits) > 0 {
			offset = hits[0].Index - limit/2
			if offset < 0 {
				offset = 0
			}
		}
	}

	page, total, err := sm.GetSessionMessages(name, offset, limit)
	if err != nil {
		return "", err
	}
	if len(page) == 0 {
		return i18n.T("session.tool.get.empty", name), nil
	}

	var b strings.Builder
	b.WriteString(i18n.T("session.tool.get.header", name, offset, offset+len(page)-1, total))
	b.WriteByte('\n')
	for i, m := range page {
		content := renderSessionMessage(m, getMessageCapFor(m.Role))
		fmt.Fprintf(&b, "\n[%d] %s: %s\n", offset+i, m.Role, content)
	}
	if next := offset + len(page); next < total {
		b.WriteByte('\n')
		b.WriteString(i18n.T("session.tool.get.next", next))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// GetMessage implements plugins.SessionMessageReader: one message, by
// absolute index, nearly whole — the read-in-full escape hatch after a
// paged Get showed a truncated entry the model actually needs.
func (a *sessionPluginAdapter) GetMessage(_ context.Context, name string, index int) (string, error) {
	if a.cli == nil || a.cli.sessionManager == nil {
		return "", fmt.Errorf("%s", i18n.T("session.tool.unavailable"))
	}
	all, total, err := a.cli.sessionManager.GetSessionMessages(name, 0, math.MaxInt32)
	if err != nil {
		return "", err
	}
	if index < 0 || index >= total {
		return "", fmt.Errorf("%s", i18n.T("session.tool.get.bad_index", index, total, name))
	}
	m := all[index]
	content := renderSessionMessage(m, getSingleMessageCap)
	header := i18n.T("session.tool.get.one_header", name, index, m.Role, total)
	return header + "\n\n" + content, nil
}

// truncateRunesafe cuts s at cap bytes, snapping back to a rune boundary so
// the output stays valid UTF-8, and appends an ellipsis when it cut.
// tailRunesafe returns the last capBytes of s without splitting a rune.
func tailRunesafe(s string, capBytes int) string {
	if len(s) <= capBytes {
		return s
	}
	i := len(s) - capBytes
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

func truncateRunesafe(s string, capBytes int) string {
	if len(s) <= capBytes {
		return s
	}
	cut := capBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// List returns the saved session names. Delegates to sessionListText, which
// is shared with the /session-list slash tool (slash_tool_registry.go).
func (a *sessionPluginAdapter) List(_ context.Context) (string, error) {
	if a.cli == nil {
		return "", fmt.Errorf("%s", i18n.T("session.tool.unavailable"))
	}
	return a.cli.sessionListText()
}

// ---------------------------------------------------------------------------
// Write capability (plugins.SessionWriter) — Phase 3 of session continuity.
//
// These methods run on the agent loop's goroutine, mid-turn, inside the tool
// batch. The plugin declares every write op non-concurrency-safe
// (builtin_session_caps.go), so the orchestrator serializes them with all
// other tool calls — that serialization, plus SessionManager's atomic
// temp+rename file writes, is the whole concurrency story; no extra locking,
// matching the read paths above and the /session handlers.
//
// The surface is non-destructive by design: no delete, no forced reload.
// ---------------------------------------------------------------------------

// checkSessionWrite validates a write-target: the store must be up and the
// name must not collide with the machine-owned namespaces (autosave-/mcp-
// files are rolling mirrors owned by the autosave paths, never live bindings
// — boundSessionName would silently ignore them; see cli_session_binding.go).
func (a *sessionPluginAdapter) checkSessionWrite(name string) error {
	if a.cli == nil || a.cli.sessionManager == nil {
		return fmt.Errorf("%s", i18n.T("session.tool.unavailable"))
	}
	// Captured server/gateway runs: currentSessionName holds a surface
	// session id there, the REPL write-through is deliberately inert
	// (rpcCaptureActive), and runLoopRPC restores the name on exit — a bind
	// made here would silently evaporate while confirming success to the
	// model. Refuse with a pointer to the surface's own binding controls.
	if rpcCaptureActive.Load() {
		return fmt.Errorf("session binding is managed by this surface, not by the @session tool: use the manage_session tool (MCP), the /session command (ACP), or /session in the chat channel (gateway)")
	}
	for _, p := range machineSessionPrefixes {
		if strings.HasPrefix(name, p) {
			return fmt.Errorf("%s", i18n.T("session.tool.write.reserved_name", name))
		}
	}
	return nil
}

// Save implements plugins.SessionWriter: persist the current conversation
// under name and bind to it, so write-through keeps it fresh from now on.
func (a *sessionPluginAdapter) Save(_ context.Context, name string) (string, error) {
	if err := a.checkSessionWrite(name); err != nil {
		return "", err
	}
	cli := a.cli
	if err := cli.sessionManager.SaveSessionV2(name, cli.buildSessionData()); err != nil {
		return "", err
	}
	cli.currentSessionName = name
	cli.boundRemoteOnly = false
	cli.stampBoundSession()
	// Tool-path mutation: a new/updated session file feeds the knowledge
	// graph's session nodes; the /session command tap never sees this path.
	cli.markGraphDirty()
	return i18n.T("session.tool.save.ok", name), nil
}

// Fork implements plugins.SessionWriter, mirroring handleForkSession: with a
// bound session the store file is forked; otherwise the in-memory history
// seeds the new session. Either way the binding moves to the fork.
func (a *sessionPluginAdapter) Fork(_ context.Context, name string) (string, error) {
	if err := a.checkSessionWrite(name); err != nil {
		return "", err
	}
	cli := a.cli
	if cli.currentSessionName != "" {
		if err := cli.sessionManager.ForkSession(cli.currentSessionName, name); err != nil {
			return "", err
		}
	} else {
		if err := cli.sessionManager.ForkCurrentToNew(name, cli.buildSessionData()); err != nil {
			return "", err
		}
	}
	cli.currentSessionName = name
	cli.boundRemoteOnly = false
	cli.stampBoundSession()
	// Tool-path mutation — same rationale as Save.
	cli.markGraphDirty()
	return i18n.T("session.tool.fork.ok", name), nil
}

// Attach implements plugins.SessionWriter's bind-only contract. When the
// named session exists and the in-memory conversation is EMPTY its history is
// loaded immediately; when the conversation already has messages we only
// record the binding — swapping the history mid-turn would yank the agent's
// own context out from under it, so convergence happens at the turn boundary
// instead (persistBoundSession / refreshBoundSession, last-writer-wins on the
// whole file; see cli_session_binding.go). A missing session is created
// lazily by the first write-through.
func (a *sessionPluginAdapter) Attach(_ context.Context, name string) (string, error) {
	if err := a.checkSessionWrite(name); err != nil {
		return "", err
	}
	cli := a.cli
	if !cli.sessionManager.SessionExists(name) {
		cli.currentSessionName = name
		cli.boundRemoteOnly = false
		cli.boundRemoteOnly = false
		return i18n.T("session.tool.attach.new", name), nil
	}
	sd, err := cli.sessionManager.LoadSessionV2(name)
	if err != nil {
		return "", err
	}
	if len(cli.history) == 0 {
		cli.restoreSessionData(sd)
		cli.currentSessionName = name
		cli.boundRemoteOnly = false
		cli.boundRemoteOnly = false
		cli.stampBoundSession()
		return i18n.T("session.tool.attach.loaded", name, len(cli.history)), nil
	}
	// Non-empty conversation attaching to an existing session: MERGE — the
	// target's history is prepended so the end-of-turn write-through writes
	// the union back. Bind-only here would make that write-through REPLACE
	// the file with the current conversation, silently destroying the target
	// session's content (this tool's contract is non-destructive).
	cli.history = append(append([]models.Message(nil), sd.ChatHistory...), cli.history...)
	cli.checkpoints = nil
	cli.currentSessionName = name
	cli.boundRemoteOnly = false
	cli.stampBoundSession()
	return i18n.T("session.tool.attach.bound", name), nil
}

// Detach implements plugins.SessionWriter: drop the binding. The conversation
// stays in memory; the saved session keeps whatever was last written through.
func (a *sessionPluginAdapter) Detach(_ context.Context) (string, error) {
	if a.cli == nil {
		return "", fmt.Errorf("%s", i18n.T("session.tool.unavailable"))
	}
	name := a.cli.currentSessionName
	if name == "" {
		return i18n.T("session.tool.detach.none"), nil
	}
	a.cli.currentSessionName = ""
	return i18n.T("session.tool.detach.ok", name), nil
}
