package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/workspace/memory"
	"go.uber.org/zap"
)

// The extraction request is split by lifetime: instructions and the
// existing memory as cached system blocks, the segment alone as the user
// message, and nothing sent twice.
func TestBuildExtractionRequestIsCacheShaped(t *testing.T) {
	prompt, history := buildExtractionRequest("INSTRUCTIONS", "/ws", "EXISTING MEMORY", "user: hi\nassistant: hello")
	if len(history) != 2 || history[0].Role != "system" || history[1].Role != "user" {
		t.Fatalf("history shape: %+v", history)
	}
	sys := history[0]
	if len(sys.SystemParts) != 2 || sys.SystemParts[0].Text != "INSTRUCTIONS" || sys.SystemParts[0].CacheControl == nil || sys.SystemParts[1].CacheControl == nil {
		t.Fatalf("system parts must be two cached blocks: %+v", sys.SystemParts)
	}
	if !strings.Contains(sys.SystemParts[1].Text, "/ws") || !strings.Contains(sys.SystemParts[1].Text, "EXISTING MEMORY") {
		t.Fatalf("workspace and existing memory belong to the second block: %q", sys.SystemParts[1].Text)
	}
	if strings.Count(sys.Content, "INSTRUCTIONS") != 1 || !strings.Contains(sys.Content, "EXISTING MEMORY") {
		t.Fatalf("flat content carries each block once: %q", sys.Content)
	}
	if strings.Contains(prompt, "INSTRUCTIONS") || strings.Contains(prompt, "EXISTING MEMORY") {
		t.Fatalf("the user message is the segment only: %q", prompt)
	}
	if !strings.HasPrefix(prompt, extractionSegmentHeader) || history[1].Content != prompt {
		t.Fatalf("prompt and last user message must match: %q", prompt)
	}
	_, bare := buildExtractionRequest("I", "", "", "seg")
	if len(bare[0].SystemParts) != 1 || bare[0].Content != "I" {
		t.Fatalf("without context there is one block: %+v", bare[0])
	}
}

// Both sides of the memory balance are counted and survive the cost
// snapshot, so /cost can say what the worker wrote and what was read.
func TestMemoryROIIsCountedAndPersisted(t *testing.T) {
	ct := NewCostTracker()
	ct.RecordMemoryUsage("CLAUDEAI", "claude-sonnet-5", realUsage(4000, 0, 0))
	ct.RecordMemoryOutcome(3, 1)
	ct.RecordMemoryRecall(2)
	ct.RecordMemoryRecall(0)
	r := ct.MemoryROIStats()
	if r.Calls != 1 || r.FactsWritten != 3 || r.EpisodesWritten != 1 || r.Recalls != 1 || r.FactsRecalled != 2 {
		t.Fatalf("roi = %+v", r)
	}
	if r.CostPerFact() <= 0 || (MemoryROI{}).CostPerFact() != 0 {
		t.Fatal("cost per item")
	}
	line := memoryROILine(r)
	for _, want := range []string{"3", "1", "2"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q lacks %q", line, want)
		}
	}
	if !strings.Contains(memoryROILine(MemoryROI{}), "—") {
		t.Error("nothing written renders a dash for the per-item cost")
	}
	if err := ct.SaveSession(); err != nil {
		t.Fatal(err)
	}
	back := NewCostTracker()
	if err := back.RestoreSession(ct.Snapshot().SessionID); err != nil {
		t.Fatal(err)
	}
	if back.MemoryROIStats() != r {
		t.Fatalf("roi must survive the snapshot: %+v vs %+v", back.MemoryROIStats(), r)
	}
	var none *CostTracker
	none.RecordMemoryOutcome(1, 1)
	none.RecordMemoryRecall(1)
	if none.MemoryROIStats() != (MemoryROI{}) {
		t.Fatal("nil tracker")
	}
}

// A proactive recall that shows facts is the read side of the balance.
func TestRecalledFactsFeedTheMemoryBalance(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop(), costTracker: NewCostTrackerAt(t.TempDir())}
	cli.noteRecalledFacts([]*memory.Fact{{ID: "f1", Content: "prefer table driven tests"}, nil, {ID: "", Content: "x"}})
	if r := cli.costTracker.MemoryROIStats(); r.Recalls != 1 || r.FactsRecalled != 2 {
		t.Fatalf("roi = %+v", r)
	}
}
