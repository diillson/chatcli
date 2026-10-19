/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package evals

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/i18n"
)

func writeFixtureFile(t *testing.T, dir, name, body string, perm os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, perm); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFileTagReadsContentAndFollowsAnchors(t *testing.T) {
	dir := t.TempDir()
	writeFixtureFile(t, dir, "testdata/go.mod", "module example.com/m\n\ngo 1.22\n", 0o600)
	writeFixtureFile(t, dir, "testdata/run.sh", "#!/bin/sh\necho ok\n", 0o700)
	p := writeSuite(t, dir, "s.yaml", `
cases:
  - id: first
    prompt: x
    files: &module
      go.mod: !file testdata/go.mod
      run.sh: !file "testdata/run.sh"
      notes.md: inline text
    checks: [{contains: a}]
  - id: second
    prompt: x
    files: *module
    checks: [{contains: a}]
`)
	suites, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range suites[0].Cases {
		if got := c.Files["go.mod"]; got != "module example.com/m\n\ngo 1.22\n" {
			t.Errorf("%s: go.mod = %q, want the file's content", c.ID, got)
		}
		if got := c.Files["notes.md"]; got != "inline text" {
			t.Errorf("%s: an inline entry must stay as written, got %q", c.ID, got)
		}
		if _, ok := c.filePerms["notes.md"]; ok {
			t.Errorf("%s: an inline entry must not carry a source mode", c.ID)
		}
		if runtime.GOOS != "windows" && c.filePerms["run.sh"]&0o100 == 0 {
			t.Errorf("%s: run.sh lost its exec bit: %v", c.ID, c.filePerms["run.sh"])
		}
	}
}

func TestLoadFileTagAbsolutePath(t *testing.T) {
	src := filepath.Join(t.TempDir(), "seed.txt")
	writeFixtureFile(t, filepath.Dir(src), "seed.txt", "seed", 0o600)
	p := writeSuite(t, t.TempDir(), "s.yaml",
		"cases:\n  - id: a\n    prompt: x\n    files:\n      seed.txt: !file '"+filepath.ToSlash(src)+"'\n    checks: [{contains: a}]\n")
	suites, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := suites[0].Cases[0].Files["seed.txt"]; got != "seed" {
		t.Fatalf("seed.txt = %q", got)
	}
}

