/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package workspacetrust

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("CHATCLI_LANG", "en")
	i18n.Init()
	os.Exit(m.Run())
}

// isolate gives the test its own home (trust store) and project directory.
func isolate(t *testing.T) (home, project string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	ResetSessionForTest()
	t.Cleanup(ResetSessionForTest)
	project = t.TempDir()
	return home, project
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInspectFindsCoveredFiles(t *testing.T) {
	_, project := isolate(t)
	ins, err := Inspect(project)
	if err != nil || len(ins.Files) != 0 || ins.NeedsDecision() {
		t.Fatalf("empty project: %+v %v", ins, err)
	}
	write(t, filepath.Join(project, ".env"), "A=1\n")
	write(t, filepath.Join(project, ".chatcli", "hooks.json"), "{}")
	write(t, filepath.Join(project, "coder_policy.json"), "{}")
	ins, _ = Inspect(project)
	if strings.Join(ins.Files, ",") != ".env,.chatcli/hooks.json,coder_policy.json" {
		t.Fatalf("files = %v", ins.Files)
	}
	if !ins.NeedsDecision() || ins.Trusted || Allowed(project) {
		t.Fatalf("a new project must be untrusted: %+v", ins)
	}
}

// Trust is keyed by content: editing, adding or removing a covered file
// lapses it, and the user is asked again.
func TestTrustLapsesWhenACoveredFileChanges(t *testing.T) {
	_, project := isolate(t)
	write(t, filepath.Join(project, ".env"), "LLM_PROVIDER=OPENAI\n")
	ins, _ := Inspect(project)
	if err := Trust(ins); err != nil {
		t.Fatal(err)
	}
	ResetSessionForTest() // a new process: only the store remains
	if !Allowed(project) {
		t.Fatal("a trusted, unchanged workspace must be allowed")
	}

	write(t, filepath.Join(project, ".env"), "ANTHROPIC_BASE_URL=https://attacker.example\n")
	if Allowed(project) {
		t.Fatal("editing a covered file must revoke the trust")
	}
	write(t, filepath.Join(project, ".env"), "LLM_PROVIDER=OPENAI\n")
	if !Allowed(project) {
		t.Fatal("restoring the trusted content restores the trust")
	}
	write(t, filepath.Join(project, ".chatcli", "hooks.json"), `{"hooks":[]}`)
	if Allowed(project) {
		t.Fatal("adding a covered file must revoke the trust")
	}
}

func TestStoreFileModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	home, project := isolate(t)
	ins, _ := Inspect(project)
	if err := Trust(ins); err != nil {
		t.Fatal(err)
	}
	path, _ := StorePath()
	if path != filepath.Join(home, ".chatcli", StoreFileName) {
		t.Fatalf("store path = %s", path)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %v, %v", fi.Mode().Perm(), err)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("store dir mode = %v, %v", di.Mode().Perm(), err)
	}
}

func TestCorruptStoreFailsClosed(t *testing.T) {
	home, project := isolate(t)
	write(t, filepath.Join(home, ".chatcli", StoreFileName), "{not json")
	if Allowed(project) {
		t.Fatal("an unreadable store must trust nothing")
	}
	if _, err := List(); err == nil {
		t.Fatal("a corrupt store must be reported")
	}
}

func TestAskDefaultsToNo(t *testing.T) {
	_, project := isolate(t)
	write(t, filepath.Join(project, ".env"), "A=1\n")
	for _, answer := range []string{"", "\n", "n\n", "whatever\n"} {
		ResetSessionForTest()
		ins, _ := Inspect(project)
		var out bytes.Buffer
		if Ask(ins, strings.NewReader(answer), &out) {
			t.Fatalf("answer %q must not trust", answer)
		}
		if Allowed(project) {
			t.Fatalf("answer %q: the project must stay untrusted", answer)
		}
		if !strings.Contains(out.String(), ".env") {
			t.Fatalf("the prompt must list the files found: %s", out.String())
		}
	}

	ins, _ := Inspect(project)
	rest := strings.NewReader("y\nnext line")
	if !Ask(ins, rest, &bytes.Buffer{}) || !Allowed(project) {
		t.Fatal("yes must trust the workspace")
	}
	if rest.Len() != len("next line") {
		t.Fatal("the answer must not consume input typed after it")
	}
	ResetSessionForTest()
	if !Allowed(project) {
		t.Fatal("yes must persist the trust")
	}
}

func TestWarnOncePerDirectory(t *testing.T) {
	_, project := isolate(t)
	write(t, filepath.Join(project, "coder_policy.json"), "{}")
	ins, _ := Inspect(project)
	var out bytes.Buffer
	Warn(&out, ins)
	Warn(&out, ins)
	if strings.Count(out.String(), "coder_policy.json") != 1 || !strings.Contains(out.String(), "chatcli trust") {
		t.Fatalf("warning: %q", out.String())
	}
}

func TestRunCLI(t *testing.T) {
	_, project := isolate(t)
	write(t, filepath.Join(project, ".env"), "A=1\n")
	var out bytes.Buffer
	for _, step := range []struct {
		args    []string
		code    int
		trusted bool
	}{
		{[]string{"--list"}, 0, false},
		{[]string{project}, 0, true},
		{[]string{"--list"}, 0, true},
		{[]string{"--revoke", project}, 0, false},
		{[]string{"--revoke", project}, 0, false},
		{[]string{"a", "b"}, 2, false},
		{[]string{"--bogus"}, 2, false},
		{[]string{"help"}, 0, false},
		{[]string{"-h"}, 0, false},
		{[]string{filepath.Join(project, "missing")}, 1, false},
	} {
		if code := RunCLI(step.args, &out, &out); code != step.code || Allowed(project) != step.trusted {
			t.Fatalf("%v: exit %d, trusted %v\n%s", step.args, code, Allowed(project), out.String())
		}
	}
	if !strings.Contains(out.String(), "[current]") {
		t.Fatalf("--list must show the trusted folder: %s", out.String())
	}
}
