/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package coder

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/textfold"
	"github.com/diillson/chatcli/ui/kit"
	"github.com/diillson/chatcli/ui/theme"
)

// printFlatSecurityPrompt draws the permission prompt for themes that lay
// panels out flat. The framed card spends ten lines and a border on what is
// one question, and puts the command — the only thing the user has to read
// before answering — in the middle of it. Here the command comes first
// under the title, the labels step back into the muted tone, and the
// choices sit on one line, so the whole prompt fits in six.
func printFlatSecurityPrompt(secCtx *SecurityContext, actionLabel string, details []string, pattern string, isExecCmd bool) {
	strong := theme.ANSI(theme.RoleTextStrong) + "\033[1m"
	reset := theme.Reset()
	label := func(key string) string {
		return kit.Colorize(fmt.Sprintf("  %-8s", strings.ToLower(kit.Plain(key))), theme.RoleMuted)
	}

	fmt.Println()
	fmt.Println(kit.SectionHeader(i18n.T("coder.security.header"), detectTerminalWidth()))

	if secCtx != nil && secCtx.AgentName != "" {
		fmt.Println(label("agent") + strong + secCtx.AgentName + reset)
		if secCtx.TaskDesc != "" {
			task := secCtx.TaskDesc
			if len(task) > 120 {
				task = textfold.Cut(task, 120) + "…"
			}
			fmt.Println(label(i18n.T("coder.security.task")) + kit.Colorize(task, theme.RoleText))
		}
	}
	fmt.Println(label(i18n.T("coder.security.action")) + kit.Colorize(actionLabel, theme.RoleText))
	for _, d := range details {
		fmt.Println(label("") + strong + d + reset)
	}
	rule := i18n.T("coder.security.exec_requires_approval")
	if !isExecCmd {
		rule = i18n.T("coder.security.no_rule_for", pattern)
	}
	fmt.Println(label(i18n.T("coder.security.rule")) + kit.Colorize(rule, theme.RoleMuted))
	fmt.Println()

	key := func(k, text string, r theme.Role) string {
		return kit.Colorize(k, r) + " " + kit.Colorize(text, theme.RoleText)
	}
	choices := []string{key("y", i18n.T("coder.security.yes_once"), theme.RoleToolSuccess)}
	if !isExecCmd {
		choices = append(choices, key("a", i18n.T("coder.security.allow_always", pattern), theme.RoleToolSuccess))
	}
	choices = append(choices, key("n", i18n.T("coder.security.no_skip"), theme.RoleToolError))
	if !isExecCmd {
		choices = append(choices, key("d", i18n.T("coder.security.deny_always", pattern), theme.RoleToolError))
	}
	// One line when it fits; one choice per line when the pattern is long.
	line := "  " + strings.Join(choices, kit.Colorize("   ·   ", theme.RoleBorder))
	if kit.VisibleLen(line) > detectTerminalWidth()-2 {
		line = "  " + strings.Join(choices, "\n  ")
	}
	fmt.Println(line)
	fmt.Print(strong + "  " + kit.GlyphArrow.String() + " " + reset)
}
