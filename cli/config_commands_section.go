/*
 * ChatCLI - /config commands section
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Diagnostics for the slash-command catalog, organized for scanning: a
 * one-line summary, commands grouped by source (native first, then each
 * interop family) with aligned columns, then the two failure ledgers
 * (refused shadows, parse-skipped files) and the scanned directories with
 * existence markers. The only mutation is "reload" (re-fingerprint).
 */
package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli/commands"
	"github.com/diillson/chatcli/i18n"
)

// commandSourceLabel renders a source family for display. Brand names stay
// literal (proper nouns); the native scopes localize.
func commandSourceLabel(s commands.Source) string {
	switch s {
	case commands.SourceProject:
		return i18n.T("cfg.commands.source.project")
	case commands.SourceGlobal:
		return i18n.T("cfg.commands.source.global")
	case commands.SourceClaude:
		return "Claude Code"
	case commands.SourceDevin:
		return "Devin"
	case commands.SourceWindsurf:
		return "Windsurf"
	case commands.SourceCursor:
		return "Cursor"
	case commands.SourceOpencode:
		return "opencode"
	case commands.SourceCodex:
		return "Codex"
	case commands.SourceGemini:
		return "Gemini CLI"
	case commands.SourceQwen:
		return "Qwen Code"
	case commands.SourceCopilot:
		return "GitHub Copilot"
	default:
		return string(s)
	}
}

// commandSourceOrder fixes the display order: native scopes first, interop
// families after, matching the catalog's precedence.
var commandSourceOrder = []commands.Source{
	commands.SourceProject, commands.SourceGlobal,
	commands.SourceClaude, commands.SourceDevin, commands.SourceWindsurf,
	commands.SourceCursor, commands.SourceOpencode, commands.SourceCodex,
	commands.SourceGemini, commands.SourceQwen, commands.SourceCopilot,
}

// showConfigCommands renders the catalog panorama.
func (cli *ChatCLI) showConfigCommands() {
	sectionHeader("⌘", "cfg.section.commands.title", ColorCyan)
	kv("  ", commandsEnabledEnv, envBool(commandsEnabledEnv))
	kv("  ", commandsAutorouteEnv, envBool(commandsAutorouteEnv))

	cat := cli.slashCommandCatalog()
	if cat == nil {
		fmt.Printf("  %s\n", colorize(i18n.T("cfg.commands.disabled"), ColorYellow))
		sectionEnd(ColorCyan)
		return
	}

	list := cat.List()
	bySource := make(map[commands.Source][]*commands.Command)
	nameWidth := 0
	for _, cmd := range list {
		bySource[cmd.Source] = append(bySource[cmd.Source], cmd)
		if w := len(cmd.InvocationName()) + 1; w > nameWidth {
			nameWidth = w
		}
	}

	fmt.Printf("  %s\n", colorize(i18n.T("cfg.commands.summary", len(list), len(bySource)), ColorGray))
	if len(list) == 0 {
		fmt.Printf("\n  %s\n", colorize(i18n.T("cfg.commands.none"), ColorGray))
	}

	for _, source := range commandSourceOrder {
		group := bySource[source]
		if len(group) == 0 {
			continue
		}
		fmt.Printf("\n  %s %s\n",
			colorize("▸ "+commandSourceLabel(source), ColorCyan+ColorBold),
			colorize(fmt.Sprintf("(%d)", len(group)), ColorGray))
		for _, cmd := range group {
			line := fmt.Sprintf("    %-*s", nameWidth+4, "/"+cmd.InvocationName())
			desc := cmd.Description
			if desc == "" {
				desc = i18n.T("complete.command.template_fallback")
			}
			fmt.Printf("%s %s", colorize(line, ColorGreen), desc)
			if cmd.ArgumentHint != "" {
				fmt.Printf("  %s", colorize(cmd.ArgumentHint, ColorCyan))
			}
			if cmd.ResolvedMode() == commands.ExecModeCoder {
				fmt.Printf("  %s", colorize(i18n.T("complete.command.coder_marker"), ColorYellow))
			}
			fmt.Println()
		}
	}

	if refused := cat.Refused(); len(refused) > 0 {
		fmt.Printf("\n  %s\n", colorize("⚠ "+i18n.T("cfg.commands.refused"), ColorYellow))
		for _, name := range sortedCommandKeys(refused) {
			fmt.Printf("    /%s\n      %s\n", name, colorize(refused[name], ColorGray))
		}
	}
	if skipped := cat.Skipped(); len(skipped) > 0 {
		fmt.Printf("\n  %s\n", colorize("⚠ "+i18n.T("cfg.commands.skipped"), ColorYellow))
		for _, path := range sortedCommandKeys(skipped) {
			fmt.Printf("    %s\n      %s\n", path, colorize(skipped[path], ColorGray))
		}
	}

	fmt.Printf("\n  %s\n", colorize(i18n.T("cfg.commands.dirs"), ColorGray))
	for _, d := range cat.Dirs() {
		marker, color := "–", ColorGray
		if _, err := os.Stat(d); err == nil {
			marker, color = "✓", ColorGreen
		}
		fmt.Printf("    %s %s\n", colorize(marker, color), colorize(d, ColorGray))
	}
	sectionEnd(ColorCyan)
}

// sortedCommandKeys returns a map's keys sorted, for deterministic rendering.
func sortedCommandKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// routeConfigCommands handles "/config commands <arg>".
func (cli *ChatCLI) routeConfigCommands(rest []string) {
	if len(rest) == 1 && strings.EqualFold(rest[0], "reload") {
		if cat := cli.slashCommandCatalog(); cat != nil {
			cat.Invalidate()
		}
		fmt.Println(colorize(i18n.T("cfg.commands.reloaded"), ColorGreen))
		cli.showConfigCommands()
		return
	}
	fmt.Println(colorize(i18n.T("cfg.commands.usage"), ColorYellow))
}
