package diffcover

import (
	"strings"
	"testing"
)

// includeAll is a trivial filter used by tests that don't exercise the
// include/exclude logic itself.
func includeAll(string) bool { return true }

func TestCompute_AllCovered(t *testing.T) {
	p := &Profile{Mode: "set", Blocks: map[string][]CoverBlock{
		"foo.go": {
			{StartLine: 10, EndLine: 20, NumStmts: 5, Count: 3},
		},
	}}
	d := &Diff{Files: map[string]*FileDiff{
		"foo.go": {Path: "foo.go", AddedLines: map[int]struct{}{
			12: {}, 13: {}, 15: {},
		}},
	}}
	res, uninstr := Compute(p, d, includeAll, 60)
	if len(uninstr) != 0 {
		t.Errorf("unexpected uninstrumented files: %v", uninstr)
	}
	if !res.Passed() {
		t.Errorf("expected pass; percent=%.1f", res.Percent())
	}
	if res.Percent() != 100 {
		t.Errorf("expected 100%%, got %.1f", res.Percent())
	}
}

func TestCompute_PartialCoverage(t *testing.T) {
	p := &Profile{Mode: "set", Blocks: map[string][]CoverBlock{
		"foo.go": {
			{StartLine: 10, EndLine: 14, Count: 1},
			{StartLine: 20, EndLine: 24, Count: 0},
		},
	}}
	d := &Diff{Files: map[string]*FileDiff{
		"foo.go": {Path: "foo.go", AddedLines: map[int]struct{}{
			11: {}, 12: {}, 22: {}, 23: {},
		}},
	}}
	res, _ := Compute(p, d, includeAll, 60)
	if res.Total != 4 {
		t.Errorf("total = %d, want 4", res.Total)
	}
	if res.Covered != 2 {
		t.Errorf("covered = %d, want 2", res.Covered)
	}
	if got := res.Percent(); got < 49.9 || got > 50.1 {
		t.Errorf("percent = %.2f, want ~50", got)
	}
	if res.Passed() {
		t.Errorf("should not pass at 50%% with threshold 60")
	}
}

func TestCompute_NonExecutableLinesIgnored(t *testing.T) {
	// Added lines outside any cover block (comments, blanks, braces) must
	// NOT count towards Total — they aren't executable Go statements.
	p := &Profile{Mode: "set", Blocks: map[string][]CoverBlock{
		"foo.go": {{StartLine: 10, EndLine: 12, Count: 1}},
	}}
	d := &Diff{Files: map[string]*FileDiff{
		"foo.go": {Path: "foo.go", AddedLines: map[int]struct{}{
			3: {}, 5: {}, // outside any block — comments/blanks
			11: {}, // inside the covered block
		}},
	}}
	res, _ := Compute(p, d, includeAll, 60)
	if res.Total != 1 {
		t.Errorf("non-executable added lines should not count; got total=%d", res.Total)
	}
	if res.Covered != 1 {
		t.Errorf("covered = %d, want 1", res.Covered)
	}
}

func TestCompute_UninstrumentedFileIsReported(t *testing.T) {
	// A Go file in the diff with NO profile entries means the test
	// invocation didn't measure that package. We must NOT silently treat
	// it as 100% — the wrapper script needs to refuse to pass.
	p := &Profile{Mode: "set", Blocks: map[string][]CoverBlock{}}
	d := &Diff{Files: map[string]*FileDiff{
		"new.go": {Path: "new.go", AddedLines: map[int]struct{}{
			1: {}, 2: {}, 3: {},
		}},
	}}
	res, uninstr := Compute(p, d, includeAll, 60)
	if len(uninstr) != 1 || uninstr[0] != "new.go" {
		t.Errorf("expected new.go in uninstrumented, got %v", uninstr)
	}
	if res.Total != 0 {
		t.Errorf("uninstrumented files must not contribute to total; got %d", res.Total)
	}
}

func TestCompute_IncludeFilterSkips(t *testing.T) {
	p := &Profile{Mode: "set", Blocks: map[string][]CoverBlock{
		"foo_test.go": {{StartLine: 1, EndLine: 5, Count: 0}},
	}}
	d := &Diff{Files: map[string]*FileDiff{
		"foo_test.go": {Path: "foo_test.go", AddedLines: map[int]struct{}{2: {}}},
	}}
	include := func(path string) bool { return !strings.HasSuffix(path, "_test.go") }
	res, _ := Compute(p, d, include, 60)
	if res.Total != 0 {
		t.Errorf("test files should be filtered out; got total=%d", res.Total)
	}
}

