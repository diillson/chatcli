package ctxmgr

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/utils"
)

func goFile(path, body string) utils.FileInfo {
	return utils.FileInfo{Path: path, Type: ".go", Content: body}
}

// A passage cut from the middle of a file used to reach the index saying
// nothing about where it came from. Retrieval then had to match on the
// passage's own words alone.
func TestPassageCarriesItsFileAndEnclosingStructure(t *testing.T) {
	body := "package auth\n\n" +
		"func ValidateToken(raw string) error {\n" +
		strings.Repeat("\t// reasoning about the token\n", 60) +
		"\treturn nil\n}\n"
	segs := SegmentFiles([]utils.FileInfo{goFile("auth/token.go", body)},
		SegmentOptions{MaxChars: 400, Situate: true, Boundaries: true})

	if len(segs) < 2 {
		t.Fatalf("want the file split into several passages, got %d", len(segs))
	}
	// A later passage — the one that used to arrive anonymous.
	tail := segs[len(segs)-1]
	if !strings.Contains(tail.Context, "auth/token.go") {
		t.Errorf("passage must name its file, got %q", tail.Context)
	}
	if !strings.Contains(tail.Context, "ValidateToken") {
		t.Errorf("passage must name the declaration enclosing it, got %q", tail.Context)
	}
	// The header is indexed, never rendered.
	if strings.Contains(tail.Content, "auth/token.go") {
		t.Error("the header must not be written into the passage content")
	}
	if !strings.HasPrefix(tail.IndexText(), tail.Context) {
		t.Error("IndexText must lead with the header")
	}
	if !strings.HasSuffix(tail.IndexText(), tail.Content) {
		t.Error("IndexText must end with the passage itself")
	}
}

// Opting in is per corpus, so existing indexes keep the vectors they paid
// for.
func TestSituatingIsOptIn(t *testing.T) {
	body := "package x\n\nfunc F() {\n" + strings.Repeat("\t// line\n", 40) + "}\n"
	off := SegmentFiles([]utils.FileInfo{goFile("x/f.go", body)}, SegmentOptions{MaxChars: 400})
	for _, s := range off {
		if s.Context != "" {
			t.Fatalf("a corpus that did not opt in must carry no header: %q", s.Context)
		}
		if s.IndexText() != s.Content {
			t.Fatal("IndexText must equal Content when there is no header")
		}
	}
}

// Passage ids key the vector cache, so they name what was embedded — not
// only what was cut. This test used to assert the opposite, on the belief
// that an opted-in corpus "re-embeds because its indexed text changed";
// it does not. Re-embedding is driven by the ids the index reports
// missing, so an unchanged id means an unchanged vector, and the situating
// header never reached a single one. The cut is still identical either
// way: only the id and the indexed text move.
func TestSituatingMovesPassageIDsSoVectorsAreReEarned(t *testing.T) {
	body := "package x\n\nfunc F() {\n" + strings.Repeat("\t// line\n", 40) + "}\n"
	files := []utils.FileInfo{goFile("x/f.go", body)}
	off := SegmentFiles(files, SegmentOptions{MaxChars: 400})
	on := SegmentFiles(files, SegmentOptions{MaxChars: 400, Situate: true})
	if len(off) != len(on) {
		t.Fatalf("segment count changed: %d vs %d", len(off), len(on))
	}
	for i := range off {
		if off[i].Content != on[i].Content {
			t.Errorf("passage %d content changed with situating: the cut must be identical", i)
		}
		if on[i].Context == "" {
			t.Errorf("passage %d opted in but carries no header", i)
			continue
		}
		if off[i].ID == on[i].ID {
			t.Errorf("passage %d kept its id though its indexed text changed: the vector would never be re-earned", i)
		}
	}

	// Situating twice is not a second migration: the id is a function of
	// the cut and the header, so a corpus already situated stays put.
	again := SegmentFiles(files, SegmentOptions{MaxChars: 400, Situate: true})
	for i := range on {
		if on[i].ID != again[i].ID {
			t.Errorf("passage %d id is not stable across re-segmentation", i)
		}
	}
}

func TestMarkdownPassageCarriesItsHeading(t *testing.T) {
	body := "# Guide\n\n## Installing the operator\n\n" + strings.Repeat("Some prose about it.\n", 40)
	segs := SegmentFiles([]utils.FileInfo{{Path: "docs/guide.md", Type: ".md", Content: body}},
		SegmentOptions{MaxChars: 300, Situate: true, Boundaries: true})
	var found bool
	for _, s := range segs {
		if strings.Contains(s.Context, "Installing the operator") {
			found = true
		}
	}
	if !found {
		t.Errorf("a passage under a heading must carry it; headers were %v", contextsOf(segs))
	}
}

// A passage that opens with its own structure says so by its first line.
func TestPassageOpeningOnAStructureTakesTheFileAlone(t *testing.T) {
	lines := []string{"package x", "", "func F() {", "\treturn", "}"}
	if got := enclosingStructure(lines, 3, ".go"); got != "" {
		t.Errorf("a passage starting on a declaration needs no enclosing one, got %q", got)
	}
}

func TestStructureLineIsTrimmedToOnePhrase(t *testing.T) {
	if got := trimStructureLine("## Installing the operator"); got != "Installing the operator" {
		t.Errorf("heading markers must go, got %q", got)
	}
	if got := trimStructureLine("func VeryLongName(a, b, c string) error {"); strings.HasSuffix(got, "{") {
		t.Errorf("a trailing brace must go, got %q", got)
	}
	long := "func " + strings.Repeat("x", 300) + "()"
	if got := trimStructureLine(long); len(got) > situateHeaderMax+4 {
		t.Errorf("a long declaration must be bounded, got %d chars", len(got))
	}
}

func contextsOf(segs []Segment) []string {
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		out = append(out, s.Context)
	}
	return out
}
