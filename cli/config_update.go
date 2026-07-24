/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /config update renders the auto-update state: resolved policy mode,
 * detected install channel, current vs. latest release (from the on-disk
 * cache — no network on render) and the env vars that steer the updater.
 */
package cli

import (
	"fmt"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/update"
	"github.com/diillson/chatcli/version"
)

// showConfigUpdate renders the auto-update section.
func (cli *ChatCLI) showConfigUpdate() {
	sectionHeader("🔄", "cfg.section.update.title", ColorCyan)
	p := uiPrefix(ColorCyan)

	info := detectInstallFn()
	rep := version.OfflineReport()

	kv(p, i18n.T("cfg.kv.update.mode"), update.ResolveMode().String())
	kv(p, i18n.T("cfg.kv.update.method"), methodLabel(info.Method))
	kv(p, i18n.T("cfg.kv.update.current"), rep.Current.Version)
	if rep.Latest != "" {
		kv(p, i18n.T("cfg.kv.update.latest"), rep.Latest)
	}
	if rep.NeedsUpdate {
		kv(p, i18n.T("cfg.kv.update.status"), i18n.T("cfg.val.update_available"))
	}

	fmt.Println(p)
	subheader(p, "cfg.sub.update.env")
	kv(p, "CHATCLI_AUTO_UPDATE", envOr("CHATCLI_AUTO_UPDATE"))
	kv(p, "CHATCLI_DISABLE_VERSION_CHECK", envBool("CHATCLI_DISABLE_VERSION_CHECK"))
	kv(p, "CHATCLI_LATEST_VERSION_URL", envOr("CHATCLI_LATEST_VERSION_URL"))

	sectionEnd(ColorCyan)
}
