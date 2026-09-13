package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

func oversizedHistory(id, payload string) []models.Message {
	return []models.Message{
		{Role: "user", Content: "run it"},
		{Role: "assistant", Content: "", ToolCalls: []models.ToolCall{{ID: id}}},
		{Role: "tool", ToolCallID: id, Content: payload},
	}
}

func overflowFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// The budget runs on the outgoing copy of the history on every request, so
// the preview it derives for one result must be the same bytes each time:
// a preview that changes rewrites the provider's cached prefix from that
// message onward on every turn.
func TestOverflowPreviewIsByteStableAcrossRequests(t *testing.T) {
	dir := t.TempDir()
	SetBudgetResultDir(dir)
	t.Cleanup(func() { SetBudgetResultDir("") })

	payload := strings.Repeat("line of tool output\n", DefaultPerResultMaxChars/10)
	first, report := EnforceToolResultBudget(oversizedHistory("call_1", payload), nil)
	if report.ResultsTruncated != 1 {
		t.Fatalf("expected one truncation, got %+v", report)
	}
	second, _ := EnforceToolResultBudget(oversizedHistory("call_1", payload), nil)

	if first[2].Content != second[2].Content {
		t.Fatalf("preview changed between requests:\n%q\n%q", first[2].Content, second[2].Content)
	}
	if !strings.Contains(first[2].Content, dir) {
		t.Fatalf("preview should reference the overflow file: %q", first[2].Content)
	}
	if files := overflowFiles(t, dir); len(files) != 1 {
		t.Fatalf("the same result must map to one file, got %v", files)
	}
}

// A provider that reuses tool-call ids (Kimi K3 does) must not have two
// different results share one overflow file.
func TestOverflowFileFollowsContentNotOnlyID(t *testing.T) {
	dir := t.TempDir()
	SetBudgetResultDir(dir)
	t.Cleanup(func() { SetBudgetResultDir("") })

	a := strings.Repeat("alpha\n", DefaultPerResultMaxChars/5)
	b := strings.Repeat("bravo\n", DefaultPerResultMaxChars/5)
	ha, _ := EnforceToolResultBudget(oversizedHistory("call_1", a), nil)
	hb, _ := EnforceToolResultBudget(oversizedHistory("call_1", b), nil)

	if ha[2].Content == hb[2].Content {
		t.Fatal("different results must not share a preview")
	}
	if files := overflowFiles(t, dir); len(files) != 2 {
		t.Fatalf("expected two overflow files, got %v", files)
	}
	for _, name := range overflowFiles(t, dir) {
		if !strings.HasPrefix(name, "budget_call_1_") || !strings.HasSuffix(name, ".txt") {
			t.Fatalf("unexpected overflow file name %q", name)
		}
	}
}

// The name is a function of id and content only, and hostile ids cannot
// escape the overflow directory.
func TestOverflowFileNameIsDeterministicAndSafe(t *testing.T) {
	one := overflowFileName("call/../x", "same")
	two := overflowFileName("call/../x", "same")
	if one != two {
		t.Fatalf("name must be deterministic: %q vs %q", one, two)
	}
	if strings.ContainsAny(one, "/\\") || strings.Contains(one, "..") {
		t.Fatalf("name must stay inside the directory: %q", one)
	}
	if overflowFileName("", "x") != overflowFileName("   ", "x") {
		t.Fatal("an empty id must normalize consistently")
	}
	if overflowFileName("id", "a") == overflowFileName("id", "b") {
		t.Fatal("different content must produce different names")
	}
}
