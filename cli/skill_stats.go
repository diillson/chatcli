/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Skill effectiveness: what a skill did for the runs it took part in.
 *
 * Skills enter a run in three ways — pinned, auto-activated by trigger,
 * or authored by the self-evolve engine from an earlier run — and until
 * now nothing measured whether any of them helped. A learned skill that
 * makes runs longer or costlier accumulates next to one that shortens
 * them, and the operator has no number to prune by. This ledger records,
 * per skill, the runs it was injected into and what those runs cost in
 * turns, tool calls and dollars, next to a baseline of every run, so
 * /skill stats can put the two side by side. It is provider-neutral by
 * construction: turns and tool calls come from the run registry and the
 * cost from the tracker, both of which every provider feeds.
 */
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// skillStatsFile is the ledger's name inside the skills directory.
const skillStatsFile = ".skill-stats.json"

// skillRunStats accumulates the runs one skill took part in (or, for the
// baseline, every run).
type skillRunStats struct {
	Runs      int       `json:"runs"`
	RunsOK    int       `json:"runs_ok"`
	Turns     int       `json:"turns"`
	ToolCalls int       `json:"tool_calls"`
	CostUSD   float64   `json:"cost_usd"`
	LastUsed  time.Time `json:"last_used,omitempty"`
}

// add folds one run in.
func (s *skillRunStats) add(turns, toolCalls int, cost float64, ok bool, at time.Time) {
	s.Runs++
	if ok {
		s.RunsOK++
	}
	s.Turns += turns
	s.ToolCalls += toolCalls
	s.CostUSD += cost
	if at.After(s.LastUsed) {
		s.LastUsed = at
	}
}

// Averages per run; zero when the skill has no runs.
func (s skillRunStats) avgTurns() float64 {
	if s.Runs == 0 {
		return 0
	}
	return float64(s.Turns) / float64(s.Runs)
}

func (s skillRunStats) avgToolCalls() float64 {
	if s.Runs == 0 {
		return 0
	}
	return float64(s.ToolCalls) / float64(s.Runs)
}

func (s skillRunStats) avgCost() float64 {
	if s.Runs == 0 {
		return 0
	}
	return s.CostUSD / float64(s.Runs)
}

func (s skillRunStats) errorPct() float64 {
	if s.Runs == 0 {
		return 0
	}
	return float64(s.Runs-s.RunsOK) / float64(s.Runs) * 100
}

// skillStatsLedger is the persisted record.
type skillStatsLedger struct {
	path     string
	mu       sync.Mutex
	Baseline skillRunStats             `json:"baseline"`
	Skills   map[string]*skillRunStats `json:"skills"`
}

// skillStatsPath resolves the ledger location; "" when the skills
// directory is unavailable, in which case nothing is recorded.
func skillStatsPath() string {
	dir, err := plugins.SkillsDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, skillStatsFile)
}

// loadSkillStats reads the ledger, returning an empty usable one on any
// error so a corrupt file never blocks a run.
func loadSkillStats() *skillStatsLedger {
	return loadSkillStatsAt(skillStatsPath())
}

