package diffcover

import (
	"strings"
	"testing"
)

func TestParseUnifiedDiff_SimpleAddedLines(t *testing.T) {
	in := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -10,0 +11,3 @@
+line eleven
+line twelve
+line thirteen
`
	d, err := ParseUnifiedDiff(strings.NewReader(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(d.Files); got != 1 {
		t.Fatalf("len(Files) = %d, want 1", got)
	}
	added := d.Files["foo.go"].AddedLines
	for _, n := range []int{11, 12, 13} {
		if _, ok := added[n]; !ok {
			t.Errorf("line %d not in AddedLines", n)
		}
	}
	if _, ok := added[14]; ok {
		t.Errorf("line 14 should not be added")
	}
}

func TestParseUnifiedDiff_PureDeletionHunkIsIgnored(t *testing.T) {
	in := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -10,2 +10,0 @@
-was here
-also was here
`
	d, err := ParseUnifiedDiff(strings.NewReader(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(d.Files["foo.go"].AddedLines); got != 0 {
		t.Errorf("pure deletion should add no lines, got %d", got)
	}
}

func TestParseUnifiedDiff_MultipleHunksAndFiles(t *testing.T) {
	in := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -5,0 +6 @@
+single line at 6
@@ -20,0 +25,2 @@
+line 25
+line 26
diff --git a/bar.go b/bar.go
--- a/bar.go
+++ b/bar.go
@@ -1,0 +1,1 @@
+line 1 of bar
`
	d, err := ParseUnifiedDiff(strings.NewReader(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantFoo := []int{6, 25, 26}
	for _, n := range wantFoo {
		if _, ok := d.Files["foo.go"].AddedLines[n]; !ok {
			t.Errorf("foo.go line %d missing", n)
		}
	}
	if _, ok := d.Files["bar.go"].AddedLines[1]; !ok {
		t.Errorf("bar.go line 1 missing")
	}
}

func TestParseUnifiedDiff_MixedHunkAdvancesCorrectly(t *testing.T) {
	// Hunk with both removed and added lines under --unified=0: the new
	// line counter must not advance for `-` lines.
	in := `diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -10,2 +10,3 @@
-old line
-another old
+new 10
+new 11
+new 12
`
	d, err := ParseUnifiedDiff(strings.NewReader(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := d.Files["x.go"].AddedLines
	for _, n := range []int{10, 11, 12} {
		if _, ok := got[n]; !ok {
			t.Errorf("line %d missing", n)
		}
	}
	if _, ok := got[13]; ok {
		t.Errorf("line 13 should not be added")
	}
}

func TestParseUnifiedDiff_SingleLineHunkCountDefaultsTo1(t *testing.T) {
	// Git emits "@@ -5 +5 @@" (no comma) when count is 1.
	in := `diff --git a/y.go b/y.go
--- a/y.go
+++ b/y.go
@@ -5 +5 @@
-old
+new
`
	d, err := ParseUnifiedDiff(strings.NewReader(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := d.Files["y.go"].AddedLines[5]; !ok {
		t.Errorf("expected line 5 to be added")
	}
}

func TestParseUnifiedDiff_RenameToDestPath(t *testing.T) {
	in := `diff --git a/old.go b/new.go
similarity index 80%
rename from old.go
rename to new.go
--- a/old.go
+++ b/new.go
@@ -1,0 +1,2 @@
+a
+b
`
	d, err := ParseUnifiedDiff(strings.NewReader(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The destination path is what shows up in `git diff --diff-filter=AM`;
	// our parser must attribute the added lines to it.
	if _, ok := d.Files["new.go"]; !ok {
		t.Fatalf("new.go missing from diff result")
	}
	if got := len(d.Files["new.go"].AddedLines); got != 2 {
		t.Errorf("new.go added lines = %d, want 2", got)
	}
}

func TestParseUnifiedDiff_EmptyDiff(t *testing.T) {
	d, err := ParseUnifiedDiff(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(d.Files) != 0 {
		t.Errorf("expected no files in empty diff")
	}
}
