/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The turn-boundary history rewrites of the agent/coder loop: microcompact
 * of old tool results, dedup of repeated reads and skill aging. All three
 * edit messages that sit ahead of the provider's rolling cache breakpoint,
 * so they run under the rewrite-pressure gate (rewrite_pressure.go): a
 * warm prefix is kept until the window is actually filling up.
 */
package cli

import (
	"fmt"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// applyTurnBoundaryRewrites runs the in-place history rewrites for one
// loop turn when the rewrite-pressure gate allows them. Each rewrite that
// changes the history declares the coming cache write as an expected
// rebuild, so the telemetry does not read it as an unstable prefix.
func (a *AgentMode) applyTurnBoundaryRewrites(turn int, cfg CompactConfig, renderer *agent.UIRenderer) {
	if a == nil || a.cli == nil {
		return
	}
	if renderer == nil {
		renderer = agent.NewUIRenderer(a.logger)
	}
	if !a.cli.historyRewriteAllowed(a.cli.history, cfg) {
		a.logger.Debug("history rewrites deferred: prefix cache is warm and the window is not under pressure",
			zap.Int("turn", turn+1),
			zap.Int("history_chars", totalChars(a.cli.history)))
		return
	}
	mcCfg := agent.DefaultMicrocompactConfig()
	// Route dropped bytes through CCR so every microcompacted tool
	// result stays recoverable via @recall instead of being lost.
	mcCfg.CCR = a.cli.compressionLayer
	if h, report := agent.ApplyMicrocompact(a.cli.history, turn, mcCfg, a.logger); report != nil && (report.Truncated > 0 || report.Summarized > 0) {
		a.cli.history = h
		a.cli.costTracker.NoteExpectedCacheRebuild()
		fmt.Printf("\r\033[K  %s %s\n",
			renderer.Colorize("🗜", agent.ColorGray),
			renderer.Colorize(
				i18n.T("agent.microcompact.applied",
					report.Truncated, report.Summarized, FormatPayloadSize(int(report.CharsSaved))),
				agent.ColorGray))
	}

	// Skill aging (same turn boundary as microcompact, so both passes
	// share a single prefix-cache invalidation event): mid-loop skill
	// blocks the model has already absorbed collapse to CCR-recoverable
	// stubs, and the collapsed skills leave the dedup set so they can
	// re-trigger after the cooldown.
	saCfg := agent.DefaultSkillAgingConfig()
	saCfg.CCR = a.cli.compressionLayer
	// Repeated reads of the same file: keep the newest, stub the rest
	// (recoverable via @recall). Same turn boundary as microcompact so
	// the two rewrites share one prefix-cache invalidation.
	if h, report := agent.DedupRepeatedReads(a.cli.history, mcCfg.CCR, a.logger); report != nil && report.Superseded > 0 {
		a.cli.history = h
		a.cli.costTracker.NoteExpectedCacheRebuild()
		fmt.Printf("\r\033[K  %s %s\n",
			renderer.Colorize("│", agent.ColorGray),
			renderer.Colorize(i18n.T("agent.dedup_reads.applied", report.Superseded, FormatPayloadSize(int(report.CharsSaved))), agent.ColorGray))
	}
	if h, report := agent.ApplySkillAging(a.cli.history, saCfg, a.logger); report != nil && report.Collapsed > 0 {
		a.cli.history = h
		a.cli.costTracker.NoteExpectedCacheRebuild()
		a.releaseCollapsedSkills(report.CollapsedSkills, turn)
		fmt.Printf("\r\033[K  %s %s\n",
			renderer.Colorize("🗜", agent.ColorGray),
			renderer.Colorize(
				i18n.T("agent.skills.aged",
					report.Collapsed, FormatPayloadSize(int(report.CharsSaved))),
				agent.ColorGray))
	}
}
