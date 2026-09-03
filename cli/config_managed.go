/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /config managed — the organization-managed defaults and locked
 * policies in effect (config/managed.go).
 */
package cli

import (
	"fmt"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
)

// showConfigManaged renders the managed file, its entries and which ones
// are locked.
func (cli *ChatCLI) showConfigManaged() {
	sectionHeader("🏢", "cfg.section.managed", ColorCyan)
	p := uiPrefix(ColorCyan)
	path, present, entries := config.ManagedState()
	kv(p, "CHATCLI_MANAGED_CONFIG", envOr("CHATCLI_MANAGED_CONFIG"))
	kv(p, i18n.T("cfg.managed.path"), path)
	if !present {
		fmt.Println(p + colorize("  "+i18n.T("cfg.managed.absent"), ColorGray))
		fmt.Println(p + colorize("  "+i18n.T("cfg.managed.hint"), ColorGray))
		return
	}
	subheader(p, "cfg.sub.managed.entries")
	for _, e := range entries {
		val := managedMarker + e.Value
		if e.Locked {
			val = managedLockedMarker + e.Value
		}
		kv(p, e.Key, val)
	}
	fmt.Println(p + colorize("  "+i18n.T("cfg.managed.precedence"), ColorGray))
}
