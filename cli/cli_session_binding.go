/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * cli_session_binding.go
 *
 * Cross-surface session continuity for the interactive REPL. Once a named
 * session is active (loaded or saved via /session), every completed turn is
 * written through to the saved-session store, and every turn start checks the
 * store for writes made by another surface (MCP/ACP server, gateway daemon,
 * another terminal) since our last sync — reloading when the file is newer.
 *
 * Conflict model is last-writer-wins on the whole file: the store's atomic
 * write (temp+rename) guarantees no torn files, and the refresh-before-turn
 * keeps surfaces converging as long as they alternate. Two surfaces answering
 * the SAME turn simultaneously is the conversation hub's job, not this one's.
 *
 * Guards, in order:
 *   - CHATCLI_SESSION_WRITETHROUGH=off disables both directions;
 *   - no named session active (currentSessionName empty) → no-op;
 *   - machine-prefixed names (autosave-, mcp-) are rolling mirrors owned by
 *     the autosave paths, never live bindings;
 *   - captured RPC runs (MCP/ACP/gateway/scheduler-bridge) are skipped: the
 *     RPC backend owns per-session persistence there, and currentSessionName
 *     holds a surface session id, not a saved-session name.
 */
package cli

import (
	"context"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
)

// sessionWritethroughEnabled gates the live write-through/refresh of the
// active named session. Default ON: continuity should "just work"; operators
// opt out with CHATCLI_SESSION_WRITETHROUGH=false.
func sessionWritethroughEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_SESSION_WRITETHROUGH"))) {
	case "false", "0", "off", "no", "disabled":
		return false
	}
	return true
}

// boundSessionName returns the saved-session name the REPL is bound to, or
// "" when no live binding applies (no named session, machine mirror name, or
// an in-flight captured RPC run whose session id merely borrows the field).
func (cli *ChatCLI) boundSessionName() string {
	if rpcCaptureActive.Load() {
		return ""
	}
	name := cli.currentSessionName
	if name == "" {
		return ""
	}
	// The user explicitly chose remote-only storage for this session:
	// creating a silent local copy would defeat that choice.
	if cli.boundRemoteOnly {
		return ""
	}
	for _, p := range machineSessionPrefixes {
		if strings.HasPrefix(name, p) {
			return ""
		}
	}
	return name
}

// markBoundSessionSynced stamps "our view matches the store as of now" —
// called after every load, save or write-through so the next refresh only
// fires on writes made by OTHER surfaces.
func (cli *ChatCLI) markBoundSessionSynced(name string) {
	if mt, err := cli.sessionManager.SessionModTime(name); err == nil {
		cli.boundSessionSync = mt
		return
	}
	// No local file yet (lazy attach, or a remote-only save/load): a zero
	// stamp records "never synced with a local file", which both keeps
	// refresh from treating the absence as an external delete and lets the
	// first write-through establish the real watermark.
	cli.boundSessionSync = time.Time{}
}

// stampBoundSession refreshes the sync watermark for the currently bound
// session, if any. Called after /session save|load|fork so the next
// refreshBoundSession only reacts to writes made by OTHER surfaces.
func (cli *ChatCLI) stampBoundSession() {
	if cli.sessionManager == nil {
		return
	}
	if name := cli.boundSessionName(); name != "" {
		cli.markBoundSessionSynced(name)
	}
}

// rotateHubThread rotates the shared cross-channel hub conversation to a
// fresh thread. Used when the local conversation changes identity (/session
// new, /session load): splicing the old thread's backlog on top of a freshly
// loaded session would interleave two different conversations.
func (cli *ChatCLI) rotateHubThread(ctx context.Context) {
	if cli.hubSync == nil {
		return
	}
	if err := cli.hubSync.newSession(ctx); err != nil {
		cli.logger.Warn("hub sync: thread rotation failed", zap.Error(err))
	}
}

// persistBoundSession writes the live history through to the active named
// session. Best-effort: a store failure must never fail the turn (the
// conversation still lives in memory and in the exit autosave).
func (cli *ChatCLI) persistBoundSession() {
	if !sessionWritethroughEnabled() || cli.sessionManager == nil {
		return
	}
	name := cli.boundSessionName()
	if name == "" {
		return
	}
	// A session we had synced with that no longer exists was deleted by
	// another surface mid-turn: unbind instead of resurrecting the file.
	if !cli.boundSessionSync.IsZero() && !cli.sessionManager.SessionExists(name) {
		cli.logger.Warn("bound session deleted by another surface; detaching",
			zap.String("session", name))
		cli.currentSessionName = ""
		cli.boundSessionSync = time.Time{}
		return
	}
	if err := cli.sessionManager.SaveSessionV2(name, cli.buildSessionData()); err != nil {
		cli.logger.Warn("session write-through failed", zap.String("session", name), zap.Error(err))
		return
	}
	cli.markBoundSessionSynced(name)
}

// refreshBoundSession reloads the active named session when another surface
// wrote it since our last sync. Called at turn start, on the turn's goroutine.
// Last-writer-wins: the store file replaces the in-memory history wholesale.
func (cli *ChatCLI) refreshBoundSession() {
	if !sessionWritethroughEnabled() || cli.sessionManager == nil {
		return
	}
	name := cli.boundSessionName()
	if name == "" {
		return
	}
	mt, err := cli.sessionManager.SessionModTime(name)
	if err != nil {
		// The file we had synced with disappeared: another surface deleted
		// the session. Unbind instead of letting the next write-through
		// silently resurrect it (a zero stamp means it never existed
		// locally — lazy attach / remote-only — and is not a delete).
		if os.IsNotExist(err) && !cli.boundSessionSync.IsZero() {
			cli.logger.Warn("bound session deleted by another surface; detaching",
				zap.String("session", name))
			cli.currentSessionName = ""
			cli.boundSessionSync = time.Time{}
		}
		return
	}
	if !mt.After(cli.boundSessionSync) {
		return
	}
	sd, err := cli.sessionManager.LoadSessionV2(name)
	if err != nil {
		cli.logger.Warn("session refresh failed", zap.String("session", name), zap.Error(err))
		return
	}
	cli.restoreSessionData(sd)
	cli.boundSessionSync = mt
}
