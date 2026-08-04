/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * cmd — rpcserve_session.go
 *
 * Cross-surface session continuity for the MCP/ACP servers: the binding
 * between a live surface session (an ACP session id, an MCP session
 * parameter) and a named saved session in the store shared with the REPL's
 * /session command.
 *
 * While bound, every completed turn is written through to the named session,
 * and every turn start adopts writes other surfaces (the interactive REPL, a
 * gateway daemon, another server) made since our last sync — the same
 * last-writer-wins model the REPL uses (see cli/cli_session_binding.go).
 *
 * This file also implements the per-session /session slash command the ACP
 * surface advertises: unlike the REPL handlers (which mutate process-global
 * state), it operates on the caller's live session and the shared store.
 */
package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/cli/rpcserve"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
)

// boundName returns the saved-session name the live session is bound to.
func (b *rpcBackend) boundName(session string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bindings[session]
}

// bindSession binds a live session to a saved-session name and stamps the
// sync watermark at the store's current mtime. A name with no store file yet
// (lazy attach) keeps a ZERO stamp — that is what tells writeThrough/refresh
// "this file has never existed" so creation is allowed and absence is not
// mistaken for an external delete.
func (b *rpcBackend) bindSession(session, name string) {
	var stamp time.Time
	if mt, err := b.store.SessionModTimeRPC(name); err == nil {
		stamp = mt
	}
	b.mu.Lock()
	if b.bindings == nil {
		b.bindings = map[string]string{}
		b.bindSync = map[string]time.Time{}
	}
	b.bindings[session] = name
	b.bindSync[session] = stamp
	b.mu.Unlock()
}

// unbindSession drops a live session's binding, if any.
func (b *rpcBackend) unbindSession(session string) {
	b.mu.Lock()
	delete(b.bindings, session)
	delete(b.bindSync, session)
	b.mu.Unlock()
}

// refreshBound adopts writes other surfaces made to the bound saved session
// since our last sync: when the store file is newer, it replaces the live
// history wholesale (last-writer-wins). No-op for unbound sessions. Best
// effort — a store failure never fails the turn.
func (b *rpcBackend) refreshBound(session string) {
	if b.store == nil {
		return
	}
	b.mu.Lock()
	name := b.bindings[session]
	stamp := b.bindSync[session]
	b.mu.Unlock()
	if name == "" {
		return
	}
	mt, err := b.store.SessionModTimeRPC(name)
	if err != nil {
		// Synced file gone = deleted by another surface → unbind (zero
		// stamp = never existed locally, the lazy-attach creation path).
		if os.IsNotExist(err) && !stamp.IsZero() {
			b.unbindSession(session)
		}
		return
	}
	if !mt.After(stamp) {
		return
	}
	hist, err := b.store.LoadSessionRPC(name)
	if err != nil {
		return
	}
	hist = capHistory(hist, historyCap(b.cli != nil))
	b.mu.Lock()
	b.sessions[session] = hist
	b.bindSync[session] = mt
	b.mu.Unlock()
}

// writeThrough persists a completed turn's history to the bound saved
// session, then re-stamps the sync watermark so our own write is not
// mistaken for another surface's. Best effort, like autosaveSession.
func (b *rpcBackend) writeThrough(session string, hist []models.Message) {
	if b.store == nil || len(hist) == 0 {
		return
	}
	b.mu.Lock()
	name := b.bindings[session]
	stamp := b.bindSync[session]
	b.mu.Unlock()
	if name == "" {
		return
	}
	// A session we had synced with that no longer exists was deleted by
	// another surface: unbind instead of resurrecting the file (a zero
	// stamp means it never existed — the lazy-attach creation path).
	if !stamp.IsZero() && !b.store.SessionExistsRPC(name) {
		b.unbindSession(session)
		return
	}
	if err := b.store.SaveSessionRPC(name, hist); err != nil {
		return
	}
	stamp = time.Now()
	if mt, err := b.store.SessionModTimeRPC(name); err == nil {
		stamp = mt
	}
	b.mu.Lock()
	b.bindSync[session] = stamp
	b.mu.Unlock()
}

