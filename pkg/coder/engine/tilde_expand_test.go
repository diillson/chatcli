package engine

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

func TestExpandUserPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	cases := map[string]string{
		"~":              home,
		"~/proj/main.go": filepath.Join(home, "proj", "main.go"),
		"./rel/path":     "./rel/path", // untouched
		"/abs/path":      "/abs/path",  // untouched
		"~user/x":        "~user/x",    // ~username NOT expanded
		"":               "",           // empty
	}
	for in, want := range cases {
		if got := expandUserPath(in); got != want {
			t.Errorf("expandUserPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestParseFlagsExpandsFileTilde proves the single-chokepoint fix: a --file
// argument starting with ~ is expanded before any command uses it for I/O, so
// the engine never creates a literal "~" directory.
func TestParseFlagsExpandsFileTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	fs := flag.NewFlagSet("write", flag.ContinueOnError)
	file := fs.String("file", "", "")
	dir := fs.String("dir", ".", "")
	if err := parseFlags(fs, []string{"--file", "~/project/main.go", "--dir", "~/project"}); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "project", "main.go"); *file != want {
		t.Errorf("--file = %q, want expanded %q", *file, want)
	}
	if want := filepath.Join(home, "project"); *dir != want {
		t.Errorf("--dir = %q, want expanded %q", *dir, want)
	}
}

func TestParseFlagsLeavesNonTildeUntouched(t *testing.T) {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	file := fs.String("file", "", "")
	if err := parseFlags(fs, []string{"--file", "./cli/main.go"}); err != nil {
		t.Fatal(err)
	}
	if *file != "./cli/main.go" {
		t.Errorf("relative path should be untouched, got %q", *file)
	}
}
