/*
 * ChatCLI - /config ui (read-only section).
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The read-only counterpart to config_ui_mutate.go: the section dump shown by
 * `/config ui` (with no subcommand) and folded into `/config all`. It reports
 * the active theme, the env var backing it, the detected terminal color
 * profile, and the list of available themes — so an operator can see what is
 * driving the UI's colors without reading source.
 */
package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/theme"
)

// showConfigUI prints the UI / theme section.
func (cli *ChatCLI) showConfigUI() {
	sectionHeader("🎨", "cfg.section.ui.title", ColorPurple)
	p := uiPrefix(ColorPurple)

	kv(p, config.ThemeEnv, envOr(config.ThemeEnv))
	kv(p, i18n.T("cfg.kv.theme_active"), theme.Active().Name)
	kv(p, i18n.T("cfg.kv.color_profile"), theme.ActiveProfile().String())
	kv(p, i18n.T("cfg.kv.themes_available"), strings.Join(theme.Names(), ", "))

	fmt.Println(p)
	// Timeline density (full/compact/minimal) is a UI concern too, but it
	// lives under /config agent (the CHATCLI_CODER_UI knob shared with
	// /coder). Point there instead of duplicating the value.
	fmt.Println(colorize("  "+i18n.T("cfg.ui.timeline_pointer"), ColorGray))
	fmt.Println(colorize("  "+i18n.T("cfg.ui.theme_status_change_hint"), ColorGray))

	sectionEnd(ColorPurple)
}
