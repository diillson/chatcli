/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package engine

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installFakeRipgrep puts an "rg" on PATH that records its argv, one
// argument per line, so the test sees exactly what the engine passed.
func installFakeRipgrep(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake rg needs a POSIX shell")
	}
	bin := t.TempDir()
	argvFile := filepath.Join(bin, "argv")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + argvFile + "'; done\n"
	if err := os.WriteFile(filepath.Join(bin, "rg"), []byte(script), 0o700); err != nil { //#nosec G306 -- test helper must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvFile
}

func TestSearch_TermStartingWithDashIsNotAnOption(t *testing.T) {
	argvFile := installFakeRipgrep(t)
	ws := t.TempDir()
	var out bytes.Buffer
	e := NewEngine(&out, &out, ws)

	term := "--pre=/tmp/evil.sh"
	if err := e.Execute(context.Background(), "search", []string{"--term", term, "--dir", ws}); err != nil {
		t.Fatalf("search: %v", err)
	}
	raw, err := os.ReadFile(argvFile) //#nosec G304 -- test temp file
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(argv) < 3 {
		t.Fatalf("argv too short: %q", argv)
	}
	tail := argv[len(argv)-3:]
	if tail[0] != "--" || tail[1] != term || tail[2] != ws {
		t.Fatalf("rg argv must end with -- <term> <dir>, got %q", argv)
	}
	for _, a := range argv[:len(argv)-3] {
		if strings.HasPrefix(a, "--pre") {
			t.Fatalf("term leaked into the option list: %q", argv)
		}
	}
}

func TestReadOnlySubcommands_RefuseDirOutsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "credentials"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"search":      {"--term", "needle", "--dir", outside},
		"tree":        {"--dir", outside},
		"map":         {"--dir", outside},
		"outline":     {"--file", filepath.Join(outside, "credentials")},
		"git-status":  {"--dir", outside},
		"git-diff":    {"--dir", outside},
		"git-log":     {"--dir", outside},
		"git-changed": {"--dir", outside},
		"git-branch":  {"--dir", outside},
	}
	for cmd, args := range cases {
		t.Run(cmd, func(t *testing.T) {
			var out bytes.Buffer
			e := NewEngine(&out, &out, ws)
			err := e.Execute(context.Background(), cmd, args)
			if err == nil || !strings.Contains(err.Error(), "outside workspace boundary") {
				t.Fatalf("%s outside the workspace: err=%v out=%q", cmd, err, out.String())
			}
			if strings.Contains(out.String(), "needle") || strings.Contains(out.String(), "credentials") {
				t.Fatalf("%s leaked outside content: %q", cmd, out.String())
			}
		})
	}
}

func TestFallbackSearch_DoesNotFollowSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	ws := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "id_test")
	if err := os.WriteFile(secret, []byte("PRIVATE-needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(ws, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "plain.txt"), []byte("plain needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	e := NewEngine(&out, &out, ws)
	if err := e.fallbackSearch("needle", ws, false, false, 0, 0, "", 0); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "PRIVATE") {
		t.Fatalf("fallback search followed a symlink out of the workspace: %q", out.String())
	}
	if !strings.Contains(out.String(), "plain needle") {
		t.Fatalf("fallback search lost a regular match: %q", out.String())
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// writeMarkerScript writes a program that creates marker when run.
func writeMarkerScript(t *testing.T, dir, marker string) string {
	t.Helper()
	p := filepath.Join(dir, "payload.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700); err != nil { //#nosec G306 -- test payload must be executable
		t.Fatal(err)
	}
	return p
}

func TestGitStatus_RepoFsmonitorDoesNotRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX payload script")
	}
	dir := newGitRepo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	payload := writeMarkerScript(t, t.TempDir(), marker)
	gitIn(t, dir, "config", "core.fsmonitor", payload)

	for _, cmd := range []string{"git-status", "git-changed", "git-diff"} {
		var out bytes.Buffer
		e := NewEngine(&out, &out, dir)
		if err := e.Execute(context.Background(), cmd, []string{"--dir", dir}); err != nil {
			t.Fatalf("%s: %v\n%s", cmd, err, out.String())
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("%s ran the repository's core.fsmonitor program", cmd)
		}
	}
}