// runLoopSession wraps an agent/coder run with the per-session conversation:
// refresh the bound session, hand the live history to the loop, store the
// updated conversation back and persist it (autosave mirror + write-through).
// The updated history is persisted even when the run errors: tool calls that
// already happened are part of the conversation.
func (b *rpcBackend) runLoopSession(session string, run func(o cli.RPCRunOpts) (string, error), opts rpcserve.RunOpts) (string, error) {
	b.refreshBound(session)
	b.mu.Lock()
	hist := append([]models.Message(nil), b.sessions[session]...)
	b.mu.Unlock()

	var updated []models.Message
	o := toRunOpts(session, opts)
	o.History = hist
	o.HistoryOut = &updated

	reply, err := run(o)

	if len(updated) > 0 {
		newHist := capHistory(updated, historyCap(b.cli != nil))
		b.mu.Lock()
		b.sessions[session] = newHist
		b.mu.Unlock()
		b.autosaveSession(session, newHist)
		b.writeThrough(session, newHist)
	}
	return reply, err
}

// runSessionCommand is the per-session /session dispatcher for the ACP/MCP
// slash-command surface. args carries the tokens after "/session". Output is
// user-facing (i18n), unlike ManageSession's model-facing English.
func (b *rpcBackend) runSessionCommand(ctx context.Context, session string, args []string) (string, error) {
	if b.store == nil {
		return "", errCLIUnavailable
	}
	if len(args) == 0 {
		return strings.Join([]string{
			i18n.T("session.usage_header"),
			i18n.T("session.usage_save"),
			i18n.T("session.usage_load"),
			i18n.T("session.usage_attach"),
			i18n.T("session.usage_detach"),
			i18n.T("session.usage_status"),
			i18n.T("session.usage_list"),
			i18n.T("session.usage_search"),
			i18n.T("session.usage_delete"),
			i18n.T("session.usage_new"),
			i18n.T("session.usage_fork"),
		}, "\n"), nil
	}
	sub := args[0]
	var name string
	if len(args) > 1 {
		name = args[1]
	}

	switch sub {
	case "save":
		if name == "" {
			return i18n.T("session.error_name_required_save"), nil
		}
		b.mu.Lock()
		hist := append([]models.Message(nil), b.sessions[session]...)
		b.mu.Unlock()
		if len(hist) == 0 {
			return i18n.T("session.rpc.nothing_to_save"), nil
		}
		if err := b.store.SaveSessionRPC(name, hist); err != nil {
			return "", err
		}
		b.bindSession(session, name)
		return i18n.T("session.save_success", name), nil

	case "load", "attach":
		if name == "" {
			return i18n.T("session.error_name_required_load"), nil
		}
		if !b.store.SessionExistsRPC(name) {
			if sub != "attach" {
				return "", errCLI(i18n.T("session.rpc.not_found", name))
			}
			// attach to a not-yet-existing session: bind now, keeping the
			// live conversation as the seed — the file is born from it on
			// the first written-through turn.
			b.bindSession(session, name)
			b.rotateHub(ctx)
			return i18n.T("session.rpc.attached_new", name), nil
		}
		hist, err := b.store.LoadSessionRPC(name)
		if err != nil {
			return "", err
		}
		hist = capHistory(hist, historyCap(b.cli != nil))
		b.mu.Lock()
		b.sessions[session] = hist
		b.mu.Unlock()
		b.bindSession(session, name)
		// A loaded session is a different thread: rotate the shared hub
		// conversation so its backlog is not spliced on top of it.
		b.rotateHub(ctx)
		return i18n.T("session.load_success", name), nil

	case "detach":
		if b.boundName(session) == "" {
			return i18n.T("session.detach_none"), nil
		}
		prev := b.boundName(session)
		b.unbindSession(session)
		return i18n.T("session.detached", prev), nil

	case "status":
		b.mu.Lock()
		count := len(b.sessions[session])
		b.mu.Unlock()
		if name := b.boundName(session); name != "" {
			return i18n.T("session.status_bound", name) + "\n" + i18n.T("session.rpc.live_messages", count), nil
		}
		return i18n.T("session.status_unbound") + "\n" + i18n.T("session.rpc.live_messages", count), nil

	case "list":
		names, err := b.store.ListSessionsRPC()
		if err != nil {
			return "", err
		}
		if len(names) == 0 {
			return i18n.T("session.list_empty"), nil
		}
		return i18n.T("session.list_header") + "\n- " + strings.Join(names, "\n- "), nil

	case "search":
		query := strings.TrimSpace(strings.Join(args[1:], " "))
		if query == "" {
			return i18n.T("session.search.usage"), nil
		}
		if b.cli == nil {
			return "", errCLIUnavailable
		}
		return b.cli.SearchSessionsRPC(query)

	case "delete":
		if name == "" {
			return i18n.T("session.error_name_required_delete"), nil
		}
		if err := b.store.DeleteSessionRPC(name); err != nil {
			return "", err
		}
		// Drop any live binding to the deleted name: writing a deleted
		// session back into existence would resurrect it silently.
		b.mu.Lock()
		for id, bound := range b.bindings {
			if bound == name {
				delete(b.bindings, id)
				delete(b.bindSync, id)
			}
		}
		b.mu.Unlock()
		return i18n.T("session.delete_success", name), nil

	case "new":
		b.mu.Lock()
		hist := b.sessions[session]
		delete(b.sessions, session)
		b.mu.Unlock()
		// Same last-chance autosave contract as ManageSession's clear.
		b.autosaveSession(session, hist)
		b.unbindSession(session)
		b.rotateHub(ctx)
		return i18n.T("session.new_session_started"), nil

	case "fork":
		if name == "" {
			return i18n.T("cmd.core.session_fork_usage"), nil
		}
		if b.cli == nil {
			return "", errCLIUnavailable
		}
		if source := b.boundName(session); source != "" {
			if err := b.cli.ForkSessionRPC(source, name); err != nil {
				return "", err
			}
		} else {
			// No bound source: fork the live in-memory conversation.
			b.mu.Lock()
			hist := append([]models.Message(nil), b.sessions[session]...)
			b.mu.Unlock()
			if len(hist) == 0 {
				return i18n.T("session.rpc.nothing_to_save"), nil
			}
			if err := b.store.SaveSessionRPC(name, hist); err != nil {
				return "", err
			}
		}
		b.bindSession(session, name)
		return i18n.T("session.rpc.forked", name), nil

	default:
		return i18n.T("session.unknown_command", sub), nil
	}
}

