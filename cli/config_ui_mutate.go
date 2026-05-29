/*
 * ChatCLI - /config ui mutator.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Runtime control of the color theme that skins every surface — chat,
 * coder and agent cards, borders, markdown and spinners:
 *
 *   /config ui                     # show active theme + color profile
 *   /config ui theme               # same as above (explicit)
 *   /config ui theme dark          # switch to the dark theme
 *   /config ui theme light         # switch to the light theme
 *
 * Unlike the timeline UI style (which the renderer re-reads from the env on
 * each NewUIRenderer), the theme is process-global state held by the theme
 * package, so a switch applies on the very next render with no restart.
 *
 * Persistence mirrors /config agent ui: we set the process env and print a
 * hint to add CHATCLI_THEME=<name> to the .env for a permanent default. We
 * never rewrite the user's .env ourselves.
 */
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/theme"
)

// routeConfigUI dispatches `/config ui <sub> [args...]`. Args arrives with the
// "ui" token already stripped. The empty-args case shows the status panorama.
func (cli *ChatCLI) routeConfigUI(args []string) {
	if len(args) == 0 {
		cli.printConfigUIStatus()
		return
	}
	sub := strings.ToLower(args[0])
	rest := args[1:]

	switch sub {
	case "help", "-h", "--help":
		cli.printConfigUIUsage()
	case "theme":
		cli.configUITheme(rest)
	default:
		fmt.Println(colorize("  "+i18n.T("cfg.ui.unknown_sub", sub), ColorYellow))
		cli.printConfigUIUsage()
	}
}

// printConfigUIUsage shows the subcommand cheat sheet for /config ui.
func (cli *ChatCLI) printConfigUIUsage() {
	fmt.Println(colorize(i18n.T("cfg.ui.usage_header"), ColorCyan+ColorBold))
	fmt.Println("  /config ui")
	fmt.Println("  /config ui theme                       # " + i18n.T("cfg.ui.usage_theme_show"))
	for _, name := range theme.Names() {
		fmt.Printf("  /config ui theme %-21s # %s\n", name, i18n.T("cfg.ui.usage_theme_set", name))
	}
	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("cfg.ui.usage_note_scope"), ColorGray))
	fmt.Println(colorize("  "+i18n.T("cfg.ui.usage_note_persist"), ColorGray))
}

// configUITheme handles `/config ui theme [value]`. No arg shows the status;
// with arg it switches the active theme at runtime (and sets the env so the
// choice survives a config reload within the session).
func (cli *ChatCLI) configUITheme(args []string) {
	if len(args) == 0 {
		cli.printConfigUIStatus()
		return
	}
	target := strings.ToLower(strings.TrimSpace(args[0]))

	previous := theme.Active().Name
	if err := theme.SetActive(target); err != nil {
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.ui.theme_invalid_value", target), ColorRed))
		fmt.Println(colorize("  "+i18n.T("cfg.ui.theme_valid_values", strings.Join(theme.Names(), ", ")), ColorGray))
		return
	}
	if err := os.Setenv(config.ThemeEnv, target); err != nil {
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.ui.theme_set_failed", err.Error()), ColorRed))
		return
	}

	if previous == target {
		fmt.Println(colorize("  ✔ "+i18n.T("cfg.ui.theme_set_noop", target), ColorGray))
	} else {
		fmt.Println(colorize("  ✔ "+i18n.T("cfg.ui.theme_set_ok", previous, target), ColorGreen))
	}
	fmt.Println(colorize("    "+i18n.T("cfg.ui.theme_persist_hint", target), ColorGray))
}

// printConfigUIStatus shows the active theme, the detected color profile and
// the available themes, marking the active one.
func (cli *ChatCLI) printConfigUIStatus() {
	current := theme.Active().Name
	envRaw := os.Getenv(config.ThemeEnv)
	source := i18n.T("cfg.ui.theme_source_env")
	if strings.TrimSpace(envRaw) == "" {
		source = i18n.T("cfg.ui.theme_source_default")
	}

	fmt.Println(colorize(i18n.T("cfg.ui.theme_status_header"), ColorCyan+ColorBold))
	fmt.Printf("  %s %s  (%s)\n",
		colorize(i18n.T("cfg.ui.theme_status_current"), ColorGray),
		colorize(current, ColorLime+ColorBold),
		source,
	)
	fmt.Printf("  %s %s\n",
		colorize(i18n.T("cfg.ui.theme_status_profile"), ColorGray),
		colorize(theme.ActiveProfile().String(), ColorYellow),
	)
	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("cfg.ui.theme_status_options"), ColorGray))
	for _, name := range theme.Names() {
		marker := "  "
		if name == current {
			marker = colorize("→ ", ColorLime+ColorBold)
		}
		fmt.Printf("  %s%s%s  %s\n",
			marker,
			colorize(fmt.Sprintf("%-8s", name), ColorYellow),
			colorize(" ·", ColorGray),
			i18n.T("cfg.ui.theme_desc_"+name),
		)
	}
	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("cfg.ui.theme_status_change_hint"), ColorGray))
}
