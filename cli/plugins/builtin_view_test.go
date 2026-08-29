/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * builtin_view_test.go
 *
 * @view plugin surface: lenient parsing, dispatch through the adapter seam,
 * degrade without an adapter, and the read-only capability.
 */
package plugins

import (
	"context"
	"strings"
	"testing"
)

type fakeViewAdapter struct{ lastPath string }

func (f *fakeViewAdapter) ViewImage(_ context.Context, path string) (string, error) {
	f.lastPath = path
	return "Image " + path + " staged", nil
}

func TestViewParseForms(t *testing.T) {
	cases := map[string][]string{
		"shot.png":     {`{"cmd":"view","args":{"file":"shot.png"}}`},
		"a b.png":      {`{"cmd":"view","args":{"path":"a b.png"}}`},
		"flat.png":     {"view", "flat.png"},
		"bare.png":     {"bare.png"},
		"spaced x.png": {"view", "spaced", "x.png"},
	}
	for want, args := range cases {
		got, err := parseViewInvocation(args)
		if err != nil || got != want {
			t.Fatalf("%v: got %q (%v), want %q", args, got, err, want)
		}
	}
	for _, args := range [][]string{nil, {"view"}, {`{"cmd":"view"}`}, {`{broken`}} {
		if _, err := parseViewInvocation(args); err == nil {
			t.Fatalf("%v must error", args)
		}
	}
}

func TestViewDispatchAndCaps(t *testing.T) {
	fake := &fakeViewAdapter{}
	SetViewAdapter(fake)
	t.Cleanup(func() { SetViewAdapter(nil) })

	p := NewBuiltinViewPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"view","args":{"file":"shot.png"}}`})
	if err != nil || !strings.Contains(out, "staged") || fake.lastPath != "shot.png" {
		t.Fatalf("dispatch broken: out=%q err=%v path=%q", out, err, fake.lastPath)
	}

	if !p.IsReadOnly([]string{"view", "x.png"}) || !p.IsConcurrencySafe(nil) {
		t.Fatal("@view must be read-only and concurrency-safe")
	}
	if strings.TrimSpace(p.DescribeCall([]string{"view", "x.png"})) == "" ||
		strings.TrimSpace(p.DescribeCall(nil)) == "" {
		t.Fatal("DescribeCall must never be empty")
	}
	if p.Name() != "@view" || !strings.Contains(p.Usage(), "view") || !strings.Contains(p.Schema(), "image") {
		t.Fatal("plugin identity incomplete")
	}
}

func TestViewWithoutAdapterErrors(t *testing.T) {
	SetViewAdapter(nil)
	p := NewBuiltinViewPlugin()
	if _, err := p.Execute(context.Background(), []string{"view", "x.png"}); err == nil {
		t.Fatal("missing adapter must error")
	}
}

func TestViewParse_FlattenedEnvelopeArgv(t *testing.T) {
	// ["view","--file",X] from the flattened envelope must yield X, not "--file X".
	got, err := parseViewInvocation([]string{"view", "--file", "/tmp/shot.png"})
	if err != nil || got != "/tmp/shot.png" {
		t.Fatalf("flattened view --file: %q (%v)", got, err)
	}
	got, _ = parseViewInvocation([]string{"view", "--path=/tmp/a b.png"})
	if got != "/tmp/a b.png" {
		t.Fatalf("flattened --path=: %q", got)
	}
	if _, err := parseViewInvocation([]string{"view", "--file"}); err == nil {
		t.Fatal("dangling --file must error, not return a bogus path")
	}
	// Bare positional still works.
	got, _ = parseViewInvocation([]string{"shot.png"})
	if got != "shot.png" {
		t.Fatalf("bare path regressed: %q", got)
	}
}