func TestGitDiff_ExternalDiffDoesNotRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX payload script")
	}
	dir := newGitRepo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	payload := writeMarkerScript(t, t.TempDir(), marker)
	gitIn(t, dir, "config", "diff.external", payload)
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	e := NewEngine(&out, &out, dir)
	if err := e.Execute(context.Background(), "git-diff", []string{"--dir", dir}); err != nil {
		t.Fatalf("git-diff: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("git-diff ran the repository's diff.external program")
	}
	if !strings.Contains(out.String(), "+changed") {
		t.Fatalf("git-diff lost the internal diff: %q", out.String())
	}
}

// An embedded bare repository whose config points its work tree back at
// the workspace is the shape that turns a shipped directory into code
// execution on the first git status.
func TestGitStatus_EmbeddedBareRepoRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX payload script")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ws := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	payload := writeMarkerScript(t, t.TempDir(), marker)
	evil := filepath.Join(ws, "evil")
	gitIn(t, ws, "init", "--bare", "evil")
	gitIn(t, ws, "--git-dir="+evil, "config", "core.bare", "false")
	gitIn(t, ws, "--git-dir="+evil, "config", "core.worktree", ws)
	gitIn(t, ws, "--git-dir="+evil, "config", "core.fsmonitor", payload)

	var out bytes.Buffer
	e := NewEngine(&out, &out, ws)
	err := e.Execute(context.Background(), "git-status", []string{"--dir", evil})
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("git-status ran a program from an embedded repository's config")
	}
	if err == nil {
		t.Fatalf("git-status in an embedded bare repository must fail, got %q", out.String())
	}
}

// git status is auto-allowed through exec; run inside an embedded bare
// repository it must not load that repository's config.
func TestExec_GitInEmbeddedBareRepoDoesNotRunFsmonitor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX payload script")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv(SandboxEnv, "off")
	ws := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	payload := writeMarkerScript(t, t.TempDir(), marker)
	evil := filepath.Join(ws, "evil")
	gitIn(t, ws, "init", "--bare", "evil")
	gitIn(t, ws, "--git-dir="+evil, "config", "core.bare", "false")
	gitIn(t, ws, "--git-dir="+evil, "config", "core.worktree", ws)
	gitIn(t, ws, "--git-dir="+evil, "config", "core.fsmonitor", payload)

	var out bytes.Buffer
	e := NewEngine(&out, &out, ws)
	_ = e.Execute(context.Background(), "exec", []string{"--cmd", "git status", "--dir", evil})
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("exec git status ran an embedded repository's fsmonitor: %q", out.String())
	}
}

func TestWithExplicitBareRepos_AppendsAfterUserEntries(t *testing.T) {
	got := withExplicitBareRepos([]string{"HOME=/h", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=a.b", "GIT_CONFIG_VALUE_0=c"})
	want := []string{"GIT_CONFIG_KEY_2=safe.bareRepository", "GIT_CONFIG_VALUE_2=explicit", "GIT_CONFIG_COUNT=3"}
	tail := got[len(got)-3:]
	for i := range want {
		if tail[i] != want[i] {
			t.Fatalf("tail = %q, want %q", tail, want)
		}
	}
	if fresh := withExplicitBareRepos([]string{"HOME=/h"}); fresh[len(fresh)-1] != "GIT_CONFIG_COUNT=1" {
		t.Fatalf("fresh env = %q", fresh)
	}
}

func TestCheckGitLayout_RefusesForeignGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ws := t.TempDir()
	gitIn(t, ws, "init")
	gitIn(t, ws, "init", "--bare", "evil")
	evil := filepath.Join(ws, "evil")
	gitIn(t, ws, "--git-dir="+evil, "config", "core.bare", "false")
	gitIn(t, ws, "--git-dir="+evil, "config", "core.worktree", ws)

	// Bypass safe.bareRepository the way an old git would, by naming the
	// git directory explicitly: the layout check alone must refuse it.
	t.Setenv("GIT_DIR", evil)
	if err := checkGitLayout(context.Background(), ws); err == nil {
		t.Fatal("checkGitLayout accepted a git directory that is not <top>/.git")
	}
}
