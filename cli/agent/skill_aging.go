/*
 * ChatCLI - Skill-block aging for agent/coder mode
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Mid-loop skill injections (see cli/skill_rescan.go) are append-only
 * user-role messages, so without a counterweight every activated skill body
 * rides in the window for the REST of the run — ten activated skills means
 * ten full markdown bodies on every subsequent request. This file is the
 * counterweight: once a skill block is old enough that the model has already
 * absorbed and applied its guidance, the block collapses to a one-line stub.
 *
 * The collapse mirrors ApplyMicrocompact's contract for tool results:
 *   - in-place mutation at the same turn-boundary call site (at most one
 *     provider prefix-cache invalidation event per turn, shared with
 *     microcompact);
 *   - every dropped byte is archived through CCR first, so the model can
 *     expand the stub with @recall at any time;
 *   - age is measured in assistant messages, the loop's turn currency.
 *
 * Collapsed blocks are NOT protected from the generic compaction pipeline
 * (no PreserveVerbatim): a stub that later gets folded into an L2 summary is
 * acceptable, because the guidance is recoverable via @recall AND the caller
 * releases collapsed skill names from the per-Run dedup set — a skill whose
 * trigger fires again after the cooldown re-injects fresh.
 */
package agent

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// skillAgeTurnsEnvVar overrides how many turns a mid-loop skill block stays
// fully inlined before collapsing to a stub. The same value doubles as the
// re-injection cooldown, so one knob governs the whole lifecycle.
const skillAgeTurnsEnvVar = "CHATCLI_SKILL_AGE_TURNS"

// defaultSkillAgeTurns is deliberately gentler than the tool-result
// microcompact thresholds (2/4): skill guidance shapes behavior across
// several actions, while a tool result is usually consumed by the very next
// one. Six turns is enough for the model to internalize the guidance.
const defaultSkillAgeTurns = 6

// SkillAgingConfig controls when mid-loop skill blocks collapse.
type SkillAgingConfig struct {
	// TurnsBeforeCollapse is the age (in assistant turns) after which a
	// skill block is reduced to a stub. Values < 1 fall back to the default.
	TurnsBeforeCollapse int

	// CCR, when set, archives the full block before the collapse and embeds
	// a <<ccr:KEY>> marker in the stub (same contract as MicrocompactConfig).
	CCR *compress.Layer
}

// DefaultSkillAgingConfig resolves the aging config from the environment,
// read live so /config flips apply on the very next turn.
func DefaultSkillAgingConfig() SkillAgingConfig {
	cfg := SkillAgingConfig{TurnsBeforeCollapse: defaultSkillAgeTurns}
	if v := os.Getenv(skillAgeTurnsEnvVar); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.TurnsBeforeCollapse = n
		}
	}
	return cfg
}

// SkillAgingReport describes what a pass over the history collapsed.
type SkillAgingReport struct {
	Collapsed       int
	CharsSaved      int64
	CollapsedSkills []string
}

// ApplySkillAging collapses mid-loop skill blocks older than the configured
// age into one-line stubs, mutating history in place (same convention as
// ApplyMicrocompact — the caller passes the persistent slice). Blocks are
// identified structurally via Meta.SkillNames, never by content sniffing:
// compaction may have already rewritten the text, but Meta survives every
// rewrite, park snapshot and session save.
//
// Returns the (same) history slice and a report; report.CollapsedSkills feeds
// the caller's dedup-set release so collapsed skills can re-trigger later.
func ApplySkillAging(history []models.Message, cfg SkillAgingConfig, logger *zap.Logger) ([]models.Message, *SkillAgingReport) {
	report := &SkillAgingReport{}
	if len(history) == 0 {
		return history, report
	}
	if cfg.TurnsBeforeCollapse < 1 {
		cfg.TurnsBeforeCollapse = defaultSkillAgeTurns
	}

	// Same turn currency as ApplyMicrocompact: assistant messages mark turns.
	turnNumber := 0
	turnMap := make(map[int]int)
	for i, msg := range history {
		if msg.Role == "assistant" {
			turnNumber++
		}
		turnMap[i] = turnNumber
	}
	if turnNumber < cfg.TurnsBeforeCollapse {
		return history, report
	}

	for i := range history {
		msg := &history[i]
		if msg.Role != "user" || msg.Meta == nil ||
			msg.Meta.SkillNames == "" || msg.Meta.SkillCollapsed {
			continue
		}
		if turnNumber-turnMap[i] < cfg.TurnsBeforeCollapse {
			continue
		}

		names := msg.Meta.SkillNameList()
		original := len(msg.Content)
		stub := buildSkillStub(names, cfg.TurnsBeforeCollapse)
		msg.Content = stub + recallSuffix(cfg.CCR, msg.Content, stub)
		msg.Meta.SkillCollapsed = true
		report.Collapsed++
		report.CharsSaved += int64(original - len(msg.Content))
		report.CollapsedSkills = append(report.CollapsedSkills, names...)
	}

	if logger != nil && report.Collapsed > 0 {
		logger.Debug("Skill aging applied",
			zap.Int("collapsed", report.Collapsed),
			zap.Int64("chars_saved", report.CharsSaved),
			zap.Strings("skills", report.CollapsedSkills))
	}
	return history, report
}

// buildSkillStub is the one-line replacement for an aged-out skill block.
// English on purpose — model-facing prompt text, like the microcompact stubs.
func buildSkillStub(names []string, ageTurns int) string {
	return fmt.Sprintf(
		"[SKILL GUIDANCE ARCHIVED — aged out after %d turns]\nSkills: %s. "+
			"If you still need this guidance, recover it before relying on it.",
		ageTurns, strings.Join(names, ", "))
}