func TestLoadFileTagRejections(t *testing.T) {
	dir := t.TempDir()
	writeFixtureFile(t, dir, "testdata/big.txt", strings.Repeat("x", 64), 0o600)
	if err := os.MkdirAll(filepath.Join(dir, "testdata", "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	saved := maxFileTagBytes
	maxFileTagBytes = 32
	t.Cleanup(func() { maxFileTagBytes = saved })

	entry := func(value string) string {
		return "cases:\n  - id: a\n    prompt: x\n    files:\n      f.txt: " + value + "\n    checks: [{contains: a}]\n"
	}
	cases := map[string]struct {
		body string
		want string
	}{
		"empty path":    {entry("!file ''"), i18n.T("evals.load.file_tag_empty", "f.txt")},
		"missing":       {entry("!file testdata/nope.txt"), i18n.T("evals.load.file_tag_missing", "f.txt", "testdata/nope.txt")},
		"directory":     {entry("!file testdata/sub"), i18n.T("evals.load.file_tag_dir", "f.txt", "testdata/sub")},
		"too big":       {entry("!file testdata/big.txt"), i18n.T("evals.load.file_tag_too_big", "f.txt", "testdata/big.txt", int64(64), int64(32))},
		"misspelled":    {entry("!fiel testdata/big.txt"), i18n.T("evals.load.tag_unknown", 5, "!fiel")},
		"outside files": {"cases:\n  - id: a\n    prompt: !file prompt.md\n    checks: [{contains: a}]\n", i18n.T("evals.load.tag_misplaced", 3)},
		"on a mapping":  {entry("!file {from: x}"), "!file"},
		"unknown key":   {"cases:\n  - id: a\n    prompt: x\n    files: {f.txt: !file testdata/big.txt}\n    chekcs: []\n", "chekcs"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := writeSuite(t, dir, "s.yaml", tc.body)
			_, err := Load(p)
			if err == nil {
				t.Fatalf("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
	// A case-level failure names the case in the active language.
	p := writeSuite(t, dir, "s.yaml", entry("!file ''"))
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), i18n.T("evals.load.case_prefix", "a")+": ") {
		t.Fatalf("error %v does not carry the translated case prefix", err)
	}
}

func TestLoadFixtureMessages(t *testing.T) {
	dir := t.TempDir()
	writeFixtureFile(t, dir, "testdata/go.mod", "module m\n", 0o600)
	fixture := func(path string) string {
		return "cases:\n  - {id: a, fixture: " + path + ", prompt: x, checks: [{contains: a}]}\n"
	}
	cases := map[string]struct{ path, want string }{
		"a file":  {"testdata/go.mod", i18n.T("evals.load.fixture_not_dir", "testdata/go.mod")},
		"missing": {"testdata/nope", i18n.T("evals.load.fixture", "testdata/nope")},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeSuite(t, dir, "s.yaml", fixture(tc.path)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v does not contain %q", err, tc.want)
			}
		})
	}
}

func TestLoadTranslatedPrefixes(t *testing.T) {
	bad := writeSuite(t, t.TempDir(), "s.yaml", "cases:\n  - id: a\n    prompt: x\n    checks:\n      - regex: '('\n")
	_, err := Load(bad)
	if err == nil || !strings.Contains(err.Error(), i18n.T("evals.load.check_prefix", 1)+": ") {
		t.Fatalf("error %v does not carry the translated check prefix", err)
	}
	dur := writeSuite(t, t.TempDir(), "s.yaml", "cases:\n  - {id: a, timeout: soon, prompt: x, checks: [{contains: a}]}\n")
	_, err = Load(dur)
	if err == nil || !strings.Contains(err.Error(), i18n.T("evals.load.duration", "soon")) {
		t.Fatalf("error %v does not carry the translated duration message", err)
	}
}

func TestReadRecordTranslatesBadJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rec.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadRecord(p)
	if err == nil || !strings.Contains(err.Error(), i18n.T("evals.record.parse", p)) {
		t.Fatalf("error %v does not carry the translated record message", err)
	}
}

func TestRunSandboxKeepsFileTagModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	fx := t.TempDir()
	writeFixtureFile(t, fx, "run.sh", "old", 0o600)
	writeFixtureFile(t, fx, "tool.sh", "fixture tool", 0o700)
	c := &Case{
		ID: "coder", Mode: ModeCoder, Prompt: "coder: go", Trials: 1, PassPolicy: PassAll,
		Timeout: Duration(time.Minute), FixtureDir: fx,
		Files: map[string]string{
			"run.sh":  "#!/bin/sh\necho ok\n", // read with !file from an executable source
			"tool.sh": "inline over the fixture",
		},
		filePerms: map[string]os.FileMode{"run.sh": 0o700},
		Checks:    []Check{{FileContains: &FileCheck{Path: "run.sh", Text: "echo ok"}}},
	}
	ex := newFakeExec(map[string][]fakeRun{"coder": {{final: "done"}}})
	rep := Run(context.Background(), []*Suite{{Name: "s", Cases: []*Case{c}}}, Options{Executor: ex, Keep: true})
	kept := rep.Cases[0].Trials[0].Sandbox
	if kept == "" {
		t.Fatalf("no sandbox kept: %+v", rep.Cases[0].Trials)
	}
	defer func() { _ = os.RemoveAll(kept) }()
	for name, wantExec := range map[string]bool{"run.sh": true, "tool.sh": true} {
		st, err := os.Stat(filepath.Join(kept, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm()&0o100 != 0; got != wantExec {
			t.Errorf("%s exec bit = %v, want %v (mode %v)", name, got, wantExec, st.Mode().Perm())
		}
	}
	if b, _ := os.ReadFile(filepath.Join(kept, "tool.sh")); string(b) != "inline over the fixture" {
		t.Errorf("inline entry not written over the fixture: %q", b)
	}
}
