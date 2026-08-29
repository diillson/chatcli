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

func TestForge_ArgMappingMatrix(t *testing.T) {
	inv := func(cmd, number, body, title, run, branch string) forgeInvocation {
		return forgeInvocation{cmd: cmd, number: number, body: body, title: title, run: run, branch: branch}
	}
	cases := []struct {
		bin  string
		inv  forgeInvocation
		want string
	}{
		{"gh", inv("pr-list", "", "", "", "", ""), "pr list --limit 20"},
		{"glab", inv("pr-list", "", "", "", "", ""), "pr list --limit 20"},
		{"glab", inv("pr-view", "9", "", "", "", ""), "mr view 9"},
		{"glab", inv("pr-diff", "9", "", "", "", ""), "mr diff 9"},
		{"gh", inv("pr-diff", "9", "", "", "", ""), "pr diff 9"},
		{"glab", inv("pr-checks", "9", "", "", "", ""), "ci status --live=false"},
		{"gh", inv("pr-comment", "9", "hi", "", "", ""), "pr comment 9 --body hi"},
		{"glab", inv("pr-comment", "9", "hi", "", "", ""), "mr note 9 --message hi"},
		{"glab", inv("pr-create", "", "d", "t", "", ""), "mr create --title t --description d"},
		{"gh", inv("issue-list", "", "", "", "", ""), "issue list --limit 20"},
		{"gh", inv("issue-view", "7", "", "", "", ""), "issue view 7 --comments"},
		{"glab", inv("issue-view", "7", "", "", "", ""), "issue view 7"},
		{"gh", inv("issue-comment", "7", "x", "", "", ""), "issue comment 7 --body x"},
		{"glab", inv("issue-comment", "7", "x", "", "", ""), "issue note 7 --message x"},
		{"gh", inv("ci-status", "", "", "", "", "dev"), "run list --limit 20 --branch dev"},
		{"glab", inv("ci-status", "", "", "", "", ""), "ci status --live=false"},
		{"gh", inv("ci-logs", "", "", "", "55", ""), "run view 55 --log-failed"},
		{"glab", inv("ci-logs", "", "", "", "55", ""), "ci trace 55"},
	}
	for _, tc := range cases {
		got, err := buildForgeArgs(tc.bin, tc.inv)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.bin, tc.inv.cmd, err)
		}
		if strings.Join(got, " ") != tc.want {
			t.Fatalf("%s %s: got %q want %q", tc.bin, tc.inv.cmd, strings.Join(got, " "), tc.want)
		}
	}
}

func TestForge_ValidationErrors(t *testing.T) {
	for _, inv := range []forgeInvocation{
		{cmd: "pr-view"}, {cmd: "pr-diff"}, {cmd: "pr-checks"},
		{cmd: "pr-comment", number: "1"}, {cmd: "issue-view"},
		{cmd: "issue-comment", number: "1"}, {cmd: "ci-logs"},
		{cmd: "pr-create"}, {cmd: "yolo"}, {cmd: "pr-weird"}, {cmd: "issue-weird"}, {cmd: "ci-weird"},
	} {
		if _, err := buildForgeArgs("gh", inv); err == nil {
			t.Fatalf("%q must error", inv.cmd)
		}
	}
}

func TestForge_FlatFlagParsing(t *testing.T) {
	inv, err := parseForgeInvocation([]string{"pr-create", "--title", "fix: y", "--body", "b", "--base", "main", "--draft", "--host", "gitlab"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.title != "fix: y" || inv.body != "b" || inv.base != "main" || !inv.draft || inv.host != "gitlab" {
		t.Fatalf("flat flags broken: %+v", inv)
	}
	inv, err = parseForgeInvocation([]string{"ci-status", "--branch=dev", "--limit=5"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.branch != "dev" || inv.limit != 5 {
		t.Fatalf("inline flags broken: %+v", inv)
	}
	if _, err := parseForgeInvocation(nil); err == nil {
		t.Fatal("empty args must error")
	}
	if _, err := parseForgeInvocation([]string{"{not json"}); err == nil {
		t.Fatal("bad envelope must error")
	}
}

func TestForge_RunnerErrorSurfacesOutput(t *testing.T) {
	prevRun, prevRemote := forgeRunner, forgeRemoteURL
	forgeRunner = func(context.Context, string, []string) (string, error) {
		return "", errContains("exit status 1: no pull requests found")
	}
	forgeRemoteURL = func(context.Context) string { return "" }
	t.Cleanup(func() { forgeRunner, forgeRemoteURL = prevRun, prevRemote })

	p := NewBuiltinForgePlugin()
	_, err := p.Execute(context.Background(), []string{"pr-list"})
	if err == nil || !strings.Contains(err.Error(), "no pull requests found") {
		t.Fatalf("runner error must surface, got %v", err)
	}
}

// errContains builds a plain error carrying the given text.
func errContains(msg string) error { return &forgeTestError{msg} }

type forgeTestError struct{ msg string }

func (e *forgeTestError) Error() string { return e.msg }

func TestForge_DescribeCallAndMeta(t *testing.T) {
	p := NewBuiltinForgePlugin()
	if p.Name() != "@forge" || !strings.Contains(p.Usage(), "pr-checks") || !strings.Contains(p.Schema(), "ci-logs") {
		t.Fatal("plugin identity/usage/schema incomplete")
	}
	for _, args := range [][]string{
		{"pr-list"}, {"pr-view", "1"}, {"pr-checks", "1"}, {"pr-create", "--title", "t"},
		{"pr-comment", "1", "--body", "b"}, {"issue-list"}, {"issue-view", "2"},
		{"ci-status"}, {"ci-logs", "9"}, {"nope"},
	} {
		if strings.TrimSpace(p.DescribeCall(args)) == "" {
			t.Fatalf("DescribeCall(%v) empty", args)
		}
	}
	if !p.IsConcurrencySafe([]string{"pr-list"}) || p.IsConcurrencySafe([]string{"pr-create", "--title", "t"}) {
		t.Fatal("concurrency caps must mirror read-only")
	}
}

func TestForge_FlattenedEnvelopeArgv(t *testing.T) {
	// The agent loop flattens {"cmd":"pr-view","args":{"number":42}} to
	// ["pr-view","--number","42"] — number/run must not become the target.
	inv, err := parseForgeInvocation([]string{"pr-view", "--number", "42"})
	if err != nil || inv.number != "42" {
		t.Fatalf("flattened pr-view number: %q (%v)", inv.number, err)
	}
	inv, _ = parseForgeInvocation([]string{"ci-logs", "--run", "9987"})
	if inv.run != "9987" {
		t.Fatalf("flattened ci-logs run: %q", inv.run)
	}
	inv, _ = parseForgeInvocation([]string{"pr-comment", "--number=7", "--body", "hi"})
	if inv.number != "7" || inv.body != "hi" {
		t.Fatalf("flattened pr-comment: %+v", inv)
	}
	// Positional still works.
	inv, _ = parseForgeInvocation([]string{"pr", "view", "5"})
	if inv.number != "5" {
		t.Fatalf("positional pr view regressed: %q", inv.number)
	}
}
