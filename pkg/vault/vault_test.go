/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package vault

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func note(path, body string) Note {
	return Note{Path: path, Front: []Field{{Key: "chatcli_id", Value: "x:" + path}}, Body: body}
}

func read(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func TestSync_WritesIncrementallyAndRemovesStale(t *testing.T) {
	dir := t.TempDir()
	rep, err := Sync(dir, []Note{note("A.md", "a"), note("Sub/B.md", "b")}, Options{})
	if err != nil || rep.Written != 2 {
		t.Fatalf("first sync: %+v %v", rep, err)
	}
	rep, err = Sync(dir, []Note{note("A.md", "a"), note("Sub/B.md", "b")}, Options{})
	if err != nil || rep.Written != 0 || rep.Unchanged != 2 {
		t.Fatalf("unchanged sync rewrote: %+v %v", rep, err)
	}
	rep, err = Sync(dir, []Note{note("A.md", "a2")}, Options{})
	if err != nil || rep.Written != 1 || rep.Removed != 1 {
		t.Fatalf("update/remove: %+v %v", rep, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Sub")); !os.IsNotExist(err) {
		t.Fatal("empty directory of a removed note left behind")
	}
	if !strings.Contains(read(t, dir, "A.md"), "a2") {
		t.Fatal("note not updated")
	}
}

func TestSync_KeysSkipRendering(t *testing.T) {
	dir := t.TempDir()
	n := note("A.md", "v1")
	n.Key = "rev1"
	if _, err := Sync(dir, []Note{n}, Options{}); err != nil {
		t.Fatal(err)
	}
	n.Body = "v2-but-same-key"
	rep, _ := Sync(dir, []Note{n}, Options{})
	if rep.Unchanged != 1 || strings.Contains(read(t, dir, "A.md"), "v2") {
		t.Fatalf("same key must skip the rewrite: %+v", rep)
	}
	n.Key = "rev2"
	if rep, _ := Sync(dir, []Note{n}, Options{}); rep.Written != 1 {
		t.Fatalf("new key must rewrite: %+v", rep)
	}
}

func TestSync_PreservesHandEditsUnlessForced(t *testing.T) {
	dir := t.TempDir()
	if _, err := Sync(dir, []Note{note("A.md", "generated"), note("B.md", "gone soon")}, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "A.md"), []byte("my own notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "B.md"), []byte("edited before removal"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := Sync(dir, []Note{note("A.md", "generated v2")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if read(t, dir, "A.md") != "my own notes" || read(t, dir, "B.md") != "edited before removal" {
		t.Fatal("hand edits overwritten or removed")
	}
	if len(rep.Kept) != 2 {
		t.Fatalf("kept = %v", rep.Kept)
	}
	// Still kept on the next run (ownership survives), until forced.
	if rep, _ := Sync(dir, []Note{note("A.md", "generated v2")}, Options{}); len(rep.Kept) != 2 {
		t.Fatalf("second run forgot the kept notes: %+v", rep)
	}
	rep, err = Sync(dir, []Note{note("A.md", "generated v2")}, Options{Force: true})
	if err != nil || !strings.Contains(read(t, dir, "A.md"), "generated v2") || rep.Removed != 1 {
		t.Fatalf("force: %+v %v", rep, err)
	}
}

func TestSync_NeverTouchesForeignFiles(t *testing.T) {
	for _, force := range []bool{false, true} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "Mine.md"), []byte("personal"), 0o600); err != nil {
			t.Fatal(err)
		}
		rep, err := Sync(dir, []Note{note("Mine.md", "generated")}, Options{Force: force})
		if err != nil {
			t.Fatal(err)
		}
		if read(t, dir, "Mine.md") != "personal" || len(rep.Kept) != 1 {
			t.Fatalf("force=%v: foreign file overwritten (%+v)", force, rep)
		}
		if _, err := Sync(dir, nil, Options{Force: force}); err != nil {
			t.Fatal(err)
		}
		if read(t, dir, "Mine.md") != "personal" {
			t.Fatalf("force=%v: foreign file removed", force)
		}
	}
}

