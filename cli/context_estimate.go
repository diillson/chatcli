/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * One context estimate.
 *
 * The footer's ctx%, the compactor's budget and /context status used to
 * compute the window occupancy with three different formulas, none of
 * which counted the tool definitions or the output reserve. This is the
 * single source: {prefix, history, tool definitions, reserve}, the same
 * categories Claude Code's /context shows.
 */
package cli

import (
	"strings"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
)

// contextEstimate is a snapshot of what the next request would carry.
type contextEstimate struct {
	Provider, Model string
	Window          int // model context window (tokens)
	PrefixChars     int // system prompt (breakdown, or the system message when it lives in history)
	HistoryChars    int // conversation without the system role
	ToolDefChars    int // serialized native tool definitions (agent/coder)
	ReserveTokens   int // max_tokens reserved for the answer
	prefixInHistory bool
	cli             *ChatCLI
}

// contextEstimate builds the estimate from live state.
func (cli *ChatCLI) contextEstimate() contextEstimate {
	e := contextEstimate{cli: cli}
	if cli == nil {
		return e
	}
	e.Provider, e.Model = cli.Provider, cli.Model
	e.Window = catalog.GetContextWindow(cli.Provider, cli.Model)
	var system, other []models.Message
	for _, m := range cli.history {
		if strings.EqualFold(m.Role, "system") {
			system = append(system, m)
		} else {
			other = append(other, m)
		}
	}
	e.HistoryChars = promptCharsOf(other)
	if len(system) > 0 {
		e.PrefixChars = promptCharsOf(system)
		e.prefixInHistory = true
	} else {
		e.PrefixChars = cli.promptBreakdowns.latestNamed("chat").TotalChars()
	}
	e.ToolDefChars = cli.toolDefsChars
	e.ReserveTokens = answerReserveTokens(cli.getMaxTokensForCurrentLLM(), e.Window)
	return e
}

// reserveMaxShare caps the answer reserve counted against the window: a
// model whose max output equals a third of its window would otherwise show
// an empty conversation as a third full. The compactor's BudgetRatio
// headroom is the same reserve, so the compactor does not add it again.
const reserveMaxShare = 0.25

// answerReserveTokens is max_tokens as sent, capped at the window share.
func answerReserveTokens(maxTokens, window int) int {
	if maxTokens <= 0 {
		return 0
	}
	if window > 0 {
		if capTokens := int(float64(window) * reserveMaxShare); maxTokens > capTokens {
			return capTokens
		}
	}
	return maxTokens
}

func (e contextEstimate) tokens(chars int) int {
	if chars <= 0 || e.cli == nil {
		return 0
	}
	return e.cli.calibrator().EstimateTokens(e.Provider, e.Model, chars)
}

// PrefixTokens, HistoryTokens and ToolDefTokens are the calibrated sizes.
func (e contextEstimate) PrefixTokens() int  { return e.tokens(e.PrefixChars) }
func (e contextEstimate) HistoryTokens() int { return e.tokens(e.HistoryChars) }
func (e contextEstimate) ToolDefTokens() int { return e.tokens(e.ToolDefChars) }

// TotalTokens is what the next request occupies: prefix, history, tool
// definitions and the answer reserve.
func (e contextEstimate) TotalTokens() int {
	return e.PrefixTokens() + e.HistoryTokens() + e.ToolDefTokens() + e.ReserveTokens
}

// Pct is the window occupancy; ok is false without a window or content.
func (e contextEstimate) Pct() (float64, bool) {
	if e.Window <= 0 {
		return 0, false
	}
	total := e.TotalTokens()
	if total <= 0 || e.PrefixChars+e.HistoryChars+e.ToolDefChars <= 0 {
		return 0, false
	}
	return float64(total) / float64(e.Window) * 100, true
}

// ReservedChars is what the compactor must leave free next to the
// history it measures: the prefix when it does not live in the history
// slice and the tool definitions. The answer reserve is the compactor's
// own BudgetRatio headroom, so it is not added twice.
func (e contextEstimate) ReservedChars() int {
	n := e.ToolDefChars
	if !e.prefixInHistory {
		n += e.PrefixChars
	}
	return n
}

// retrievedShare is the window share retrieved passages may take per turn.
const retrievedShare = 0.15

// retrievedFloorChars keeps retrieval useful on small windows.
const retrievedFloorChars = 4_000

// retrievedBudgetFor scales the per-turn retrieved-passages budget with the
// window: min(default, window × cpt × share), never under the floor.
func retrievedBudgetFor(window int, cpt float64) int {
	if window <= 0 {
		return 0
	}
	if cpt <= 0 {
		cpt = defaultCharsPerToken
	}
	n := int(float64(window) * cpt * retrievedShare)
	if n < retrievedFloorChars {
		n = retrievedFloorChars
	}
	if n > ctxmgr.DefaultRetrievedBudgetChars {
		n = ctxmgr.DefaultRetrievedBudgetChars
	}
	return n
}

// applyRetrievedBudget points the context manager at the window-scaled
// retrieval budget for this session (a no-op without a context handler).
func (cli *ChatCLI) applyRetrievedBudget(b *prefixBudget) {
	if cli == nil || cli.contextHandler == nil || b == nil {
		return
	}
	mgr := cli.contextHandler.GetManager()
	if mgr == nil {
		return
	}
	mgr.SetRetrievedBudget(retrievedBudgetFor(b.Window, cli.frozenPrefixRatio(cli.Provider, cli.Model)))
}
