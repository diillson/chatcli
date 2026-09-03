/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Undo for compaction and persisted /rewind checkpoints.
 *
 * Every rewrite of the history (auto-compact, /compact, overflow
 * recovery, /clear) is preceded by a snapshot of what it replaces;
 * /rewind compact puts that history back. The snapshot lives in memory
 * for the running session and, after a resume, is rebuilt from the
 * transcript journal: rewrite events carry the ordered hashes of the
 * replaced history and the journal holds every message ever seen.
 * /rewind checkpoints persist with the session the same way (hash lists
 * resolved against the journal on load).
 */
package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
)

// maxPreCompactionSnapshots bounds the in-memory undo stack.
const maxPreCompactionSnapshots = 5

// rememberPreCompaction pushes a copy of the current history on the undo
// stack (called right before any rewrite).
func (cli *ChatCLI) rememberPreCompaction() {
	if cli == nil || len(cli.history) == 0 {
		return
	}
	snap := make([]models.Message, len(cli.history))
	copy(snap, cli.history)
	cli.preCompaction = append(cli.preCompaction, snap)
	if len(cli.preCompaction) > maxPreCompactionSnapshots {
		cli.preCompaction = cli.preCompaction[len(cli.preCompaction)-maxPreCompactionSnapshots:]
	}
}

// errNoCompactionToUndo marks a /rewind compact with nothing to restore.
var errNoCompactionToUndo = errors.New("no compaction to undo")

// preCompactionHistory returns the most recent pre-rewrite history: the
// in-memory snapshot when the rewrite happened in this process, else the
// journal's latest rewrite event resolved against its messages.
func (cli *ChatCLI) preCompactionHistory() ([]models.Message, string, error) {
	if n := len(cli.preCompaction); n > 0 {
		snap := cli.preCompaction[n-1]
		cli.preCompaction = cli.preCompaction[:n-1]
		return snap, "memory", nil
	}
	events, err := cli.transcriptEvents()
	if err != nil {
		return nil, "", errNoCompactionToUndo
	}
	idx := transcriptIndex(events)
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind != "rewrite" || len(ev.Hashes) == 0 {
			continue
		}
		if history, ok := resolveHashes(idx, ev.Hashes); ok {
			return history, "journal", nil
		}
		break
	}
	return nil, "", errNoCompactionToUndo
}

// undoCompaction is /rewind compact: restores the history the most recent
// rewrite replaced. The current history is kept as a checkpoint so the
// undo itself can be undone with /rewind.
func (cli *ChatCLI) undoCompaction() bool {
	history, source, err := cli.preCompactionHistory()
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("rewind.compact.none"), ColorGray))
		return false
	}
	before := len(cli.history)
	cli.saveCheckpoint()
	cli.history = history
	cli.syncTranscript()
	if cli.costTracker != nil {
		cli.costTracker.NoteExpectedCacheRebuild()
	}
	fmt.Printf("  %s %s\n", colorize("↩", ColorGreen), i18n.T("rewind.compact.restored", before, len(cli.history), source))
	return true
}

// handleRewindCommand dispatches /rewind [compact].
func (cli *ChatCLI) handleRewindCommand(userInput string) {
	arg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(userInput), "/rewind"))
	switch strings.ToLower(arg) {
	case "":
		cli.showRewindMenu()
	case "compact", "compaction", "undo":
		cli.undoCompaction()
	default:
		fmt.Println(colorize("  "+i18n.T("rewind.usage"), ColorYellow))
	}
}

// checkpointRecords renders the in-memory checkpoints for persistence.
func (cli *ChatCLI) checkpointRecords() []models.SessionCheckpoint {
	if cli == nil || len(cli.checkpoints) == 0 {
		return nil
	}
	out := make([]models.SessionCheckpoint, 0, len(cli.checkpoints))
	for _, cp := range cli.checkpoints {
		rec := models.SessionCheckpoint{Timestamp: cp.Timestamp, Label: cp.Label, Hashes: make([]string, len(cp.History))}
		for i := range cp.History {
			rec.Hashes[i] = messageHash(cp.History[i])
		}
		out = append(out, rec)
	}
	return out
}

// restoreCheckpoints rebuilds checkpoints from persisted records using
// the transcript journal; records whose messages are gone are dropped.
func (cli *ChatCLI) restoreCheckpoints(recs []models.SessionCheckpoint) []conversationCheckpoint {
	if len(recs) == 0 {
		return nil
	}
	events, err := cli.transcriptEvents()
	if err != nil {
		return nil
	}
	idx := transcriptIndex(events)
	out := make([]conversationCheckpoint, 0, len(recs))
	for _, rec := range recs {
		history, ok := resolveHashes(idx, rec.Hashes)
		if !ok {
			continue
		}
		ts := rec.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		out = append(out, conversationCheckpoint{Timestamp: ts, Label: rec.Label, History: history, MsgCount: len(history)})
	}
	if len(out) > maxCheckpoints {
		out = out[len(out)-maxCheckpoints:]
	}
	return out
}
