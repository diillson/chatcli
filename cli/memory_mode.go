/*
 * ChatCLI - Memory injection mode (push vs pull).
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Controls how long-term memory reaches the model in agent/coder mode:
 *
 *   full  — inject the full hint-driven retrieval every turn (the push
 *           model; cost grows with memory size and is paid each turn).
 *   index — inject only a small, stable digest of what memory knows and
 *           let the agent pull detail on demand via the @memory recall
 *           tool (the pull model; per-turn cost is bounded regardless of
 *           how large memory grows). Default.
 *   off   — inject no memory (bootstrap files still apply).
 *
 * Chat mode is tool-less by design, so it cannot pull: there "index"
 * degrades to "full" and only "off" suppresses memory.
 */
package cli

import (
	"os"
	"strings"
)

const (
	memModeFull  = "full"
	memModeIndex = "index"
	memModeOff   = "off"

	// defaultMemoryMode is the production default: pull-first, so per-turn
	// token cost stays bounded as memory grows.
	defaultMemoryMode = memModeIndex
)

// memoryRecallHint is appended to the stable index block in "index" mode. It
// is an English constant (not i18n) because it is an instruction to the model,
// which follows English directives most reliably — the same rationale behind
// the other system-prompt constants.
const memoryRecallHint = "Only a compact index of long-term memory is shown above to save tokens. " +
	"When you need the full detail behind any profile attribute, topic, or project, " +
	`call the @memory tool with {"cmd":"recall","args":{"query":"<topic>"}} to pull the relevant facts on demand.`

// loadMemoryMode reads CHATCLI_MEMORY_MODE, normalizing to one of the three
// valid modes and falling back to the production default on empty/unknown.
func loadMemoryMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_MEMORY_MODE"))) {
	case memModeFull:
		return memModeFull
	case memModeIndex:
		return memModeIndex
	case memModeOff:
		return memModeOff
	default:
		return defaultMemoryMode
	}
}
