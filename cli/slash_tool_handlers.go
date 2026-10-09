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

// helpText is the /help catalog for the model: the same generated listing
// the user sees (every registered command, alias, context modifier, flag and
// subcommand), rendered without terminal colors so it can be paraphrased or
// referenced. One renderer, so the model's catalog is never out of date.
func (cli *ChatCLI) helpText() string {
	var b strings.Builder
	cli.renderHelp(&b, helpStyle{})
	return b.String()
}

// versionText returns a one-shot string describing the running build
// and whether an update is available. The caller's ctx caps the update
// probe so an LLM-triggered /version doesn't stall the turn on a slow
// network; we layer an extra 2s timeout on top in case the caller's ctx
// is the long-lived agent loop ctx.
func (cli *ChatCLI) versionText(parent context.Context) string {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	return FormatVersionReport(version.GetReport(ctx))
}

// sessionListText returns the local saved-session catalog (name — title) as
// plain text, one entry per line. It is the extracted, string-returning core
// behind both the @session tool's list subcommand and the /session-list
// slash tool. Remote sessions are deliberately out of scope: they need a
// live remote client plus interactive disambiguation, which doesn't fit the
// request-response shape the model expects (handleListSessions keeps owning
// that interactive path).
func (cli *ChatCLI) sessionListText() (string, error) {
	if cli.sessionManager == nil {
		return "", fmt.Errorf("%s", i18n.T("session.tool.unavailable"))
	}
	names, err := cli.sessionManager.ListSessions()
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return i18n.T("session.tool.list.empty"), nil
	}
	titles := cli.sessionManager.SessionTitles()
	var b strings.Builder
	b.WriteString(i18n.T("session.tool.list.header"))
	b.WriteByte('\n')
	for _, n := range names {
		line := "  • " + n
		if t := titles[n]; t != "" {
			line += " — " + t
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
