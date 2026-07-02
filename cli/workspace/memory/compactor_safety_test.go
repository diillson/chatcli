package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// seedFactContent yields the i-th distinct fact body. Each opens on a unique
// subject token so the add-time reconciliation (near-duplicate reinforce /
// same-subject supersede) never merges two seeds.
func seedFactContent(i int) string {
	return fmt.Sprintf("service%d listens on port %d and writes logs under /var/log/service%d", i, 3000+i, i)
}

// seedFacts adds n distinct facts and returns the index.
func seedFacts(t *testing.T, dir string, n int) *FactIndex {
	t.Helper()
	fi := NewFactIndex(dir, DefaultConfig(), zap.NewNop())
	for i := 0; i < n; i++ {
		if !fi.AddFact(seedFactContent(i), "general", nil) {
			t.Fatalf("seed fact %d not added", i)
		}
	}
	if fi.Count() != n {
		t.Fatalf("seed collapsed: %d facts stored, want %d", fi.Count(), n)
	}
	return fi
}

// TestCompactionRejectsTruncatedLLMOutput pins the shrink guard: models
// truncate long lists routinely, and a truncated consolidation used to be
// applied wholesale — permanently deleting every fact the model didn't echo.
// A result that keeps less than half the facts must be rejected (falling back
// to conservative score-based curation), never applied.
func TestCompactionRejectsTruncatedLLMOutput(t *testing.T) {
	dir := t.TempDir()
	fi := seedFacts(t, dir, 40)
	c := NewCompactor(fi, NewDailyNoteStore(dir, zap.NewNop()), DefaultConfig(), dir, zap.NewNop())

	truncated := func(ctx context.Context, prompt string) (string, error) {
		// The model "answers" with only 5 of the 40 facts.
		var b strings.Builder
		for i := 0; i < 5; i++ {
			fmt.Fprintf(&b, "- [general] %s\n", seedFactContent(i))
		}
		return b.String(), nil
	}

	if err := c.RunWithLLM(context.Background(), truncated); err != nil {
		t.Fatalf("RunWithLLM: %v", err)
	}
	if got := fi.Count(); got != 40 {
		t.Fatalf("truncated LLM output was applied: %d facts remain, want all 40 preserved", got)
	}
}

// TestCompactionArchivesRemovedFacts pins recoverability: facts a legitimate
// consolidation drops must land in memory_archive.json, not vanish.
func TestCompactionArchivesRemovedFacts(t *testing.T) {
	dir := t.TempDir()
	fi := seedFacts(t, dir, 20)
	c := NewCompactor(fi, NewDailyNoteStore(dir, zap.NewNop()), DefaultConfig(), dir, zap.NewNop())

	keep15 := func(ctx context.Context, prompt string) (string, error) {
		var b strings.Builder
		for i := 0; i < 15; i++ {
			fmt.Fprintf(&b, "- [general] %s\n", seedFactContent(i))
		}
		return b.String(), nil
	}

	if err := c.RunWithLLM(context.Background(), keep15); err != nil {
		t.Fatalf("RunWithLLM: %v", err)
	}
	if got := fi.Count(); got != 15 {
		t.Fatalf("Count = %d after consolidation, want 15", got)
	}

	data, err := os.ReadFile(filepath.Join(dir, "memory_archive.json"))
	if err != nil {
		t.Fatalf("dropped facts were not archived: %v", err)
	}
	var archived []*Fact
	if err := json.Unmarshal(data, &archived); err != nil {
		t.Fatalf("archive unparseable: %v", err)
	}
	if len(archived) != 5 {
		t.Fatalf("archive holds %d facts, want the 5 dropped ones", len(archived))
	}
}

// TestCompactionPreservesFactMetadata pins trust metadata across compaction:
// a user-stated fact (confidence 0.9, provenance "user", source project) must
// not be silently downgraded to an extraction-grade guess when the model
// echoes it back.
func TestCompactionPreservesFactMetadata(t *testing.T) {
	dir := t.TempDir()
	fi := NewFactIndex(dir, DefaultConfig(), zap.NewNop())
	for i := 0; i < 10; i++ {
		fi.AddFact(seedFactContent(i), "general", nil)
	}
	if fi.Count() != 10 {
		t.Fatalf("filler seed collapsed: %d", fi.Count())
	}
	content := "commits are always GPG-signed on project gamma"
	if !fi.AddFactWithMeta(content, "preference", []string{"git"}, "/ws/gamma", ConfidenceUser, ProvenanceUser) {
		t.Fatal("meta fact not added")
	}
	c := NewCompactor(fi, NewDailyNoteStore(dir, zap.NewNop()), DefaultConfig(), dir, zap.NewNop())

	echoAll := func(ctx context.Context, prompt string) (string, error) {
		var b strings.Builder
		for i := 0; i < 10; i++ {
			fmt.Fprintf(&b, "- [general] %s\n", seedFactContent(i))
		}
		b.WriteString("- [preference] " + content + "\n")
		return b.String(), nil
	}

	if err := c.RunWithLLM(context.Background(), echoAll); err != nil {
		t.Fatalf("RunWithLLM: %v", err)
	}

	id := fi.hashContent(content)
	f, ok := fi.GetByID(id)
	if !ok {
		t.Fatal("meta fact lost in compaction")
	}
	if f.Confidence != ConfidenceUser {
		t.Fatalf("Confidence downgraded by compaction: %v, want %v", f.Confidence, ConfidenceUser)
	}
	if f.Provenance != ProvenanceUser {
		t.Fatalf("Provenance lost in compaction: %q", f.Provenance)
	}
	if f.SourceProject != "/ws/gamma" {
		t.Fatalf("SourceProject lost in compaction: %q", f.SourceProject)
	}
}

// TestCompactionIntervalSurvivesRestart pins the trigger: lastCompaction used
// to live only in memory, so every process start with a well-filled index
// re-triggered the (destructive-adjacent, LLM-billed) compaction pass.
func TestCompactionIntervalSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.MaxFactsCount = 10 // 9 facts ≈ 90% of cap → the count trigger is armed
	fi := NewFactIndex(dir, cfg, zap.NewNop())
	for i := 0; i < 9; i++ {
		fi.AddFact(seedFactContent(i), "general", nil)
	}

	c1 := NewCompactor(fi, NewDailyNoteStore(dir, zap.NewNop()), cfg, dir, zap.NewNop())
	if err := c1.RunScoreBased(); err != nil { // completes a compaction "now"
		t.Fatal(err)
	}
	if c1.NeedsCompaction() {
		t.Fatal("compaction wanted immediately after completing one")
	}

	// "Restart": a fresh compactor over the same directory must remember that
	// a compaction just ran, not fire another one on startup.
	c2 := NewCompactor(fi, NewDailyNoteStore(dir, zap.NewNop()), cfg, dir, zap.NewNop())
	if c2.NeedsCompaction() {
		t.Fatal("restart forgot lastCompaction — compaction storms on every process start")
	}
}