func TestSync_AdoptsIdenticalUntrackedNotes(t *testing.T) {
	dir := t.TempDir()
	// A previous run wrote the note but died before its manifest landed.
	if err := os.WriteFile(filepath.Join(dir, "A.md"), Render(note("A.md", "a")), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := Sync(dir, []Note{note("A.md", "a")}, Options{})
	if err != nil || rep.Unchanged != 1 || len(rep.Kept) != 0 {
		t.Fatalf("identical untracked note not adopted: %+v %v", rep, err)
	}
	if rep, _ := Sync(dir, []Note{note("A.md", "a2")}, Options{}); rep.Written != 1 {
		t.Fatalf("adopted note not updatable: %+v", rep)
	}
}

func TestSync_CanceledKeepsOwnership(t *testing.T) {
	dir := t.TempDir()
	notes := []Note{note("A.md", "a"), note("B.md", "b"), note("C.md", "c")}
	if _, err := Sync(dir, notes, Options{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	notes[0].Body = "a2"
	rep, err := Sync(dir, notes, Options{Context: ctx})
	if err == nil || rep.Removed != 0 {
		t.Fatalf("canceled sync: %+v %v", rep, err)
	}
	if rep, _ := Sync(dir, nil, Options{}); rep.Removed != 3 {
		t.Fatalf("ownership lost by the canceled run: %+v", rep)
	}
}

func TestSync_TornNoteIsRewritten(t *testing.T) {
	dir := t.TempDir()
	if _, err := Sync(dir, []Note{note("A.md", "content")}, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "A.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rep, _ := Sync(dir, []Note{note("A.md", "content")}, Options{})
	if rep.Written != 1 || len(rep.Kept) != 0 {
		t.Fatalf("torn note treated as an edit: %+v", rep)
	}
}

func TestSync_RejectsUnsafePathsAndDedupesCase(t *testing.T) {
	dir := t.TempDir()
	rep, err := Sync(dir, []Note{
		note("../escape.md", "x"), note("/abs.md", "x"), note("a/../../b.md", "x"),
		note(".hidden/x.md", "x"), note("notmd.txt", "x"), note(`C:\win.md`, "x"),
		note("Auth.md", "1"), note("auth.md", "2"),
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Skipped) != 6 {
		t.Fatalf("skipped = %v", rep.Skipped)
	}
	if rep.Written != 2 {
		t.Fatalf("case-colliding notes must both land: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.md")); err == nil {
		t.Fatal("path traversal escaped the vault")
	}
}

func TestSync_PrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	dir := filepath.Join(t.TempDir(), "vault")
	if _, err := Sync(dir, []Note{note("Sub/A.md", "a")}, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"Sub/A.md", ManifestDir + "/manifest.json"} {
		fi, err := os.Stat(filepath.Join(dir, p))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s perm = %v (%v)", p, fi.Mode().Perm(), err)
		}
	}
	fi, _ := os.Stat(filepath.Join(dir, "Sub"))
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm = %v", fi.Mode().Perm())
	}
}

func TestSync_CorruptManifestKeepsEverything(t *testing.T) {
	dir := t.TempDir()
	if _, err := Sync(dir, []Note{note("A.md", "a")}, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestDir, "manifest.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, _ := Sync(dir, nil, Options{})
	if rep.Removed != 0 || read(t, dir, "A.md") == "" {
		t.Fatal("corrupt manifest must never cause deletions")
	}
}

func TestRender_FrontMatterIsValidYAMLScalars(t *testing.T) {
	out := string(Render(Note{Front: []Field{
		{Key: "title", Value: "a: b # c"},
		{Key: "plain", Value: "simple text"},
		{Key: "bool", Value: "true"},
		{Key: "num", Value: "0.8"},
		{Key: "empty", Value: ""},
		{Key: "aliases", IsList: true, List: []string{"x", "- dash"}},
		{Key: "none", IsList: true},
		{Key: "bad key!", Value: "v"},
	}, Body: "# T\n\nbody"}))
	for _, want := range []string{
		"title: \"a: b # c\"\n", "plain: simple text\n", "bool: \"true\"\n", "num: \"0.8\"\n",
		"empty: \"\"\n", "aliases:\n  - x\n  - \"- dash\"\n", "badkey: v\n", "---\n\n# T\n\nbody\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "none:") {
		t.Error("empty list rendered")
	}
}

func TestRender_TruncatesHugeNotesOnRuneBoundary(t *testing.T) {
	body := strings.Repeat("é", MaxNoteBytes)
	out := Render(Note{Body: body})
	if len(out) > MaxNoteBytes || !strings.Contains(string(out), "truncated by chatcli") {
		t.Fatalf("not bounded: %d bytes", len(out))
	}
	if !strings.HasPrefix(string(out), "é") || strings.ContainsRune(string(out), '\uFFFD') {
		t.Fatal("cut inside a rune")
	}
}

func TestFileNameTagLink(t *testing.T) {
	cases := map[string]string{
		"a/b:c*d?":            "a b c d",
		"  ":                  "untitled",
		"CON":                 "CON_",
		"..hidden..":          "hidden",
		"[[x]] #tag | pipe ^": "x tag pipe",
	}
	for in, want := range cases {
		if got := FileName(in); got != want {
			t.Errorf("FileName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := FileName(strings.Repeat("ã", 200)); len(got) > 96 || strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("FileName not bounded on rune boundary: %d", len(got))
	}
	tags := map[string]string{"Kubernetes Operator": "kubernetes-operator", "2026": "n2026", "c++": "c", "!!": "", "a/b": "a/b"}
	for in, want := range tags {
		if got := Tag(in); got != want {
			t.Errorf("Tag(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Link("Projects/Orbital.md", "Orbital"); got != "[[Projects/Orbital]]" {
		t.Errorf("Link = %q", got)
	}
	if got := Link("Facts/x (1a2b).md", "a [b] | c"); got != "[[Facts/x (1a2b)|a (b) / c]]" {
		t.Errorf("Link label escaping = %q", got)
	}
}

func TestSync_CaseOnlyRenameKeepsTheNote(t *testing.T) {
	dir := t.TempDir()
	if _, err := Sync(dir, []Note{note("Topics/auth.md", "v1")}, Options{}); err != nil {
		t.Fatal(err)
	}
	rep, err := Sync(dir, []Note{note("Topics/Auth.md", "v2")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "Topics", "Auth.md"))
	if err != nil || !strings.Contains(string(b), "v2") {
		t.Fatalf("renamed note lost (%+v): %v", rep, err)
	}
	if len(rep.Kept) != 0 {
		t.Fatalf("a case-only rename is not a hand edit: %+v", rep)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "Topics"))
	if len(entries) != 1 {
		t.Fatalf("old-case file left behind or both removed: %v", entries)
	}
}

func TestSync_ConcurrentSyncsDoNotFreezeNotes(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = Sync(dir, []Note{note("A.md", "v"), note("B.md", "w")}, Options{})
		}(i)
	}
	wg.Wait()
	rep, err := Sync(dir, []Note{note("A.md", "v2"), note("B.md", "w2")}, Options{})
	if err != nil || rep.Written != 2 || len(rep.Kept) != 0 {
		t.Fatalf("notes frozen by concurrent syncs: %+v %v", rep, err)
	}
}
