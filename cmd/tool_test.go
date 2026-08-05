/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cmd

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli"
)

// fakeToolRunner scripts the toolRunner surface and records the call made.
type fakeToolRunner struct {
	tools   []cli.RPCToolInfo
	out     string
	err     error
	gotName string
	gotJSON string
	gotArgv []string
	viaJSON bool
	viaArgv bool
}

func (f *fakeToolRunner) ListAllRPCTools() []cli.RPCToolInfo { return f.tools }
func (f *fakeToolRunner) RunAnyRPCTool(_ context.Context, name, args string) (string, error) {
	f.viaJSON, f.gotName, f.gotJSON = true, name, args
	return f.out, f.err
}
func (f *fakeToolRunner) RunAnyRPCToolArgv(_ context.Context, name string, argv []string) (string, error) {
	f.viaArgv, f.gotName, f.gotArgv = true, name, append([]string(nil), argv...)
	return f.out, f.err
}

// TestRunToolWith covers the boot-free subcommand body: list rendering,
// JSON-envelope routing, argv routing and error propagation.
func TestRunToolWith(t *testing.T) {
	ctx := context.Background()

	t.Run("list renders catalog with read-only tag", func(t *testing.T) {
		f := &fakeToolRunner{tools: []cli.RPCToolInfo{
			{Name: "docs-flatten", Description: "flattens docs\nsecond line ignored", ReadOnly: true},
			{Name: "coder", Description: strings.Repeat("x", 150)},
		}}
		var sb strings.Builder
		if err := runToolWith(ctx, f, nil, &sb); err != nil {
			t.Fatalf("list: %v", err)
		}
		got := sb.String()
		if !strings.Contains(got, "@docs-flatten [read-only]") {
			t.Fatalf("missing read-only entry: %q", got)
		}
		if strings.Contains(got, "second line ignored") {
			t.Fatal("description must be first-line only")
		}
		if !strings.Contains(got, "…") {
			t.Fatal("long description must be truncated")
		}
	})

	t.Run("empty catalog message", func(t *testing.T) {
		var sb strings.Builder
		if err := runToolWith(ctx, &fakeToolRunner{}, []string{"list"}, &sb); err != nil {
			t.Fatalf("list: %v", err)
		}
		if strings.TrimSpace(sb.String()) == "" {
			t.Fatal("empty catalog must still print a message")
		}
	})

	t.Run("json envelope routes to the string entry", func(t *testing.T) {
		f := &fakeToolRunner{out: "done"}
		var sb strings.Builder
		if err := runToolWith(ctx, f, []string{"docs-flatten", `{"root":"."}`}, &sb); err != nil {
			t.Fatalf("run: %v", err)
		}
		if !f.viaJSON || f.viaArgv || f.gotJSON != `{"root":"."}` {
			t.Fatalf("wrong routing: %+v", f)
		}
		if !strings.Contains(sb.String(), "done") {
			t.Fatal("tool output must be printed")
		}
	})

	t.Run("argv routes verbatim", func(t *testing.T) {
		f := &fakeToolRunner{out: "ok"}
		var sb strings.Builder
		if err := runToolWith(ctx, f, []string{"@wikipedia", "--term", "two words"}, &sb); err != nil {
			t.Fatalf("run: %v", err)
		}
		if !f.viaArgv || f.viaJSON {
			t.Fatalf("wrong routing: %+v", f)
		}
		if len(f.gotArgv) != 2 || f.gotArgv[1] != "two words" {
			t.Fatalf("argv not preserved: %q", f.gotArgv)
		}
	})

	t.Run("tool error propagates", func(t *testing.T) {
		f := &fakeToolRunner{err: errors.New("boom")}
		var sb strings.Builder
		if err := runToolWith(ctx, f, []string{"@x"}, &sb); err == nil {
			t.Fatal("tool error must propagate to the caller")
		}
	})
}

// TestParseToolInvocation pins the CLI contract of `chatcli tool`:
// no args or "list" lists the catalog; a JSON envelope as the single
// argument passes through as a raw string; anything else is argv passed
// verbatim (never joined and re-split — see the @model parser lesson).
func TestParseToolInvocation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want toolInvocation
	}{
		{"no args lists", nil, toolInvocation{List: true}},
		{"explicit list", []string{"list"}, toolInvocation{List: true}},
		{"argv form", []string{"@docs-flatten", "--root", ".", "--format", "jsonl"},
			toolInvocation{Name: "@docs-flatten", Argv: []string{"--root", ".", "--format", "jsonl"}}},
		{"name without at", []string{"wikipedia", "--term", "Go language"},
			toolInvocation{Name: "wikipedia", Argv: []string{"--term", "Go language"}}},
		{"json envelope", []string{"docs-flatten", `{"root":".","format":"jsonl"}`},
			toolInvocation{Name: "docs-flatten", JSON: `{"root":".","format":"jsonl"}`}},
		{"bare tool no args", []string{"@version"},
			toolInvocation{Name: "@version"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseToolInvocation(tc.args)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseToolInvocation(%v) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}
}
