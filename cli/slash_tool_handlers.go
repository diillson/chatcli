/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/version"
)

// helpText produces a compact, LLM-friendly summary of the available
// slash commands and context modifiers. We deliberately do not return
// the full colored / boxed help that /help prints to the user — the
// model gets a plain-text catalog it can paraphrase or reference.
//
// Keeping this distinct from showHelp() lets the human-facing UX evolve
// (colors, sectioning, ASCII art) without polluting the model's prompt
// surface with terminal escape codes.
func (cli *ChatCLI) helpText() string {
	var b strings.Builder
	fmt.Fprintln(&b, i18n.T("help.header.title"))
	fmt.Fprintln(&b, i18n.T("help.header.subtitle1"))
	fmt.Fprintln(&b)

	emit := func(section string, entries [][2]string) {
		fmt.Fprintf(&b, "## %s\n", section)
		for _, e := range entries {
			fmt.Fprintf(&b, "  %-32s  %s\n", e[0], e[1])
		}
		fmt.Fprintln(&b)
	}

	emit(i18n.T("help.section.general"), [][2]string{
		{"/help", i18n.T("help.command.help")},
		{"/exit | /quit", i18n.T("help.command.exit")},
		{"/newsession", i18n.T("help.command.newsession")},
		{"/version | /v", i18n.T("help.command.version")},
		{"/compact [instruction]", i18n.T("help.command.compact")},
		{"/memory [subcommand]", i18n.T("help.command.memory")},
	})
	emit(i18n.T("help.section.config"), [][2]string{
		{"/switch", i18n.T("help.command.switch")},
		{"/config | /status", i18n.T("help.command.config")},
		{"/reload", i18n.T("help.command.reload")},
	})
	emit(i18n.T("help.section.context"), [][2]string{
		{"@file <path>", i18n.T("help.command.file")},
		{"@git", i18n.T("help.command.git")},
		{"@history", i18n.T("help.command.history")},
		{"@env", i18n.T("help.command.env")},
	})
	return b.String()
}

// versionText returns a one-shot string describing the running build
// and whether an update is available. The caller's ctx caps the update
// probe so an LLM-triggered /version doesn't stall the turn on a slow
// network; we layer an extra 2s timeout on top in case the caller's ctx
// is the long-lived agent loop ctx.
func (cli *ChatCLI) versionText(parent context.Context) string {
	versionInfo := version.GetCurrentVersion()
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	latest, hasUpdate, err := version.CheckLatestVersionWithContext(ctx)
	return version.FormatVersionInfo(versionInfo, latest, hasUpdate, err)
}
