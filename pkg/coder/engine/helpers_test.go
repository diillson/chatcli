package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestCollectFiles(t *testing.T) {
	got := collectFiles(`"primary.go"`, []string{"a.go", "'b.go'"})
	want := []string{"primary.go", "a.go", "b.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("collectFiles = %v want %v", got, want)
	}

	// Empty primary is skipped.
	got = collectFiles("", []string{"x.go"})
	if !reflect.DeepEqual(got, []string{"x.go"}) {
		t.Errorf("collectFiles empty primary = %v", got)
	}
}

func TestParseCSVSet(t *testing.T) {
	set := parseCSVSet(" go , js ,, py ,")
	if len(set) != 3 {
		t.Fatalf("set size=%d want 3: %v", len(set), set)
	}
	for _, k := range []string{"go", "js", "py"} {
		if !set[k] {
			t.Errorf("set missing %q", k)
		}
	}
}

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" a , b ,, c ,")
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitCSV = %v want %v", got, want)
	}
}

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if fileExists(p) {
		t.Error("file should not exist yet")
	}
	if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if !fileExists(p) {
		t.Error("file should exist now")
	}
}

func TestReadFileWithLimit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}

	// No limit.
	content, truncated, err := readFileWithLimit(p, 0)
	if err != nil || truncated || content != "0123456789" {
		t.Errorf("no limit: content=%q truncated=%v err=%v", content, truncated, err)
	}

	// Truncated.
	content, truncated, err = readFileWithLimit(p, 4)
	if err != nil || !truncated || content != "0123" {
		t.Errorf("limit 4: content=%q truncated=%v err=%v", content, truncated, err)
	}

	// Missing file.
	if _, _, err := readFileWithLimit(filepath.Join(dir, "nope.txt"), 0); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestCreateBackup(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")

	// Missing file: no-op, no error.
	if err := createBackup(p); err != nil {
		t.Errorf("createBackup on missing file should be no-op: %v", err)
	}
	if fileExists(p + ".bak") {
		t.Error("no backup should be created for missing file")
	}

	// Existing file: backup created with same content.
	if err := os.WriteFile(p, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := createBackup(p); err != nil {
		t.Fatalf("createBackup: %v", err)
	}
	bak, err := os.ReadFile(p + ".bak")
	if err != nil {
		t.Fatalf("reading backup: %v", err)
	}
	if string(bak) != "data" {
		t.Errorf("backup content = %q", bak)
	}
}

func TestDetectTestCommand(t *testing.T) {
	cases := []struct {
		marker string
		want   string
	}{
		{"go.mod", "go test ./..."},
		{"package.json", "npm test"},
		{"pyproject.toml", "pytest -q"},
		{"Cargo.toml", "cargo test"},
		{"pom.xml", "mvn test"},
		{"build.gradle", "gradle test"},
	}
	for _, c := range cases {
		t.Run(c.marker, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, c.marker), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
			if got := detectTestCommand(dir); got != c.want {
				t.Errorf("detectTestCommand(%s) = %q want %q", c.marker, got, c.want)
			}
		})
	}

	// No markers -> empty.
	if got := detectTestCommand(t.TempDir()); got != "" {
		t.Errorf("empty dir = %q want \"\"", got)
	}
}

func TestSplitCSVStableOrder(t *testing.T) {
	got := splitCSV("c,a,b")
	// splitCSV preserves input order (no sort).
	if !reflect.DeepEqual(got, []string{"c", "a", "b"}) {
		t.Errorf("splitCSV order = %v", got)
	}
	sorted := append([]string(nil), got...)
	sort.Strings(sorted)
	if reflect.DeepEqual(got, sorted) && len(got) > 1 {
		// sanity: just confirms our input was unsorted
		t.Log("input happened to be sorted")
	}
}
