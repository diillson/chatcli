/*
 * ChatCLI - /config commands section
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Read-mostly diagnostics for the slash-command catalog: what loaded, from
 * which source, what was refused for shadowing a built-in, and which
 * directories are scanned. The only mutation is "reload" (re-fingerprint).
 */
package cli

import (
	"fmt"

	"github.com/diillson/chatcli/i18n"
)

// showConfigCommands renders the catalog panorama.
func (cli *ChatCLI) showConfigCommands() {
	sectionHeader("⌘", "cfg.section.commands.title", ColorCyan)

	kv("  ", commandsEnabledEnv, envBool(commandsEnabledEnv))

	cat := cli.slashCommandCatalog()
	if cat == nil {
		fmt.Printf("  %s\n", colorize(i18n.T("cfg.commands.disabled"), ColorYellow))
		sectionEnd(ColorCyan)
		return
	}

	list := cat.List()
	if len(list) == 0 {
		fmt.Printf("  %s\n", colorize(i18n.T("cfg.commands.none"), ColorGray))
	}
	for _, cmd := range list {
		line := fmt.Sprintf("  /%s", cmd.InvocationName())
		if cmd.Description != "" {
			line += "  — " + cmd.Description
		}
		fmt.Println(colorize(line, ColorGreen))
		fmt.Printf("      %s\n", colorize(fmt.Sprintf("[%s] %s", cmd.Source, cmd.Path), ColorGray))
	}

	if refused := cat.Refused(); len(refused) > 0 {
		fmt.Printf("\n  %s\n", colorize(i18n.T("cfg.commands.refused"), ColorYellow))
		for name, path := range refused {
			fmt.Printf("      /%s  (%s)\n", name, path)
		}
	}

	fmt.Printf("\n  %s\n", colorize(i18n.T("cfg.commands.dirs"), ColorGray))
	for _, d := range cat.Dirs() {
		fmt.Printf("      %s\n", d)
	}
	sectionEnd(ColorCyan)
}

// routeConfigCommands handles "/config commands <arg>".
func (cli *ChatCLI) routeConfigCommands(rest []string) {
	if len(rest) == 1 && rest[0] == "reload" {
		if cat := cli.slashCommandCatalog(); cat != nil {
			cat.Invalidate()
		}
		fmt.Println(colorize(i18n.T("cfg.commands.reloaded"), ColorGreen))
		cli.showConfigCommands()
		return
	}
	fmt.Println(colorize(i18n.T("cfg.commands.usage"), ColorYellow))
}
