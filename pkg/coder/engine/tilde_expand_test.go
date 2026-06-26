package engine

import (
	"flag"
	"os"
	"path/filepath"
	"runtime"
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

// TestExpandEnvSetVar proves "$VAR"/"${VAR}" references mid-path are resolved,
// matching what the agent is told to do (e.g. "$CHATCLI_AGENT_TMPDIR/patch.sh").
func TestExpandEnvSetVar(t *testing.T) {
	t.Setenv("CHATCLI_AGENT_TMPDIR", filepath.FromSlash("/tmp/chatcli-scratch"))
	cases := map[string]string{
		"$CHATCLI_AGENT_TMPDIR/patch.sh":   filepath.FromSlash("/tmp/chatcli-scratch") + "/patch.sh",
		"${CHATCLI_AGENT_TMPDIR}/patch.sh": filepath.FromSlash("/tmp/chatcli-scratch") + "/patch.sh",
		"./rel/$CHATCLI_AGENT_TMPDIR":      "./rel/" + filepath.FromSlash("/tmp/chatcli-scratch"),
	}
	for in, want := range cases {
		if got := expandEnv(in); got != want {
			t.Errorf("expandEnv(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestExpandEnvUnsetIsPreserved is the safety contract: an unset variable must
// NOT collapse to "" (which would turn "$UNSET/x" into "/x" and retarget a
// write to the filesystem root). The literal reference is kept so downstream
// validation/IO fails loudly instead.
func TestExpandEnvUnsetIsPreserved(t *testing.T) {
	os.Unsetenv("CHATCLI_DEFINITELY_UNSET_VAR")
	got := expandEnv("$CHATCLI_DEFINITELY_UNSET_VAR/x")
	if got != "${CHATCLI_DEFINITELY_UNSET_VAR}/x" {
		t.Errorf("unset var must be preserved, got %q", got)
	}
	if got == "/x" {
		t.Fatal("unset var collapsed to root-relative path — silent retarget bug")
	}
}

// TestExpandPathEnvThenTilde proves env vars are resolved before "~", so a
// variable whose value itself begins with "~" still lands in the home dir.
func TestExpandPathEnvThenTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	t.Setenv("CHATCLI_TEST_HOMEISH", "~/sub")
	if got, want := expandPath("$CHATCLI_TEST_HOMEISH/file"), filepath.Join(home, "sub", "file"); got != want {
		t.Errorf("expandPath = %q, want %q", got, want)
	}
}

// TestParseFlagsExpandsEnvVar is the end-to-end chokepoint proof for env vars:
// a --file/--dir carrying "$VAR" is resolved before any command does I/O.
func TestParseFlagsExpandsEnvVar(t *testing.T) {
	scratch := filepath.FromSlash("/tmp/chatcli-scratch")
	t.Setenv("CHATCLI_AGENT_TMPDIR", scratch)
	fs := flag.NewFlagSet("write", flag.ContinueOnError)
	file := fs.String("file", "", "")
	if err := parseFlags(fs, []string{"--file", "$CHATCLI_AGENT_TMPDIR/out.txt"}); err != nil {
		t.Fatal(err)
	}
	if want := scratch + "/out.txt"; *file != want {
		t.Errorf("--file = %q, want expanded %q", *file, want)
	}
}

// TestExpandEnvWindowsPercent covers the Windows-native "%VAR%" syntax. The
// expansion is OS-gated, so on POSIX a literal "%" must survive untouched.
func TestExpandEnvWindowsPercent(t *testing.T) {
	t.Setenv("CHATCLI_AGENT_TMPDIR", `C:\scratch`)
	got := expandEnv(`%CHATCLI_AGENT_TMPDIR%\out.txt`)
	if runtime.GOOS == "windows" {
		if want := `C:\scratch\out.txt`; got != want {
			t.Errorf("expandEnv(%%VAR%%) = %q, want %q", got, want)
		}
	} else {
		if got != `%CHATCLI_AGENT_TMPDIR%\out.txt` {
			t.Errorf("%%VAR%% must be untouched on %s, got %q", runtime.GOOS, got)
		}
	}
}