// rotateHub rotates the shared hub conversation thread (no-op without hub).
func (b *rpcBackend) rotateHub(ctx context.Context) {
	if b.cli != nil {
		b.cli.RotateHubThreadRPC(ctx)
	}
}

// RestoreSession implements rpcserve.ACPSessionRestoreBackend: bring a prior
// session id back to life for ACP session/load. Preference order: the live
// in-process session (server not restarted), then the rolling autosave
// mirror ("mcp-<id>") the turn path maintains — which is what survives a
// server restart.
func (b *rpcBackend) RestoreSession(_ context.Context, session string) ([]rpcserve.HistoryItem, error) {
	b.mu.Lock()
	hist := append([]models.Message(nil), b.sessions[session]...)
	b.mu.Unlock()

	if len(hist) == 0 {
		if b.store == nil {
			return nil, errCLIUnavailable
		}
		mirrored, err := b.store.LoadSessionRPC("mcp-" + session)
		if err != nil {
			return nil, errCLI(fmt.Sprintf("unknown session %q — no live state and no autosave mirror for it", session))
		}
		hist = capHistory(mirrored, historyCap(b.cli != nil))
		b.mu.Lock()
		b.sessions[session] = hist
		b.mu.Unlock()
	}

	items := make([]rpcserve.HistoryItem, 0, len(hist))
	for _, m := range hist {
		// Replay only the conversational turns: system prompts, raw tool
		// results and tool-call envelopes are engine plumbing — replaying
		// them as chat bubbles floods the editor with internal noise.
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		if len(m.ToolCalls) > 0 || strings.TrimSpace(m.Content) == "" {
			continue
		}
		items = append(items, rpcserve.HistoryItem{Role: m.Role, Content: m.Content})
	}
	return items, nil
}
