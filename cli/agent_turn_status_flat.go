/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/metrics"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/kit"
	"github.com/diillson/chatcli/ui/theme"
)

// flatTurnStatus is the live status line of a turn for themes that lay
// panels out flat. The line is repainted several times a second while the
// model works, so it is the one place where the numbers a user watches can
// move: the clock, and the session's tokens and cost, which step forward
// each time a model call returns. The framed line shows only the model and
// the clock, in brackets; here the parts are separated by a quiet dot and
// only the model name is in the strong tone.
func (a *AgentMode) flatTurnStatus(d time.Duration, modelName string, queued int) string {
	sep := kit.Colorize(" · ", theme.RoleBorder)
	parts := []string{
		theme.ANSI(theme.RoleTextStrong) + modelName + theme.Reset(),
		kit.Colorize(metrics.FormatDurationShort(d), theme.RoleMuted),
	}
	// While a reply streams, the tokens it has produced so far are added
	// as an estimate and the figure says so with a leading "~"; the cost
	// never moves on an estimate, only when the call's real usage lands.
	tokens, cost := a.sessionUsage()
	streaming := a.liveOutputTokens()
	if tokens+streaming > 0 {
		count := formatTokenCount64(tokens + streaming)
		if streaming > 0 {
			count = "~" + count
		}
		parts = append(parts, kit.Colorize(i18n.T("agent.turn.flat.tokens", count), theme.RoleMuted))
		if cost > 0 {
			parts = append(parts, kit.Colorize(formatTurnCost(cost), theme.RoleMuted))
		}
	}
	if queued > 0 {
		parts = append(parts, kit.Colorize(i18n.T("agent.turn.flat.queued", queued), theme.RoleWarning))
	}
	return "\r  " + kit.Colorize(metrics.GetSpinnerFrame(), theme.RoleTextStrong) + " " +
		kit.Colorize(i18n.T("agent.turn.flat.working"), theme.RoleText) + "  " + strings.Join(parts, sep)
}

// sessionUsage returns the session's tokens and cost so far, zero when no
// tracker is wired (tests, surfaces that do not bill).
func (a *AgentMode) sessionUsage() (tokens int64, cost float64) {
	if a.cli == nil || a.cli.costTracker == nil {
		return 0, 0
	}
	return a.cli.costTracker.TotalTokens(), a.cli.costTracker.TotalCost()
}