func TestCompute_FilesWithOnlyNonExecAreDropped(t *testing.T) {
	// File where all added lines are non-executable should not appear in
	// PerFile (otherwise the markdown would show 0/0 rows).
	p := &Profile{Mode: "set", Blocks: map[string][]CoverBlock{
		"foo.go": {{StartLine: 100, EndLine: 110, Count: 1}},
	}}
	d := &Diff{Files: map[string]*FileDiff{
		"foo.go": {Path: "foo.go", AddedLines: map[int]struct{}{1: {}, 2: {}}}, // outside the block
	}}
	res, _ := Compute(p, d, includeAll, 60)
	if len(res.Files) != 0 {
		t.Errorf("expected no per-file rows for non-executable-only files, got %d", len(res.Files))
	}
}

func TestResult_PercentEmpty(t *testing.T) {
	r := Result{Threshold: 60}
	if r.Percent() != 100 {
		t.Errorf("empty result should be 100%%, got %.1f", r.Percent())
	}
	if !r.Passed() {
		t.Errorf("empty result should pass")
	}
}

func TestMatchAny(t *testing.T) {
	if !MatchAny("foo/bar.go", []string{"*.go"}) {
		t.Error("*.go should match nested foo/bar.go")
	}
	if MatchAny("foo.txt", []string{"*.go"}) {
		t.Error("*.go should not match foo.txt")
	}
	if !MatchAny("anything", nil) {
		t.Error("empty patterns should match anything")
	}
}

func TestMatchNone(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		patterns []string
		want     bool // true means NOT excluded
	}{
		// Suffix globs must reach nested paths (the bug Floor 3 caught locally).
		{"_test suffix nested", "tools/qg/diffcover/compute_test.go", []string{"*_test.go"}, false},
		{"_test suffix root", "compute_test.go", []string{"*_test.go"}, false},
		{".go does not match .txt", "foo.txt", []string{"*.go"}, true},
		// Recursive prefix.
		{"proto/** matches descendant", "proto/x.go", []string{"proto/**"}, false},
		{"proto/** matches dir itself", "proto", []string{"proto/**"}, false},
		{"proto/** does not match sibling", "protocol.go", []string{"proto/**"}, true},
		// Anywhere-by-basename.
		{"**/foo.pb.go nested", "a/b/foo.pb.go", []string{"**/foo.pb.go"}, false},
		{"**/foo.pb.go basename only", "foo.pb.go", []string{"**/foo.pb.go"}, false},
		{"**/foo.pb.go not a match", "foo.pb.go.bak", []string{"**/foo.pb.go"}, true},
		// Combined patterns — only one needs to match for exclusion.
		{"multi-pattern hits last", "tools/docgen/main.go", []string{"*_test.go", "tools/docgen/**"}, false},
		// No patterns means no exclusion.
		{"empty patterns include", "anything.go", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchNone(tc.path, tc.patterns); got != tc.want {
				t.Errorf("MatchNone(%q, %v) = %v, want %v", tc.path, tc.patterns, got, tc.want)
			}
		})
	}
}

func TestFormatLineList(t *testing.T) {
	cases := []struct {
		in   []int
		want string
	}{
		{nil, ""},
		{[]int{5}, "5"},
		{[]int{1, 2, 3}, "1-3"},
		{[]int{1, 2, 3, 5, 6, 9}, "1-3, 5-6, 9"},
		{[]int{1, 3, 5}, "1, 3, 5"},
	}
	for _, tc := range cases {
		if got := formatLineList(tc.in); got != tc.want {
			t.Errorf("formatLineList(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatMarkdown_PassingResultHasNoTable(t *testing.T) {
	r := Result{Total: 0, Covered: 0, Threshold: 60}
	md := r.FormatMarkdown()
	if !strings.Contains(md, "100.0%") {
		t.Errorf("expected 100%% summary, got: %s", md)
	}
	if strings.Contains(md, "| File |") {
		t.Errorf("empty result should not render a table")
	}
}

func TestFormatMarkdown_IncludesPerFileRows(t *testing.T) {
	r := Result{
		Total:     10,
		Covered:   6,
		Threshold: 60,
		Files: []FileResult{
			{Path: "a.go", Total: 5, Covered: 5},
			{Path: "b.go", Total: 5, Covered: 1, MissingLines: []int{10, 11, 12, 20}},
		},
	}
	md := r.FormatMarkdown()
	if !strings.Contains(md, "`a.go`") || !strings.Contains(md, "`b.go`") {
		t.Errorf("missing per-file rows: %s", md)
	}
	if !strings.Contains(md, "10-12") {
		t.Errorf("missing line range compaction: %s", md)
	}
}
