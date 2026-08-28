/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * builtin_forge_test.go
 *
 * @forge surface: lenient parsing (envelope + flat argv + two-token
 * spellings), gh/glab argv mapping, forge auto-detection from the remote,
 * and the read-only split that keeps mutations behind the security gate.
 */
package plugins

import (
	"context"
	"strings"
	"testing"
)

func withFakeForge(t *testing.T, remote string) *struct {
	bin  string
	args []string
} {
	t.Helper()
	rec := &struct {
		bin  string
		args []string
	}{}
	prevRun, prevRemote := forgeRunner, forgeRemoteURL
	forgeRunner = func(_ context.Context, bin string, args []string) (string, error) {
		rec.bin, rec.args = bin, args
		return "FAKE-OUTPUT", nil
	}
	forgeRemoteURL = func(context.Context) string { return remote }
	t.Cleanup(func() { forgeRunner, forgeRemoteURL = prevRun, prevRemote })
	return rec
}

func TestForge_EnvelopeToGHArgs(t *testing.T) {
	rec := withFakeForge(t, "git@github.com:user/repo.git")
	p := NewBuiltinForgePlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"pr-checks","args":{"number":42}}`})
	if err != nil {
		t.Fatal(err)
	}
	if out != "FAKE-OUTPUT" {
		t.Fatalf("unexpected output %q", out)
	}
	if rec.bin != "gh" || strings.Join(rec.args, " ") != "pr checks 42" {
		t.Fatalf("wrong CLI call: %s %v", rec.bin, rec.args)
	}
}

func TestForge_TwoTokenSpellingAndFlags(t *testing.T) {
	rec := withFakeForge(t, "git@github.com:user/repo.git")
	p := NewBuiltinForgePlugin()

	if _, err := p.Execute(context.Background(), []string{"pr", "view", "7"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(rec.args, " ") != "pr view 7 --comments" {
		t.Fatalf("two-token spelling broken: %v", rec.args)
	}

	if _, err := p.Execute(context.Background(), []string{"ci-logs", "999"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(rec.args, " ") != "run view 999 --log-failed" {
		t.Fatalf("ci-logs mapping broken: %v", rec.args)
	}
}

func TestForge_GitLabRemoteRoutesToGlab(t *testing.T) {
	rec := withFakeForge(t, "https://gitlab.com/user/repo.git")
	p := NewBuiltinForgePlugin()

	if _, err := p.Execute(context.Background(), []string{"pr-view", "5"}); err != nil {
		t.Fatal(err)
	}
	if rec.bin != "glab" || strings.Join(rec.args, " ") != "mr view 5" {
		t.Fatalf("gitlab routing broken: %s %v", rec.bin, rec.args)
	}

	out, err := p.Execute(context.Background(), []string{"detect"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "glab") {
		t.Fatalf("detect should report glab: %s", out)
	}
}

func TestForge_PRCreateValidationAndArgs(t *testing.T) {
	rec := withFakeForge(t, "git@github.com:u/r.git")
	p := NewBuiltinForgePlugin()

	if _, err := p.Execute(context.Background(), []string{"pr-create"}); err == nil {
		t.Fatal("pr-create without title must error")
	}
	if _, err := p.Execute(context.Background(),
		[]string{`{"cmd":"pr-create","args":{"title":"fix: x","body":"b","base":"main","draft":true}}`}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rec.args, " ")
	for _, want := range []string{"pr create", "--title fix: x", "--base main", "--draft"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("pr-create args missing %q: %v", want, rec.args)
		}
	}
}

func TestForge_ReadOnlySplit(t *testing.T) {
	p := NewBuiltinForgePlugin()
	for _, args := range [][]string{
		{"pr-list"}, {"pr-view", "1"}, {"pr-diff", "1"}, {"pr-checks", "1"},
		{"issue-list"}, {"issue-view", "1"}, {"ci-status"}, {"ci-logs", "9"}, {"detect"},
	} {
		if !p.IsReadOnly(args) {
			t.Fatalf("%v must be read-only", args)
		}
	}
	for _, args := range [][]string{
		{"pr-create", "--title", "t"}, {"pr-comment", "1", "--body", "b"},
		{`{"cmd":"issue-comment","args":{"number":1,"body":"b"}}`},
	} {
		if p.IsReadOnly(args) {
			t.Fatalf("%v must NOT be read-only (security gate)", args)
		}
	}
	if p.IsReadOnly(nil) {
		t.Fatal("empty args must fail closed")
	}
}

func TestForge_UnknownCmd(t *testing.T) {
	withFakeForge(t, "")
	p := NewBuiltinForgePlugin()
	if _, err := p.Execute(context.Background(), []string{"yolo"}); err == nil {
		t.Fatal("unknown cmd must error")
	}
}
