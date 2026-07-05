package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeDailyNoteAt fabricates a daily note file for a past date, in the same
// YYYYMM/YYYYMMDD.md layout DailyNoteStore uses. Synthetic content only.
func writeDailyNoteAt(t *testing.T, dir string, date time.Time, bullets ...string) {
	t.Helper()
	monthDir := filepath.Join(dir, date.Format("200601"))
	if err := os.MkdirAll(monthDir, 0o750); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	sb.WriteString("## 10:00\n\n")
	for _, b := range bullets {
		sb.WriteString("- " + b + "\n")
	}
	if err := os.WriteFile(filepath.Join(monthDir, date.Format("20060102")+".md"), []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRollup_WeeklyAndMonthlyDeterministic(t *testing.T) {
	dir := t.TempDir()
	rs := NewRollupStore(dir, testLogger())

	// Two days in a long-elapsed week (~10 weeks ago), plus today (must NOT
	// roll — its week is still running).
	old := time.Now().AddDate(0, 0, -70)
	monday := startOfISOWeek(old)
	writeDailyNoteAt(t, dir, monday, "implementou o parser sintético", "corrigiu teste flaky")
	writeDailyNoteAt(t, dir, monday.AddDate(0, 0, 2), "decidiu usar fila em memória")
	writeDailyNoteAt(t, dir, time.Now(), "trabalho de hoje")

	written, err := rs.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if written < 1 {
		t.Fatalf("expected at least one digest written, got %d", written)
	}

	year, week := monday.ISOWeek()
	weeklyPath := filepath.Join(dir, "weekly", fmt.Sprintf("%04d-W%02d.md", year, week))
	data, err := os.ReadFile(weeklyPath)
	if err != nil {
		t.Fatalf("weekly digest missing: %v", err)
	}
	for _, want := range []string{"parser sintético", "fila em memória"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("weekly digest missing %q:\n%s", want, data)
		}
	}

	// The elapsed month must have a monthly digest too (weeklies preferred).
	if monday.Format("2006-01") != time.Now().Format("2006-01") {
		monthly := filepath.Join(dir, "monthly", monday.Format("2006-01")+".md")
		if _, err := os.Stat(monthly); err != nil {
			t.Errorf("monthly digest missing: %v", err)
		}
	}

	// Current week must not be rolled.
	yNow, wNow := time.Now().ISOWeek()
	if _, err := os.Stat(filepath.Join(dir, "weekly", fmt.Sprintf("%04d-W%02d.md", yNow, wNow))); err == nil {
		t.Error("running week must not be rolled up")
	}

	// Idempotent: a second run writes nothing new.
	again, err := rs.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("second run must be a no-op, wrote %d", again)
	}
}

func TestRollup_LLMSummarizerPreferred(t *testing.T) {
	dir := t.TempDir()
	rs := NewRollupStore(dir, testLogger())
	old := startOfISOWeek(time.Now().AddDate(0, 0, -21))
	writeDailyNoteAt(t, dir, old, "conteúdo bruto do dia")

	summarize := func(_ context.Context, prompt string) (string, error) {
		if !strings.Contains(prompt, "conteúdo bruto do dia") {
			return "", fmt.Errorf("prompt missing source")
		}
		return "- resumo vindo do LLM", nil
	}
	if _, err := rs.Run(context.Background(), summarize); err != nil {
		t.Fatal(err)
	}
	year, week := old.ISOWeek()
	data, err := os.ReadFile(filepath.Join(dir, "weekly", fmt.Sprintf("%04d-W%02d.md", year, week)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "resumo vindo do LLM") {
		t.Errorf("LLM digest must be used when available:\n%s", data)
	}
}

func TestRollup_FormatTrajectoryAndRetention(t *testing.T) {
	dir := t.TempDir()
	rs := NewRollupStore(dir, testLogger())

	// Fabricate more weeklies than the retention cap, plus one monthly.
	weeklyDir := filepath.Join(dir, "weekly")
	if err := os.MkdirAll(weeklyDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < weeklyRetention+4; i++ {
		name := fmt.Sprintf("2025-W%02d.md", i+1)
		if err := os.WriteFile(filepath.Join(weeklyDir, name), []byte("# Week\n- item semana "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	monthlyDir := filepath.Join(dir, "monthly")
	if err := os.MkdirAll(monthlyDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(monthlyDir, "2025-06.md"), []byte("# Month 2025-06\n- destaque do mês"), 0o600); err != nil {
		t.Fatal(err)
	}

	traj := rs.FormatTrajectory(600)
	if !strings.Contains(traj, "destaque do mês") {
		t.Errorf("trajectory must include latest monthly:\n%s", traj)
	}
	if len(traj) > 600 {
		t.Errorf("trajectory must respect the budget, got %d chars", len(traj))
	}

	if _, err := rs.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := len(rs.sortedFiles("weekly")); got != weeklyRetention {
		t.Errorf("retention must cap weeklies at %d, got %d", weeklyRetention, got)
	}
}

func TestManager_MemoryContextIncludesTrajectory(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, DefaultConfig(), testLogger())

	if err := os.MkdirAll(filepath.Join(dir, "monthly"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "monthly", "2025-05.md"), []byte("# Month 2025-05\n- marco sintético do mês"), 0o600); err != nil {
		t.Fatal(err)
	}
	mgr.Profile.Update(map[string]string{"name": "Fulano"})

	ctx := mgr.GetMemoryContext()
	if !strings.Contains(ctx, "## Trajectory") || !strings.Contains(ctx, "marco sintético") {
		t.Errorf("memory context must include trajectory section:\n%s", ctx)
	}
}
