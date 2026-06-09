/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/utils"
)

func lines(n int, prefix string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(prefix)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestSegmentFiles_RespectsMaxCharsAndOverlap(t *testing.T) {
	// 20 lines of ~10 chars each → ~200 chars; MaxChars 60 forces several windows.
	f := utils.FileInfo{Path: "a.go", Type: "go", Content: lines(20, "0123456789")}
	segs := SegmentFiles([]utils.FileInfo{f}, SegmentOptions{MaxChars: 60, OverlapLines: 2})

	if len(segs) < 2 {
		t.Fatalf("expected multiple segments, got %d", len(segs))
	}
	for i, s := range segs {
		if len(s.Content) > 60+11 { // soft cap + one line slack
			t.Errorf("segment %d exceeds soft cap: %d chars", i, len(s.Content))
		}
		if s.StartLine < 1 || s.EndLine < s.StartLine {
			t.Errorf("segment %d bad line range %d-%d", i, s.StartLine, s.EndLine)
		}
	}
	// Overlap: each segment after the first should start at or before the
	// previous segment's end (replayed lines).
	for i := 1; i < len(segs); i++ {
		if segs[i].StartLine > segs[i-1].EndLine+1 {
			t.Errorf("gap between segment %d (end %d) and %d (start %d)",
				i-1, segs[i-1].EndLine, i, segs[i].StartLine)
		}
	}
}

func TestSegmentFiles_DeterministicIDs(t *testing.T) {
	f := utils.FileInfo{Path: "a.go", Type: "go", Content: lines(30, "hello world")}
	a := SegmentFiles([]utils.FileInfo{f}, SegmentOptions{MaxChars: 80})
	b := SegmentFiles([]utils.FileInfo{f}, SegmentOptions{MaxChars: 80})
	if len(a) != len(b) {
		t.Fatalf("nondeterministic segment count: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatalf("segment %d id not stable: %s vs %s", i, a[i].ID, b[i].ID)
		}
	}
}

func TestSegmentFiles_DistinctPathsDistinctIDs(t *testing.T) {
	body := lines(5, "same content here")
	a := SegmentFiles([]utils.FileInfo{{Path: "a.go", Content: body}}, SegmentOptions{})
	b := SegmentFiles([]utils.FileInfo{{Path: "b.go", Content: body}}, SegmentOptions{})
	if len(a) == 0 || len(b) == 0 {
		t.Fatal("expected segments")
	}
	if a[0].ID == b[0].ID {
		t.Fatal("identical content in different files must yield distinct ids")
	}
}

func TestSegmentFiles_SkipsEmptyAndWhitespace(t *testing.T) {
	if got := SegmentFiles([]utils.FileInfo{{Path: "e.go", Content: "   \n\n  \t"}}, SegmentOptions{}); len(got) != 0 {
		t.Fatalf("whitespace-only file should yield no segments, got %d", len(got))
	}
}

func TestSegmentFiles_OverLongSingleLineEmits(t *testing.T) {
	long := strings.Repeat("x", 5000)
	segs := SegmentFiles([]utils.FileInfo{{Path: "big.txt", Content: long}}, SegmentOptions{MaxChars: 100})
	if len(segs) != 1 || segs[0].StartLine != 1 {
		t.Fatalf("a single over-long line must still emit one segment, got %d", len(segs))
	}
}

func TestSegmentFiles_NoStallWithLargeOverlap(t *testing.T) {
	// Overlap larger than the window height must not loop forever.
	f := utils.FileInfo{Path: "a.go", Content: lines(50, "line")}
	segs := SegmentFiles([]utils.FileInfo{f}, SegmentOptions{MaxChars: 30, OverlapLines: 32})
	if len(segs) == 0 {
		t.Fatal("expected progress, got no segments")
	}
	// Must reach the end of the file.
	last := segs[len(segs)-1]
	if last.EndLine < 50 {
		t.Fatalf("segmentation stalled before EOF, last end=%d", last.EndLine)
	}
}
