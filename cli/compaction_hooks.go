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

// firePreCompact runs the PreCompact hooks synchronously (they may want to
// snapshot the history before it changes).
func (cli *ChatCLI) firePreCompact(ctx context.Context, trigger string) {
	if cli == nil || cli.hookManager == nil {
		return
	}
	cli.hookManager.Fire(ctx, cli.compactionEvent(hooks.EventPreCompact, trigger))
}

// firePostCompact runs the PostCompact hooks detached from the turn's
// deadline (the turn must not wait on them).
func (cli *ChatCLI) firePostCompact(ctx context.Context, trigger string) {
	if cli == nil || cli.hookManager == nil {
		return
	}
	cli.hookManager.FireAsync(context.WithoutCancel(ctx), cli.compactionEvent(hooks.EventPostCompact, trigger))
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
	key, ok := layer.Archive(renderMessagesForArchive(dropped))
	if !ok {
		return ""
	}
	return "[full transcript of the messages dropped by context recovery recoverable via @recall " + compress.FormatMarker(key) + "]"
}
