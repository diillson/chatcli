/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * One seam for everything that surrounds a compaction, whichever loop
 * runs it (chat, agent/coder, one-shot, /compact, overflow recovery):
 * the PreCompact/PostCompact hooks with their trigger, the cost record,
 * the cache-rebuild note. Keeping the call sites to two lines is what
 * makes the four loops behave the same.
 */
package cli

import (
	"context"
	"os"
	"time"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/cli/hooks"
	"github.com/diillson/chatcli/models"
)

// Compaction triggers reported to hooks (CHATCLI_HOOK_TRIGGER).
const (
	compactTriggerAuto     = "auto"
	compactTriggerManual   = "manual"
	compactTriggerRecovery = "recovery"
)

// beforeCompaction runs right before any rewrite of the history: it keeps
// the pre-compaction history for /rewind compact and runs the PreCompact
// hooks synchronously (they may want to snapshot the transcript too).
func (cli *ChatCLI) beforeCompaction(ctx context.Context, trigger string) {
	if cli == nil {
		return
	}
	cli.rememberPreCompaction()
	if cli.hookManager == nil {
		return
	}
	cli.hookManager.Fire(ctx, cli.compactionEvent(hooks.EventPreCompact, trigger))
}

// Compaction outcomes reported to hooks (CHATCLI_HOOK_OUTCOME).
const (
	compactOutcomeApplied = "applied"
	compactOutcomeSkipped = "skipped"
)

// firePostCompact runs the PostCompact hooks detached from the turn's
// deadline (the turn must not wait on them), reporting outcome "applied".
func (cli *ChatCLI) firePostCompact(ctx context.Context, trigger string) {
	cli.firePostCompactOutcome(ctx, trigger, compactOutcomeApplied)
}

func (cli *ChatCLI) firePostCompactOutcome(ctx context.Context, trigger, outcome string) {
	if cli == nil || cli.hookManager == nil {
		return
	}
	ev := cli.compactionEvent(hooks.EventPostCompact, trigger)
	ev.Outcome = outcome
	cli.hookManager.FireAsync(context.WithoutCancel(ctx), ev)
}

// compactionSkipped closes a compaction that changed nothing (no-op,
// rejected summary, failure): the undo snapshot beforeCompaction pushed is
// popped — /rewind compact must never "restore" an identical history and
// push a bogus checkpoint — and the PreCompact hook gets its paired
// PostCompact with outcome "skipped".
func (cli *ChatCLI) compactionSkipped(ctx context.Context, trigger string) {
	if cli == nil {
		return
	}
	cli.dropPreCompactionSnapshot()
	cli.firePostCompactOutcome(ctx, trigger, compactOutcomeSkipped)
}

// historiesEqual reports whether two histories carry the same messages.
func historiesEqual(a, b []models.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || a[i].Content != b[i].Content || a[i].ToolCallID != b[i].ToolCallID || len(a[i].ToolCalls) != len(b[i].ToolCalls) {
			return false
		}
	}
	return true
}

// afterHistoryRestore is the shared bookkeeping of every history restore
// (/rewind checkpoint, /rewind compact): the journal records the rewrite,
// the cache telemetry expects a rebuild, and the undo stack is cleared —
// its snapshots belong to the timeline that was just left.
func (cli *ChatCLI) afterHistoryRestore() {
	if cli == nil {
		return
	}
	cli.syncTranscript()
	if cli.costTracker != nil {
		cli.costTracker.NoteExpectedCacheRebuild()
	}
	cli.preCompaction = nil
}

func (cli *ChatCLI) compactionEvent(typ hooks.EventType, trigger string) hooks.HookEvent {
	wd, _ := os.Getwd()
	return hooks.HookEvent{
		Type:       typ,
		Timestamp:  time.Now(),
		SessionID:  cli.currentSessionName,
		WorkingDir: wd,
		Trigger:    trigger,
	}
}

// noteCompactionApplied is the post-compaction bookkeeping shared by every
// loop: cost record from the compactor's report, cache-rebuild note for
// the cache telemetry, PostCompact hooks.
func (cli *ChatCLI) noteCompactionApplied(ctx context.Context, trigger string) {
	cli.refreshBootstrapCard()
	if cli == nil {
		return
	}
	if cli.historyCompactor != nil {
		cli.costTracker.RecordCompaction(cli.historyCompactor.LastReport())
	}
	cli.costTracker.NoteExpectedCacheRebuild()
	cli.firePostCompact(ctx, trigger)
}

// archiveDroppedMessages archives to CCR the messages of before that are
// absent from after (overflow recovery drops whole messages without a
// summary). Returns the recall marker note, empty when nothing was
// archived.
func archiveDroppedMessages(layer *compress.Layer, before, after []models.Message) string {
	if layer == nil || len(before) == 0 {
		return ""
	}
	kept := make(map[string]int, len(after))
	for _, m := range after {
		kept[m.Role+"\x00"+m.Content]++
	}
	dropped := make([]models.Message, 0, len(before))
	for _, m := range before {
		key := m.Role + "\x00" + m.Content
		if kept[key] > 0 {
			kept[key]--
			continue
		}
		if m.Role == "system" {
			continue
		}
		dropped = append(dropped, m)
	}
	if len(dropped) == 0 {
		return ""
	}
	key, ok := layer.Archive(persistRedact(renderMessagesForArchive(dropped)))
	if !ok {
		return ""
	}
	return "[full transcript of the messages dropped by context recovery recoverable via @recall " + compress.FormatMarker(key) + "]"
}
