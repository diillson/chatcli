/*
 * ChatCLI - Tests for /config security reveal
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/pkg/atrest"
	"go.uber.org/zap"
)

// revealFixture seals a whole-file store, a journal and the prompt history
// under a temporary state root, and leaves one plaintext store file and one
// foreign file beside them.
func revealFixture(t *testing.T) (*ChatCLI, string) {
	t.Helper()
	root := t.TempDir()
	park.SetBaseDir(filepath.Join(root, "parked"))
	t.Cleanup(func() { park.SetBaseDir("") })
	histPath := filepath.Join(root, "history")
	t.Setenv("HISTORY_FILE", histPath)
	t.Setenv(atrest.EnvKey, "reveal-key-reveal-key-reveal-key-00")
	prev := revealTerminal
	revealTerminal = func() bool { return true }
	t.Cleanup(func() { revealTerminal = prev })

	writeOld(t, filepath.Join(root, "memory", "graph.json"), `{"nodes":["SECRET-NODE"]}`)
	writeOld(t, filepath.Join(root, "transcripts", "s1.jsonl"), "{\"kind\":\"msg\",\"text\":\"SECRET-LINE\"}\n")
	writeOld(t, histPath, "SECRET-PROMPT\n")
	writeOld(t, filepath.Join(root, "sessions", "foreign.txt"), "not a store file")
	cli := &ChatCLI{logger: zap.NewNop(), stateRoot: root, historyManager: NewHistoryManager(zap.NewNop())}
	if rep := cli.resealAtRestStores(0); len(rep.Errors) != 0 {
		t.Fatalf("seal: %+v", rep)
	}
	// A store file written in plaintext after the walk: not listed.
	if err := os.WriteFile(filepath.Join(root, "memory", "plain.json"), []byte(`{"p":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return cli, root
}

func TestReveal_ListsOnlySealedFilesRelativeToTheRoot(t *testing.T) {
	cli, _ := revealFixture(t)
	var rels []string
	for _, f := range cli.revealCandidates() {
		rels = append(rels, f.Rel)
	}
	got := strings.Join(rels, ",")
	if got != "history,memory/graph.json,transcripts/s1.jsonl" {
		t.Fatalf("candidates = %s", got)
	}
	out := captureStdout(t, func() { cli.configSecurityReveal(nil) })
	if !strings.Contains(out, "memory/graph.json") || strings.Contains(out, "plain.json") || strings.Contains(out, "foreign.txt") {
		t.Fatalf("list:\n%s", out)
	}
}

func TestReveal_PrintsPlaintextAndLeavesTheFileSealed(t *testing.T) {
	cli, root := revealFixture(t)
	graph := filepath.Join(root, "memory", "graph.json")
	before, _ := os.ReadFile(graph)
	for arg, want := range map[string]string{
		"memory/graph.json":    "SECRET-NODE",
		graph:                  "SECRET-NODE",
		"transcripts/s1.jsonl": "SECRET-LINE",
		"history":              "SECRET-PROMPT",
	} {
		out := captureStdout(t, func() { cli.configSecurityReveal([]string{arg}) })
		if !strings.Contains(out, want) {
			t.Fatalf("reveal %s:\n%s", arg, out)
		}
	}
	after, _ := os.ReadFile(graph)
	if string(before) != string(after) || !isSealedFile(t, graph) {
		t.Fatal("reveal changed the file on disk")
	}
	if !strings.HasPrefix(readFileString(t, filepath.Join(root, "transcripts", "s1.jsonl")), sealedLinePrefix) {
		t.Fatal("reveal unsealed the journal on disk")
	}
}

func TestReveal_RefusesWhatIsNotASealedStoreFile(t *testing.T) {
	cli, root := revealFixture(t)
	for _, arg := range []string{"memory/plain.json", "sessions/foreign.txt", "../../etc/passwd", filepath.Join(root, "..", "x")} {
		out := captureStdout(t, func() { cli.configSecurityReveal([]string{arg}) })
		if !strings.Contains(out, "not a sealed store file") && !strings.Contains(out, "não é um arquivo cifrado") {
			t.Fatalf("reveal %s:\n%s", arg, out)
		}
	}
}

func TestReveal_OutWritesAPlaintextCopySafely(t *testing.T) {
	cli, root := revealFixture(t)
	dest := filepath.Join(t.TempDir(), "graph.json")
	captureStdout(t, func() { cli.configSecurityReveal([]string{"memory/graph.json", "--out", dest}) })
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != `{"nodes":["SECRET-NODE"]}` {
		t.Fatalf("copy = %q, %v", data, err)
	}
	if info, _ := os.Stat(dest); info.Mode().Perm() != 0o600 {
		t.Fatalf("copy permissions = %v", info.Mode().Perm())
	}
	// No silent overwrite.
	if err := os.WriteFile(dest, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() { cli.configSecurityReveal([]string{"memory/graph.json", "--out", dest}) })
	if readFileString(t, dest) != "mine" {
		t.Fatal("--out overwrote an existing file without --force")
	}
	captureStdout(t, func() { cli.configSecurityReveal([]string{"memory/graph.json", "--out=" + dest, "--force"}) })
	if readFileString(t, dest) == "mine" {
		t.Fatal("--force did not overwrite")
	}
	// Never inside the state root.
	inside := filepath.Join(root, "memory", "copy.json")
	captureStdout(t, func() { cli.configSecurityReveal([]string{"memory/graph.json", "--out", inside}) })
	if _, err := os.Stat(inside); err == nil {
		t.Fatal("a plaintext copy was written inside the state root")
	}
	// Not through a symlinked directory either.
	link := filepath.Join(t.TempDir(), "into-root")
	if err := os.Symlink(filepath.Join(root, "memory"), link); err != nil {
		t.Skip("symlinks unavailable")
	}
	captureStdout(t, func() {
		cli.configSecurityReveal([]string{"memory/graph.json", "--out", filepath.Join(link, "copy2.json")})
	})
	if _, err := os.Stat(filepath.Join(root, "memory", "copy2.json")); err == nil {
		t.Fatal("a plaintext copy reached the state root through a symlink")
	}
}

func TestReveal_RefusesOutsideAnInteractiveTerminal(t *testing.T) {
	cli, _ := revealFixture(t)
	revealTerminal = func() bool { return false }
	out := captureStdout(t, func() { cli.configSecurityReveal([]string{"memory/graph.json"}) })
	if strings.Contains(out, "SECRET-NODE") {
		t.Fatal("plaintext printed outside a terminal")
	}
}

func TestReveal_CompleterOffersTheSealedFiles(t *testing.T) {
	cli, _ := revealFixture(t)
	revealCache.Lock()
	revealCache.files, revealCache.at = nil, revealCache.at.AddDate(-1, 0, 0)
	revealCache.Unlock()
	doc := func(s string) prompt.Document {
		b := prompt.NewBuffer()
		b.InsertText(s, false, true)
		return *b.Document()
	}
	var names []string
	for _, s := range cli.getConfigSecuritySuggestions(doc("/config security reveal ")) {
		names = append(names, s.Text)
	}
	if strings.Join(names, ",") != "history,memory/graph.json,transcripts/s1.jsonl" {
		t.Fatalf("suggestions = %v", names)
	}
	var flags []string
	for _, s := range cli.getConfigSecuritySuggestions(doc("/config security reveal memory/graph.json --")) {
		flags = append(flags, s.Text)
	}
	if strings.Join(flags, ",") != "--out,--force" {
		t.Fatalf("flags = %v", flags)
	}
	if subs := cli.getConfigSecuritySuggestions(doc("/config security rev")); len(subs) != 1 || subs[0].Text != "reveal" {
		t.Fatalf("subcommand = %v", subs)
	}
}
