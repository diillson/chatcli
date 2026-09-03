/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /context status — "what is in my context right now, and what does each
 * part cost". Composes the pieces that already existed in isolation (last
 * assembled prompt breakdown, live history, learned token ratio, cache
 * telemetry, compaction budget) into one view with token estimates, the
 * projected share of the model window, and where auto-compact will fire.
 */
package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
)

// historyRoleStat aggregates the live history by role.
type historyRoleStat struct {
	Role     string
	Messages int
	Chars    int
}

// historyStats summarizes cli.history for the inspector.
type historyStats struct {
	Roles      []historyRoleStat
	Messages   int
	Chars      int
	Summaries  int // compacted summary messages present
	CCRMarkers int // <<ccr:KEY>> markers the model can expand with @recall
}

// contextStatusReport is the assembled view; kept as data so it can be
// tested without capturing stdout.
type contextStatusReport struct {
	Provider, Model     string
	Window              int
	CharsPerToken       float64
	CalibrationSamples  int
	Prompt              *promptBreakdown
	History             historyStats
	PromptTokens        int
	HistoryTokens       int
	TotalTokens         int
	ProjectedPct        float64
	CompactBudgetTokens int
	CompactAtPct        float64
	Cache               CacheStats
}

// summarizeHistory computes the role breakdown of the live history. Weight
// follows the compactor's messageWeight so figures match its budget.
func summarizeHistory(history []models.Message) historyStats {
	var st historyStats
	byRole := map[string]*historyRoleStat{}
	for _, m := range history {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "" {
			role = "user"
		}
		rs, ok := byRole[role]
		if !ok {
			rs = &historyRoleStat{Role: role}
			byRole[role] = rs
		}
		w := messageWeight(m)
		rs.Messages++
		rs.Chars += w
		st.Messages++
		st.Chars += w
		if m.Meta != nil && m.Meta.IsSummary {
			st.Summaries++
		}
		st.CCRMarkers += len(compress.ExtractKeys(m.Content))
	}
	for _, rs := range byRole {
		st.Roles = append(st.Roles, *rs)
	}
	sort.Slice(st.Roles, func(i, j int) bool { return st.Roles[i].Chars > st.Roles[j].Chars })
	return st
}

// buildContextStatusReport assembles the report from live state.
func (cli *ChatCLI) buildContextStatusReport() contextStatusReport {
	r := contextStatusReport{Provider: cli.Provider, Model: cli.Model}
	r.Window = catalog.GetContextWindow(cli.Provider, cli.Model)
	r.CharsPerToken, r.CalibrationSamples = globalTokenCalibrator.CharsPerToken(cli.Provider, cli.Model)
	r.Prompt = cli.promptBreakdowns.latest()
	r.History = summarizeHistory(cli.history)

	toTokens := func(chars int) int {
		if chars <= 0 || r.CharsPerToken <= 0 {
			return 0
		}
		return int(float64(chars)/r.CharsPerToken + 0.5)
	}
	// The system message is part of the history slice; the breakdown
	// describes its composition. Count history WITHOUT the system role so
	// prompt and history are not summed twice.
	historyChars := 0
	for _, rs := range r.History.Roles {
		if rs.Role != "system" {
			historyChars += rs.Chars
		}
	}
	r.PromptTokens = toTokens(r.Prompt.TotalChars())
	r.HistoryTokens = toTokens(historyChars)
	r.TotalTokens = r.PromptTokens + r.HistoryTokens
	if r.Window > 0 {
		r.ProjectedPct = float64(r.TotalTokens) / float64(r.Window) * 100
		cfg := DefaultCompactConfig(cli.Provider, cli.Model)
		r.CompactBudgetTokens = int(float64(r.Window) * cfg.BudgetRatio)
		r.CompactAtPct = cfg.BudgetRatio * 100
	}
	if cli.costTracker != nil {
		r.Cache = cli.costTracker.CacheStats()
	}
	return r
}

