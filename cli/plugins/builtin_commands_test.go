/*
 * ChatCLI - @commands builtin tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package plugins

import (
	"context"
	"strings"
	"testing"
)

type fakeCommandsAdapter struct{}

func (fakeCommandsAdapter) List() string { return "- review-pr: Review a PR (args: <pr>)" }
func (fakeCommandsAdapter) Get(_ context.Context, name, args string) (string, bool) {
	if name != "review-pr" {
		return "", false
	}
	return "Review PR " + args + ".", true
}

func TestBuiltinCommands_ListAndGet(t *testing.T) {
	SetCommandsAdapter(fakeCommandsAdapter{})
	t.Cleanup(func() { SetCommandsAdapter(nil) })
	p := NewBuiltinCommandsPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`})
	if err != nil || !strings.Contains(out, "review-pr") {
		t.Fatalf("list failed: %q %v", out, err)
	}

	// Canonical nested envelope.
	out, err = p.Execute(context.Background(), []string{`{"cmd":"get","args":{"name":"review-pr","args":"1326 security"}}`})
	if err != nil || out != "Review PR 1326 security." {
		t.Fatalf("get nested failed: %q %v", out, err)
	}

	// Lenient flat and plain-text forms.
	if out, err = p.Execute(context.Background(), []string{`{"cmd":"get","name":"review-pr","args":"7"}`}); err != nil || out != "Review PR 7." {
		t.Fatalf("get flat failed: %q %v", out, err)
	}
	if out, err = p.Execute(context.Background(), []string{"get review-pr 42"}); err != nil || out != "Review PR 42." {
		t.Fatalf("get plain failed: %q %v", out, err)
	}
	if out, err = p.Execute(context.Background(), nil); err != nil || !strings.Contains(out, "review-pr") {
		t.Fatalf("bare call must default to list: %q %v", out, err)
	}

	// Errors are actionable.
	if _, err = p.Execute(context.Background(), []string{`{"cmd":"get"}`}); err == nil {
		t.Error("get without name must error")
	}
	if _, err = p.Execute(context.Background(), []string{"get nope"}); err == nil || !strings.Contains(err.Error(), "list") {
		t.Errorf("unknown command must point at list: %v", err)
	}
}

func TestBuiltinCommands_NoAdapter(t *testing.T) {
	SetCommandsAdapter(nil)
	if _, err := NewBuiltinCommandsPlugin().Execute(context.Background(), []string{"list"}); err == nil {
		t.Error("missing adapter must error, not panic")
	}
}
