/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /config memory renders the long-term memory configuration: the injection
 * mode that governs the push/pull tradeoff in agent/coder, plus live store
 * stats. Read-only, mirroring /config quality — set the mode via the
 * CHATCLI_MEMORY_MODE env var (full | index | off).
 */
package cli

import (
	"fmt"
	"os"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
)

// showConfigMemory renders the /config memory section.
func (cli *ChatCLI) showConfigMemory() {
	sectionHeader("🧠", "cfg.section.memory.title", ColorBlue)
	p := uiPrefix(ColorBlue)

	subheader(p, "cfg.sub.memory.injection")
	mode := loadMemoryMode()
	desc := i18n.T("cfg.kv.memory.mode_" + mode)
	// Tag the value as the runtime default when the env var is unset so the
	// operator can tell a configured value from the fallback.
	if os.Getenv("CHATCLI_MEMORY_MODE") == "" {
		desc = defaultMarker + desc
	}
	kv(p, "CHATCLI_MEMORY_MODE", desc)
	kv(p, "CHATCLI_MEMORY_ENABLED", envBool("CHATCLI_MEMORY_ENABLED"))

	if cli.memoryStore != nil {
		if idx := cli.memoryStore.GetMemoryIndex(0); idx != "" {
			kv(p, i18n.T("cfg.kv.memory.index_size"), fmt.Sprintf("%d bytes", len(idx)))
		}
		if longTerm := cli.memoryStore.ReadLongTerm(); longTerm != "" {
			kv(p, i18n.T("cfg.kv.long_term_size"), fmt.Sprintf("%d bytes", len(longTerm)))
		}
	} else {
		kv(p, i18n.T("cfg.kv.memory_store"), i18n.T("cfg.val.not_initialized"))
	}

	// Self-evolution has its own section (/config selfevolve); a one-line
	// pointer keeps it discoverable from here since it shares this worker.
	kv(p, i18n.T("cfg.kv.selfevolve.see_section"), i18n.T("cfg.kv.selfevolve.see_section_val"))

	// Knowledge graph: per-turn map-of-content injection (the @memory neighbors
	// pull is always available regardless).
	subheader(p, "cfg.sub.graph")
	graphVal := i18n.T("cfg.val.disabled")
	if graphIndexEnabled() {
		graphVal = i18n.T("cfg.val.enabled")
	}
	if os.Getenv(config.GraphIndexEnv) == "" {
		graphVal = defaultMarker + graphVal
	}
	kv(p, config.GraphIndexEnv, graphVal)

	sectionEnd(ColorBlue)
}
