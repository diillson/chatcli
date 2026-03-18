package memory

import (
	"os"
	"testing"

	"go.uber.org/zap"
)

func testLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

func TestFactIndex_AddAndDedup(t *testing.T) {
	dir := t.TempDir()
	fi := NewFactIndex(dir, DefaultConfig(), testLogger())

	added := fi.AddFact("Go project uses Bubble Tea", "architecture", []string{"go", "bubbletea"})
	if !added {
		t.Error("expected fact to be added")
	}

	// Same content should deduplicate
	added2 := fi.AddFact("Go project uses Bubble Tea", "architecture", nil)
	if added2 {
		t.Error("expected duplicate to be rejected")
	}

	if fi.Count() != 1 {
		t.Errorf("expected 1 fact, got %d", fi.Count())
	}
}

func TestFactIndex_Search(t *testing.T) {
	dir := t.TempDir()
	fi := NewFactIndex(dir, DefaultConfig(), testLogger())

	fi.AddFact("Go project uses Bubble Tea for TUI", "architecture", []string{"go", "bubbletea"})
	fi.AddFact("Python project uses Flask", "architecture", []string{"python", "flask"})
	fi.AddFact("OAuth requires plain http.Client", "gotcha", []string{"auth"})

	results := fi.Search([]string{"go", "bubble"})
	if len(results) == 0 {
		t.Fatal("expected search results")
	}
	if results[0].Content != "Go project uses Bubble Tea for TUI" {
		t.Errorf("expected Go fact first, got %q", results[0].Content)
	}
}

func TestFactIndex_MaxFacts(t *testing.T) {
	dir := t.TempDir()
	config := DefaultConfig()
	config.MaxFactsCount = 5
	fi := NewFactIndex(dir, config, testLogger())

	for i := 0; i < 10; i++ {
		fi.AddFact(string(rune('A'+i))+" some fact content here", "general", nil)
	}

	if fi.Count() > 5 {
		t.Errorf("expected max 5 facts, got %d", fi.Count())
	}
}

func TestFactIndex_Persistence(t *testing.T) {
	dir := t.TempDir()
	fi := NewFactIndex(dir, DefaultConfig(), testLogger())

	fi.AddFact("Persistent fact", "general", []string{"test"})

	// Create new instance from same dir
	fi2 := NewFactIndex(dir, DefaultConfig(), testLogger())
	if fi2.Count() != 1 {
		t.Errorf("expected 1 persisted fact, got %d", fi2.Count())
	}

	facts := fi2.GetAll()
	if len(facts) != 1 || facts[0].Content != "Persistent fact" {
		t.Error("persisted fact content mismatch")
	}
}

func TestFactIndex_GenerateMarkdown(t *testing.T) {
	dir := t.TempDir()
	fi := NewFactIndex(dir, DefaultConfig(), testLogger())

	fi.AddFact("Go uses goroutines for concurrency", "pattern", nil)
	fi.AddFact("User prefers concise answers", "preference", nil)

	md := fi.GenerateMarkdown(32 * 1024)
	if md == "" {
		t.Error("expected non-empty markdown")
	}
	if !containsStr(md, "Go uses goroutines") {
		t.Error("expected fact content in markdown")
	}
}

func TestFactIndex_Archive(t *testing.T) {
	dir := t.TempDir()
	fi := NewFactIndex(dir, DefaultConfig(), testLogger())

	fi.AddFact("Old fact that should decay", "general", nil)
	fi.AddFact("Another old fact", "general", nil)

	candidates := fi.GetArchiveCandidates(100.0) // threshold so high everything matches
	if len(candidates) != 2 {
		t.Errorf("expected 2 candidates, got %d", len(candidates))
	}

	archivePath := dir + "/archive.json"
	err := fi.ArchiveFacts(candidates, archivePath)
	if err != nil {
		t.Fatalf("archive failed: %v", err)
	}

	if fi.Count() != 0 {
		t.Errorf("expected 0 facts after archive, got %d", fi.Count())
	}

	// Verify archive file exists
	if _, err := os.Stat(archivePath); err != nil {
		t.Error("archive file not created")
	}
}

func containsStr(haystack, needle string) bool {
	return len(haystack) > 0 && len(needle) > 0 &&
		(haystack == needle || len(haystack) > len(needle) &&
			(func() bool {
				for i := 0; i <= len(haystack)-len(needle); i++ {
					if haystack[i:i+len(needle)] == needle {
						return true
					}
				}
				return false
			})())
}
