/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ChatCLI - memory_notice.go
 *
 * Surfaces background memory activity to the user. The memory worker runs
 * on its own goroutine and must not write to stdout directly (it would
 * corrupt go-prompt's line redraw). Instead it queues a one-line notice
 * here; the main loop drains and prints it at the next executor tick.
 */
package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/i18n"
)

// pushMemoryNotice queues a one-line notice for display at the next
// executor tick. Safe to call from the background worker goroutine.
func (cli *ChatCLI) pushMemoryNotice(msg string) {
	if msg == "" {
		return
	}
	cli.memNoticeMu.Lock()
	cli.memNotices = append(cli.memNotices, msg)
	cli.memNoticeMu.Unlock()
}

// drainMemoryNotices prints and clears any queued notices. It runs on the
// main loop so writes never race with go-prompt's redraw.
func (cli *ChatCLI) drainMemoryNotices() {
	cli.memNoticeMu.Lock()
	notices := cli.memNotices
	cli.memNotices = nil
	cli.memNoticeMu.Unlock()

	for _, n := range notices {
		fmt.Println(colorize("  "+n, ColorGray))
	}
}

// formatMemoryNotice renders an ExtractionSummary as a compact one-liner,
// e.g. "memory: +2 facts, profile updated, +1 project". Returns "" when
// nothing user-visible was persisted (a lone daily-note write is noise).
func formatMemoryNotice(s memory.ExtractionSummary) string {
	var parts []string
	if s.FactsAdded > 0 {
		parts = append(parts, i18n.T("mem.notice.facts", s.FactsAdded))
	}
	if s.ProfileUpdated {
		parts = append(parts, i18n.T("mem.notice.profile"))
	}
	if s.ProjectsUpserted > 0 {
		parts = append(parts, i18n.T("mem.notice.projects", s.ProjectsUpserted))
	}
	if s.TopicsRecorded > 0 {
		parts = append(parts, i18n.T("mem.notice.topics", s.TopicsRecorded))
	}
	if len(parts) == 0 {
		return ""
	}
	return i18n.T("mem.notice.prefix") + " " + strings.Join(parts, ", ")
}
