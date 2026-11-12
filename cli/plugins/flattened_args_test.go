/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"encoding/json"
	"strings"
	"testing"
)

// These tests feed each tool the argv the agent loop really hands it (see
// TestToolCallFlatteningShapes in package cli for the shapes). The JSON
// branch the usage texts describe was the only one under test, while the
// flattened form is the one that runs.

func innerStr(t *testing.T, m map[string]json.RawMessage, key string) string {
	t.Helper()
	var s string
	if raw, ok := m[key]; ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

// A message was queued to a recipient named "--card_id" (or "--text"), and
// the tool answered "mail sent".
func TestMailSendFlattenedReachesTheRecipient(t *testing.T) {
	sub, inner, err := parseMailInvocation([]string{"send", "--card_id", "card-2", "--text", "fix the build", "--to", "coder"})
	if err != nil || sub != "send" {
		t.Fatalf("sub=%q err=%v", sub, err)
	}
	if to, text, card := innerStr(t, inner, "to"), innerStr(t, inner, "text"), innerStr(t, inner, "card_id"); to != "coder" || text != "fix the build" || card != "card-2" {
		t.Fatalf("to=%q text=%q card_id=%q", to, text, card)
	}
	// The positional form keeps working.
	_, inner, _ = parseMailInvocation([]string{"send", "coder", "fix", "the", "build"})
	if to, text := innerStr(t, inner, "to"), innerStr(t, inner, "text"); to != "coder" || text != "fix the build" {
		t.Fatalf("positional: to=%q text=%q", to, text)
	}
	// A bare recipient followed by flags.
	_, inner, _ = parseMailInvocation([]string{"send", "coder", "--text", "hi"})
	if to, text := innerStr(t, inner, "to"), innerStr(t, inner, "text"); to != "coder" || text != "hi" {
		t.Fatalf("mixed: to=%q text=%q", to, text)
	}
}

func TestChannelsListFlattenedFilters(t *testing.T) {
	inv := parseChannelsInvocation("list --channel telegram --limit 5")
	if inv.Cmd != "list" || inv.Channel != "telegram" || inv.Limit != 5 {
		t.Fatalf("flattened: %+v", inv)
	}
	if inv = parseChannelsInvocation("list telegram"); inv.Channel != "telegram" || inv.Limit != 10 {
		t.Fatalf("bare word: %+v", inv)
	}
	if inv = parseChannelsInvocation("unread"); inv.Cmd != "unread" || inv.Channel != "" {
		t.Fatalf("no filter: %+v", inv)
	}
}

func TestWebSearchFlattenedAliases(t *testing.T) {
	q, n := parseWebSearchArgs([]string{"search", "--max_results", "3", "--q", "golang generics"})
	if q != "golang generics" || n != 3 {
		t.Fatalf("q/max_results: %q %d", q, n)
	}
	if q, n = parseWebSearchArgs([]string{"search", "--query", "x", "--maxResults", "7"}); q != "x" || n != 7 {
		t.Fatalf("query/maxResults: %q %d", q, n)
	}
	if q, n = parseWebSearchArgs([]string{"search", "go", "context"}); q != "go context" || n != 10 {
		t.Fatalf("positional: %q %d", q, n)
	}
	if q, _ = parseWebSearchArgs([]string{"search", "--limit", "4"}); q != "" {
		t.Fatalf("a call with no query searched for %q", q)
	}
}

// dry_run:false is dropped by the flattener, so the documented way to write
// left every apply a preview. "apply": true survives.
func TestDiffApplyCanWrite(t *testing.T) {
	decode := func(argv ...string) diffToolArgs {
		t.Helper()
		_, raw, err := parseDiffToolInvocation(argv)
		if err != nil {
			t.Fatal(err)
		}
		return diffToolArgsFrom(raw)
	}
	if d := decode("apply", "--file", "a.go", "--patch", "p"); !d.dryRun {
		t.Fatal("apply with no flag must preview")
	}
	if d := decode("apply", "--apply", "--file", "a.go", "--patch", "p"); d.dryRun {
		t.Fatal(`"apply": true must write`)
	}
	if d := decode(`{"cmd":"apply","args":{"file":"a.go","patch":"p","dry_run":false}}`); d.dryRun {
		t.Fatal("raw JSON dry_run:false must still write")
	}
	if d := decode("apply", "--apply", "--dry_run", "--file", "a.go", "--patch", "p"); !d.dryRun {
		t.Fatal("an explicit dry_run wins over apply")
	}
}

func TestLSPRenameApplySurvivesFlattening(t *testing.T) {
	if in := decodeLSPRaw(`{"file":"a.go","line":1,"column":1,"new_name":"x"}`); !in.DryRun {
		t.Fatal("rename must preview by default")
	}
	if in := decodeLSPRaw(`{"file":"a.go","line":1,"column":1,"new_name":"x","apply":true}`); in.DryRun {
		t.Fatal(`"apply": true must write`)
	}
	p := NewBuiltinLSPPlugin()
	if strings.Contains(p.Usage(), "dry_run\": false") || !strings.Contains(p.Usage(), `"apply": true`) {
		t.Fatal("usage still teaches the form the agent loop drops")
	}
}

// An unknown verb used to become "login" (opening a browser) or "use"
// (switching the model for the rest of the task).
func TestUnknownVerbIsNotAnAction(t *testing.T) {
	if _, _, err := parseMCPLoginInvocation([]string{`{"cmd":"restart","args":{"server":"aws"}}`}); err == nil {
		t.Fatal("@mcp-login restart started a login")
	}
	if _, _, err := parseMCPLoginInvocation([]string{"restart", "--server", "aws"}); err == nil {
		t.Fatal("@mcp-login restart --server started a login")
	}
	if _, _, err := parseMCPLoginInvocation([]string{"restart", "aws"}); err == nil {
		t.Fatal("@mcp-login restart aws started a login")
	}
	// What worked keeps working.
	for _, argv := range [][]string{{"aws"}, {"login", "aws"}, {`{"cmd":"login","args":{"server":"aws"}}`}, {`{"server":"aws"}`}} {
		cmd, server, err := parseMCPLoginInvocation(argv)
		if err != nil || cmd != "login" || server != "aws" {
			t.Fatalf("%v: cmd=%q server=%q err=%v", argv, cmd, server, err)
		}
	}
	if cmd, _, err := parseMCPLoginInvocation([]string{"status"}); err != nil || cmd != "status" {
		t.Fatalf("status: %q %v", cmd, err)
	}

	if _, err := parseModelInvocation([]string{`{"cmd":"benchmark","args":{"model":"gpt-x"}}`}); err == nil {
		t.Fatal("@model benchmark switched the model")
	}
	inv, err := parseModelInvocation([]string{`{"cmd":"use","args":{"model":"gpt-x"}}`})
	if err != nil || inv.cmd != "use" || inv.model != "gpt-x" {
		t.Fatalf("use: %+v %v", inv, err)
	}
	if inv, err = parseModelInvocation([]string{`{"model":"gpt-x"}`}); err != nil || inv.cmd != "use" {
		t.Fatalf("flat model: %+v %v", inv, err)
	}
}
