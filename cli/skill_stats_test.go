package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// A run is credited to the baseline and to every skill it used; the
// averages and the error share follow, and the ledger survives a save
// and load. A corrupt file yields an empty usable ledger.
func TestSkillStatsLedgerRecordsAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), skillStatsFile)
	l := loadSkillStatsAt(path)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	l.recordRun([]string{"alpha", " ", "beta"}, 4, 6, 0.20, true, now, nil)
	l.recordRun(nil, 2, 1, 0.05, true, now.Add(time.Minute), nil)
	l.recordRun([]string{"alpha"}, 8, 10, -1, false, now.Add(2*time.Minute), nil) // a negative cost clamps to zero

	if l.Baseline.Runs != 3 || l.Baseline.RunsOK != 2 || l.Baseline.Turns != 14 {
		t.Fatalf("baseline = %+v", l.Baseline)
	}
	alpha := l.Skills["alpha"]
	if alpha == nil || alpha.Runs != 2 || alpha.avgTurns() != 6 || alpha.avgToolCalls() != 8 || alpha.errorPct() != 50 {
		t.Fatalf("alpha = %+v", alpha)
	}
	if l.Skills["beta"] == nil || l.Skills[" "] != nil || l.Skills[""] != nil {
		t.Fatalf("blank names are skipped: %v", l.Skills)
	}
	if !alpha.LastUsed.Equal(now.Add(2 * time.Minute)) {
		t.Errorf("last used = %v", alpha.LastUsed)
	}
	if err := l.save(); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("ledger must be owner-only, got %v", st.Mode().Perm())
	}
	back := loadSkillStatsAt(path)
	if back.Baseline != l.Baseline || back.Skills["alpha"].Runs != 2 || back.Skills["beta"].CostUSD != 0.20 {
		t.Fatalf("round trip lost data: %+v", back)
	}
	_ = os.WriteFile(path, []byte("{not json"), 0o600)
	if broken := loadSkillStatsAt(path); broken.Skills == nil || len(broken.Skills) != 0 {
		t.Fatal("a corrupt ledger must load empty, not fail")
	}
	if empty := loadSkillStatsAt(""); empty.save() != nil {
		t.Fatal("a ledger without a path saves nothing and never errors")
	}
	var none *skillStatsLedger
	none.recordRun([]string{"x"}, 1, 1, 1, true, now, nil)
	if none.save() != nil {
		t.Fatal("nil ledger")
	}
}

// The view puts each skill next to the baseline, marks learned skills,
// and narrows to one name on request.
func TestRenderSkillStats(t *testing.T) {
	l := loadSkillStatsAt("")
	now := time.Now()
	l.recordRun([]string{"flamengo-match-info"}, 2, 1, 0.08, true, now, nil)
	l.recordRun([]string{"go-refactor"}, 12, 20, 1.20, false, now, nil)
	l.recordRun(nil, 6, 8, 0.50, true, now, nil)
	learned := map[string]string{"flamengo-match-info": "hash"}

	lines := renderSkillStats(l, learned, "")
	if len(lines) != 3 || !strings.Contains(lines[0], "3 runs") {
		t.Fatalf("expected baseline plus two rows, got %q", lines)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"flamengo-match-info", "go-refactor", "learned", "installed", "-", "+"} {
		if !strings.Contains(joined, want) {
			t.Errorf("view lacks %q:\n%s", want, joined)
		}
	}
	only := renderSkillStats(l, learned, "GO-REFACTOR")
	if len(only) != 2 || !strings.Contains(only[1], "go-refactor") {
		t.Fatalf("filter must keep one row: %q", only)
	}
	if empty := renderSkillStats(loadSkillStatsAt(""), nil, ""); len(empty) != 1 {
		t.Fatalf("no skills yet: %q", empty)
	}
	if deltaPct(5, 0) != 0 || deltaPct(5, 10) != -50 {
		t.Fatal("delta arithmetic")
	}
	if rows := skillStatsRows(nil, nil); rows != nil {
		t.Fatal("nil ledger has no rows")
	}
}

// The run-end hook credits the run's own spend, turns and tool calls to
// the skills injected into it, into the ledger under the skills dir.
func TestRecordSkillRunOutcomeCreditsInjectedSkills(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop(), costTracker: NewCostTrackerAt(t.TempDir())}
	a := &AgentMode{cli: cli, logger: zap.NewNop(), injectedSkillNames: map[string]bool{"alpha": true}}
	a.runStartCost = cli.costTracker.TotalCost()
	cli.costTracker.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", &models.UsageInfo{IsReal: true, PromptTokens: 100000, CompletionTokens: 1000})
	a.recordSkillRunOutcome(5, 7, nil)

	l := loadSkillStats()
	if l.path == "" {
		t.Skip("skills directory unavailable in this environment")
	}
	s := l.Skills["alpha"]
	if s == nil || s.Runs != 1 || s.Turns != 5 || s.ToolCalls != 7 || s.CostUSD <= 0 || s.RunsOK != 1 {
		t.Fatalf("alpha = %+v", s)
	}
	if l.Baseline.Runs != 1 {
		t.Fatalf("baseline = %+v", l.Baseline)
	}
	var none *AgentMode
	none.recordSkillRunOutcome(1, 1, nil)
}
