/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Memory flush before compaction. The memory worker extracts facts from the
 * live history a couple of turns behind the conversation; compaction
 * replaces the middle of that history with a summary, so anything the
 * worker had not reached yet was distilled from a summary at best and lost
 * at worst. Every compaction site now hands the not-yet-extracted segment
 * to the worker's durable queue first, so the original messages reach
 * long-term memory verbatim regardless of what the summary keeps.
 */
package cli

import (
	"context"

	"github.com/diillson/chatcli/models"
)

// unextractedSegment returns the live messages the memory worker has not
// processed yet (system messages excluded — they carry no episodic content).
func (cli *ChatCLI) unextractedSegment() []models.Message {
	if cli == nil || cli.memWorker == nil || cli.memWorker.store == nil {
		return nil
	}
	start := cli.memWorker.lastProcessedIdx
	if start < 0 || start >= len(cli.history) {
		return nil
	}
	seg := make([]models.Message, 0, len(cli.history)-start)
	for _, m := range cli.history[start:] {
		if m.Role == "system" || m.IsTurnContext() {
			continue // prompts and ChatCLI's own recall/date blocks are not conversation
		}
		seg = append(seg, m)
	}
	if len(seg) < 2 {
		return nil // a lone user turn carries nothing to distill
	}
	return seg
}

// flushMemoryBeforeCompaction queues the unextracted segment on the memory
// worker's WAL and triggers an extraction pass, then marks the segment as
// processed so the live-delta gate does not double-extract it.
func (cli *ChatCLI) flushMemoryBeforeCompaction(ctx context.Context) {
	seg := cli.unextractedSegment()
	if seg == nil {
		return
	}
	cli.memWorker.nudgeSegment(ctx, seg)
	cli.memWorker.lastProcessedIdx = len(cli.history)
}

// queueMemoryBeforeCompaction is the one-shot variant: the process exits
// right after its turn, so the segment is only queued for the next session.
func (cli *ChatCLI) queueMemoryBeforeCompaction() {
	seg := cli.unextractedSegment()
	if seg == nil {
		return
	}
	cli.memWorker.queueSegmentForNextSession(seg)
	cli.memWorker.lastProcessedIdx = len(cli.history)
}