// showContextStatus renders the inspector.
func (cli *ChatCLI) showContextStatus() {
	r := cli.buildContextStatusReport()
	p := uiPrefix(ColorCyan)

	fmt.Println(colorize(i18n.T("context.status.header"), ColorLime+ColorBold))
	fmt.Println(colorize(strings.Repeat("─", 80), ColorGray))
	fmt.Printf("\n  %s %s/%s\n", colorize(i18n.T("context.status.model"), ColorCyan), r.Provider, r.Model)
	if r.Window > 0 {
		fmt.Printf("  %s %s\n", colorize(i18n.T("context.status.window"), ColorCyan), formatTokenCount(int64(r.Window)))
	}
	if r.CalibrationSamples > 0 {
		fmt.Printf("  %s %s\n", colorize(i18n.T("context.status.ratio"), ColorCyan),
			i18n.T("context.status.ratio_learned", fmt.Sprintf("%.2f", r.CharsPerToken), r.CalibrationSamples))
	} else {
		fmt.Printf("  %s %s\n", colorize(i18n.T("context.status.ratio"), ColorCyan),
			i18n.T("context.status.ratio_default", fmt.Sprintf("%.0f", r.CharsPerToken)))
	}

	// System prompt sections.
	fmt.Printf("\n  %s\n", colorize(i18n.T("context.status.prompt_header"), ColorCyan))
	if r.Prompt == nil || len(r.Prompt.Sections) == 0 {
		fmt.Printf("    %s\n", colorize(i18n.T("context.status.prompt_none"), ColorGray))
	} else {
		fmt.Printf("    %s\n", colorize(i18n.T("context.status.prompt_mode", r.Prompt.Mode, r.Prompt.At.Format("15:04:05")), ColorGray))
		for _, sec := range r.Prompt.Sections {
			cached := " "
			if sec.Cached {
				cached = colorize("●", ColorGreen)
			}
			name := i18n.T("context.status.section." + sec.Name)
			fmt.Printf("    %s %-28s %8s  ≈%6s tok\n", cached, name,
				FormatPayloadSize(sec.Chars), formatTokenCount(int64(tokensFor(sec.Chars, r.CharsPerToken))))
		}
		fmt.Printf("    %s\n", colorize(i18n.T("context.status.prompt_cached_legend",
			formatTokenCount(int64(tokensFor(r.Prompt.CachedChars(), r.CharsPerToken))),
			formatTokenCount(int64(r.PromptTokens))), ColorGray))
	}

	// History.
	fmt.Printf("\n  %s\n", colorize(i18n.T("context.status.history_header"), ColorCyan))
	for _, rs := range r.History.Roles {
		if rs.Role == "system" {
			continue
		}
		fmt.Printf("      %-14s %4d msg %10s  ≈%6s tok\n", rs.Role, rs.Messages,
			FormatPayloadSize(rs.Chars), formatTokenCount(int64(tokensFor(rs.Chars, r.CharsPerToken))))
	}
	fmt.Printf("    %s\n", colorize(i18n.T("context.status.history_recoverable", r.History.Summaries, r.History.CCRMarkers), ColorGray))

	// Totals and thresholds.
	fmt.Printf("\n  %s\n", colorize(i18n.T("context.status.totals_header"), ColorCyan))
	fmt.Printf("    %s ≈%s tok\n", kitPad(i18n.T("context.status.total_prompt")), formatTokenCount(int64(r.PromptTokens)))
	fmt.Printf("    %s ≈%s tok\n", kitPad(i18n.T("context.status.total_history")), formatTokenCount(int64(r.HistoryTokens)))
	if r.Window > 0 {
		fmt.Printf("    %s ≈%s tok (%s)\n", kitPad(i18n.T("context.status.total_projected")),
			formatTokenCount(int64(r.TotalTokens)), colorize(fmt.Sprintf("%.0f%%", r.ProjectedPct), pctColor(r.ProjectedPct)))
		fmt.Printf("    %s %s\n", kitPad(i18n.T("context.status.compact_at")),
			i18n.T("context.status.compact_at_value", fmt.Sprintf("%.0f%%", r.CompactAtPct), formatTokenCount(int64(r.CompactBudgetTokens))))
	}
	if r.Cache.Reported() {
		state := i18n.T("context.status.cache_cold")
		if r.Cache.Warm {
			state = i18n.T("context.status.cache_warm")
		}
		fmt.Printf("    %s %s\n", kitPad(i18n.T("context.status.cache")),
			i18n.T("context.status.cache_value", fmt.Sprintf("%.0f%%", r.Cache.HitPct), r.Cache.TTL, state))
	}
	fmt.Println(p)
	fmt.Println(colorize("  "+i18n.T("context.status.legend"), ColorGray))
	fmt.Println()
}

func tokensFor(chars int, ratio float64) int {
	if chars <= 0 || ratio <= 0 {
		return 0
	}
	return int(float64(chars)/ratio + 0.5)
}

func kitPad(s string) string { return fmt.Sprintf("%-26s", s) }

func pctColor(pct float64) string {
	switch {
	case pct >= 90:
		return ColorRed
	case pct >= 70:
		return ColorYellow
	default:
		return ColorGreen
	}
}
