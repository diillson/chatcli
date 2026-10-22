package agent

import (
	"fmt"
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

// The preview keeps what the budget allows: with an 80k cap most of the
// result stays inline, with the marker naming the omitted lines, the file
// and the @read call that continues from the first omitted line.
func TestPreviewWithDiskPersist_SizedToTheBudgetWithLineHint(t *testing.T) {
	dir := t.TempDir()
	SetBudgetResultDir(dir)
	t.Cleanup(func() { SetBudgetResultDir("") })

	var sb strings.Builder
	for i := 1; i <= 5000; i++ {
		fmt.Fprintf(&sb, "line %04d of the tool output\n", i)
	}
	content := sb.String() // ~140k
	got := PreviewWithDiskPersist(content, "read_1", 80_000, nil)
	if len(got) > 80_000 {
		t.Fatalf("preview must fit the budget: %d", len(got))
	}
	if len(got) < 60_000 {
		t.Fatalf("preview must keep most of the budget, got only %d chars", len(got))
	}
	if !strings.Contains(got, "line 0001 of") || !strings.Contains(got, "line 5000 of") {
		t.Fatal("preview must keep the head and the tail")
	}
	for _, want := range []string{"chars omitted (lines ", " of 5000) — full output saved to " + dir, `continue with @read {"file":"` + dir, `"from_line":`} {
		if !strings.Contains(got, want) {
			t.Fatalf("marker missing %q:\n%s", want, got[len(got)-600:])
		}
	}
	// The named first omitted line is the first line not in the head.
	i := strings.Index(got, "chars omitted (lines ")
	var first, last, total int
	if _, err := fmt.Sscanf(got[i:], "chars omitted (lines %d-%d of %d)", &first, &last, &total); err != nil {
		t.Fatal(err)
	}
	head := got[:strings.Index(got, "\n\n... [")]
	if first != strings.Count(head, "\n")+1 || last >= 5000 || last <= first || total != 5000 {
		t.Fatalf("line range %d-%d of %d does not match the head/tail", first, last, total)
	}
	if files := overflowFiles(t, dir); len(files) != 1 {
		t.Fatalf("one overflow file expected, got %v", files)
	}
	// A small budget still shows both ends.
	small := PreviewWithDiskPersist(content, "read_2", 3000, nil)
	if !strings.Contains(small, "line 0001 of") || !strings.Contains(small, "line 5000 of") {
		t.Fatal("small budget must keep both ends")
	}
	if PreviewWithDiskPersist("short", "x", 0, nil) != "short" {
		t.Fatal("content under the budget passes through")
	}
}

// A tool that declared a larger cap than the per-result default keeps it in
// the per-result pass; unknown calls get the default.
func TestEnforceToolResultBudgetFor_HonorsPerCallCaps(t *testing.T) {
	dir := t.TempDir()
	SetBudgetResultDir(dir)
	t.Cleanup(func() { SetBudgetResultDir("") })

	payload := strings.Repeat("x\n", 25_000) // 50k, over the 20k default
	caps := map[string]int{"call_1": 80_000}
	capFor := func(id string) int { return caps[id] }

	kept, report := EnforceToolResultBudgetFor(oversizedHistory("call_1", payload), 0, 0, capFor, nil)
	if report.ResultsTruncated != 0 || kept[2].Content != payload {
		t.Fatalf("a result under its own cap must stay whole (truncated=%d)", report.ResultsTruncated)
	}
	cut, report := EnforceToolResultBudgetFor(oversizedHistory("call_2", payload), 0, 0, capFor, nil)
	if report.ResultsTruncated != 1 || len(cut[2].Content) > DefaultPerResultMaxChars {
		t.Fatalf("an unknown call gets the default cap (truncated=%d, len=%d)", report.ResultsTruncated, len(cut[2].Content))
	}
	// A cap below the default never lowers it.
	caps["call_3"] = 1000
	low, report := EnforceToolResultBudgetFor(oversizedHistory("call_3", strings.Repeat("y\n", 5000)), 0, 0, capFor, nil)
	if report.ResultsTruncated != 0 || len(low[2].Content) != 10_000 {
		t.Fatalf("a per-call cap below the default must not cut (truncated=%d, len=%d)", report.ResultsTruncated, len(low[2].Content))
	}
}
