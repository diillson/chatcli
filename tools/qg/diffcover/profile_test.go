package diffcover

import (
	"strings"
	"testing"
)

func TestParseProfile_HappyPath(t *testing.T) {
	in := `mode: atomic
github.com/diillson/chatcli/cli/cli.go:30.10,32.16 2 1
github.com/diillson/chatcli/cli/cli.go:35.2,38.3 4 0
github.com/diillson/chatcli/llm/openai/openai_client.go:50.1,55.20 6 12
`
	p, err := ParseProfile(strings.NewReader(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Mode != "atomic" {
		t.Errorf("mode = %q, want atomic", p.Mode)
	}
	if got := len(p.Blocks); got != 2 {
		t.Fatalf("len(Blocks) = %d, want 2 files", got)
	}

	cli := p.Blocks["github.com/diillson/chatcli/cli/cli.go"]
	if len(cli) != 2 {
		t.Fatalf("cli.go blocks = %d, want 2", len(cli))
	}
	if cli[0] != (CoverBlock{StartLine: 30, EndLine: 32, NumStmts: 2, Count: 1}) {
		t.Errorf("block[0] = %+v", cli[0])
	}
	if cli[1] != (CoverBlock{StartLine: 35, EndLine: 38, NumStmts: 4, Count: 0}) {
		t.Errorf("block[1] = %+v", cli[1])
	}
}

func TestParseProfile_EmptyMode(t *testing.T) {
	in := `github.com/foo/bar.go:1.1,2.2 1 1`
	_, err := ParseProfile(strings.NewReader(in))
	if err == nil {
		t.Fatal("expected error for missing mode header, got nil")
	}
	if !strings.Contains(err.Error(), "missing mode") {
		t.Errorf("error message = %q, want mention of missing mode", err.Error())
	}
}

func TestParseProfile_IgnoresBlankLines(t *testing.T) {
	in := "mode: set\n\n\n  \nfoo.go:1.1,2.2 1 0\n"
	p, err := ParseProfile(strings.NewReader(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Blocks["foo.go"]) != 1 {
		t.Errorf("expected 1 block for foo.go, got %d", len(p.Blocks["foo.go"]))
	}
}

func TestParseProfile_MergesDuplicateEntriesMaxWins(t *testing.T) {
	// `go test -coverpkg=./... ./...` emits the same block multiple times
	// (one per test binary). The merge must keep "covered" (count>0) over
	// any subsequent "uncovered" (count=0) entry — otherwise files
	// measured by a tangential test binary look entirely uncovered to us.
	in := `mode: atomic
github.com/diillson/chatcli/foo.go:10.1,12.2 2 5
github.com/diillson/chatcli/foo.go:10.1,12.2 2 0
github.com/diillson/chatcli/foo.go:20.1,22.2 1 0
github.com/diillson/chatcli/foo.go:20.1,22.2 1 7
`
	p, err := ParseProfile(strings.NewReader(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	blocks := p.Blocks["github.com/diillson/chatcli/foo.go"]
	if len(blocks) != 2 {
		t.Fatalf("expected 2 unique blocks after merge, got %d", len(blocks))
	}
	// Both unique blocks must be marked covered.
	for _, b := range blocks {
		if b.Count == 0 {
			t.Errorf("merge failed: block %+v has count=0", b)
		}
	}
}

func TestParseProfile_MalformedRecord(t *testing.T) {
	cases := []string{
		"mode: set\nno-colon-here garbage\n",
		"mode: set\nfoo.go:malformed 1 2\n",
		"mode: set\nfoo.go:1.1,2.2 not-a-num 0\n",
		"mode: set\nfoo.go:1.1,2.2 1 not-a-num\n",
	}
	for _, in := range cases {
		_, err := ParseProfile(strings.NewReader(in))
		if err == nil {
			t.Errorf("expected error for input:\n%s", in)
		}
	}
}

func TestCoverBlock_Covers(t *testing.T) {
	b := CoverBlock{StartLine: 10, EndLine: 20}
	for _, tc := range []struct {
		line int
		want bool
	}{
		{9, false},
		{10, true},
		{15, true},
		{20, true},
		{21, false},
	} {
		if got := b.Covers(tc.line); got != tc.want {
			t.Errorf("Covers(%d) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestTotalCoverage(t *testing.T) {
	p := &Profile{
		Mode: "atomic",
		Blocks: map[string][]CoverBlock{
			"a.go": {
				{StartLine: 1, EndLine: 2, NumStmts: 3, Count: 1}, // covered
				{StartLine: 5, EndLine: 6, NumStmts: 2, Count: 0}, // uncovered
			},
			"b.go": {
				{StartLine: 1, EndLine: 1, NumStmts: 5, Count: 10}, // covered
			},
		},
	}
	cov, tot, pct := p.TotalCoverage()
	if tot != 10 {
		t.Errorf("total stmts = %d, want 10", tot)
	}
	if cov != 8 {
		t.Errorf("covered stmts = %d, want 8", cov)
	}
	if pct < 79.9 || pct > 80.1 {
		t.Errorf("percent = %.2f, want ~80", pct)
	}
}

func TestTotalCoverage_Empty(t *testing.T) {
	p := &Profile{Mode: "set", Blocks: map[string][]CoverBlock{}}
	cov, tot, pct := p.TotalCoverage()
	if cov != 0 || tot != 0 || pct != 0 {
		t.Errorf("empty profile = (%d, %d, %.2f), want (0, 0, 0)", cov, tot, pct)
	}
}

func TestStripPrefixes_LongestWins(t *testing.T) {
	p := &Profile{
		Mode: "atomic",
		Blocks: map[string][]CoverBlock{
			"github.com/diillson/chatcli/cli/cli.go":              {{StartLine: 1, EndLine: 2}},
			"github.com/diillson/chatcli/operator/main.go":        {{StartLine: 1, EndLine: 2}},
			"github.com/diillson/chatcli/operator/api/types.go":   {{StartLine: 5, EndLine: 6}},
		},
	}
	out := p.StripPrefixes([]string{
		"github.com/diillson/chatcli",
		"github.com/diillson/chatcli/operator",
	})

	want := map[string]bool{
		"cli/cli.go":          true,
		"operator/main.go":    false, // operator prefix is LONGER, must win
		"main.go":             true,
		"api/types.go":        true,
	}
	for k, wantPresent := range want {
		if _, ok := out.Blocks[k]; ok != wantPresent {
			t.Errorf("key %q: present=%v, want %v", k, ok, wantPresent)
		}
	}
}

func TestStripPrefixes_NoMatchKeepsOriginal(t *testing.T) {
	p := &Profile{
		Mode:   "set",
		Blocks: map[string][]CoverBlock{"unrelated/foo.go": {{StartLine: 1, EndLine: 1}}},
	}
	out := p.StripPrefixes([]string{"github.com/something/else"})
	if _, ok := out.Blocks["unrelated/foo.go"]; !ok {
		t.Errorf("expected unrelated path to be preserved")
	}
}