func loadSkillStatsAt(path string) *skillStatsLedger {
	l := &skillStatsLedger{path: path, Skills: map[string]*skillRunStats{}}
	if path == "" {
		return l
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path derived from the fixed skills dir
	if err != nil {
		return l
	}
	if err := json.Unmarshal(data, l); err != nil || l.Skills == nil {
		l.Skills = map[string]*skillRunStats{}
	}
	return l
}

// save writes the ledger atomically with owner-only permissions.
func (l *skillStatsLedger) save() error {
	if l == nil || l.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".skill-stats-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), l.path)
}

// recordRun folds one finished run into the baseline and into every
// skill that took part in it.
func (l *skillStatsLedger) recordRun(skills []string, turns, toolCalls int, cost float64, ok bool, at time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if cost < 0 {
		cost = 0
	}
	l.Baseline.add(turns, toolCalls, cost, ok, at)
	for _, name := range skills {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		s := l.Skills[name]
		if s == nil {
			s = &skillRunStats{}
			l.Skills[name] = s
		}
		s.add(turns, toolCalls, cost, ok, at)
	}
}

// recordSkillRunOutcome is the run-end hook: what this run cost, credited
// to the skills injected into it and to the baseline. Never fails the
// run — a ledger that cannot be written is logged and skipped.
func (a *AgentMode) recordSkillRunOutcome(turns, toolCalls int, runErr error) {
	if a == nil || a.cli == nil {
		return
	}
	cost := 0.0
	if a.cli.costTracker != nil {
		cost = a.cli.costTracker.TotalCost() - a.runStartCost
	}
	ledger := loadSkillStats()
	ledger.recordRun(a.InjectedSkillNames(), turns, toolCalls, cost, runErr == nil, time.Now())
	if err := ledger.save(); err != nil && a.logger != nil {
		a.logger.Debug("skill stats not recorded", zap.Error(err))
	}
}

// skillStatsRow is one rendered line of /skill stats.
type skillStatsRow struct {
	Name    string
	Learned bool
	Stats   skillRunStats
}

// skillStatsRows lists the ledger's skills, most used first, marking the
// ones the self-evolve engine authored.
func skillStatsRows(l *skillStatsLedger, learned map[string]string) []skillStatsRow {
	if l == nil {
		return nil
	}
	rows := make([]skillStatsRow, 0, len(l.Skills))
	for name, s := range l.Skills {
		if s == nil {
			continue
		}
		_, isLearned := learned[name]
		rows = append(rows, skillStatsRow{Name: name, Learned: isLearned, Stats: *s})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Stats.Runs != rows[j].Stats.Runs {
			return rows[i].Stats.Runs > rows[j].Stats.Runs
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}

// deltaPct is how far a skill's average sits from the baseline's, as a
// signed percentage; 0 when the baseline is empty.
func deltaPct(skill, baseline float64) float64 {
	if baseline <= 0 {
		return 0
	}
	return (skill - baseline) / baseline * 100
}

// renderSkillStats builds the /skill stats view. filter narrows it to one
// skill ("" for all).
func renderSkillStats(l *skillStatsLedger, learned map[string]string, filter string) []string {
	rows := skillStatsRows(l, learned)
	if filter = strings.TrimSpace(filter); filter != "" {
		kept := rows[:0]
		for _, r := range rows {
			if strings.EqualFold(r.Name, filter) {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	var out []string
	if len(rows) == 0 {
		return append(out, i18n.T("skill.stats.empty"))
	}
	b := l.Baseline
	out = append(out, i18n.T("skill.stats.baseline", b.Runs,
		fmt.Sprintf("%.1f", b.avgTurns()), fmt.Sprintf("%.1f", b.avgToolCalls()),
		fmt.Sprintf("$%.4f", b.avgCost()), fmt.Sprintf("%.0f%%", b.errorPct())))
	for _, r := range rows {
		s := r.Stats
		origin := i18n.T("skill.stats.origin_installed")
		if r.Learned {
			origin = i18n.T("skill.stats.origin_learned")
		}
		out = append(out, i18n.T("skill.stats.row", r.Name, origin, s.Runs,
			fmt.Sprintf("%.1f", s.avgTurns()), fmt.Sprintf("%+.0f%%", deltaPct(s.avgTurns(), b.avgTurns())),
			fmt.Sprintf("%.1f", s.avgToolCalls()),
			fmt.Sprintf("$%.4f", s.avgCost()), fmt.Sprintf("%+.0f%%", deltaPct(s.avgCost(), b.avgCost())),
			fmt.Sprintf("%.0f%%", s.errorPct())))
	}
	return out
}

// ShowStats prints the effectiveness table.
func (sh *SkillHandler) ShowStats(filter string) {
	ledger := loadSkillStats()
	learned := loadSelfEvolveManifest().Skills
	fmt.Println()
	fmt.Printf("  %s\n", colorize(i18n.T("skill.stats.header"), ColorCyan))
	fmt.Printf("  %s\n", colorize(strings.Repeat("─", 50), ColorGray))
	for i, line := range renderSkillStats(ledger, learned, filter) {
		color := ColorReset
		if i == 0 {
			color = ColorGray
		}
		fmt.Printf("  %s\n", colorize(line, color))
	}
	fmt.Printf("  %s\n\n", colorize(i18n.T("skill.stats.legend"), ColorGray))
}
