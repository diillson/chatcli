/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/update"
)

// A mistyped argument to /update used to fall through to the install.
func TestUpdateTypoDoesNotInstall(t *testing.T) {
	prev := detectInstallFn
	called := false
	detectInstallFn = func() update.Info { called = true; return prev() }
	t.Cleanup(func() { detectInstallFn = prev })

	c := &ChatCLI{}
	out := captureStdout(t, func() { c.handleUpdateCommand(context.Background(), "/update chek") })
	if called {
		t.Fatal("/update chek started the update flow")
	}
	if !strings.Contains(out, "chek") {
		t.Fatalf("the typo was not reported:\n%s", out)
	}
}

// Only a yes approves a gated channel action; any other word used to.
func TestChannelConfirmNeedsAYesOrANo(t *testing.T) {
	c := &ChatCLI{}
	for _, word := range []string{"nope", "maybe", "yess", "rejeitar"} {
		out := captureStdout(t, func() {
			c.runChannelConfirm(context.Background(), []string{"/channel", "confirm", "7", word})
		})
		if !strings.Contains(out, i18n.T("chan.cmd.confirm_bad_answer", word)) {
			t.Errorf("%q was not refused as an answer:\n%s", word, out)
		}
	}
}

func TestNearSubcommand(t *testing.T) {
	subs := []string{"create", "new", "list", "ls", "remove", "rm", "delete", "status"}
	for word, want := range map[string]string{
		"lsit": "list", "lis": "list", "listt": "list", "creat": "create", "remvoe": "remove",
		"statu": "status", "delet": "delete", "sttaus": "status",
	} {
		if got := nearSubcommand(word, subs...); got != want {
			t.Errorf("nearSubcommand(%q) = %q, want %q", word, got, want)
		}
	}
	// Exact subcommands and real branch names are not near-misses.
	for _, word := range []string{"list", "ls", "feature-x", "fix/login", "main", "nw", "release", "stats-page"} {
		if got := nearSubcommand(word, subs...); got != "" {
			t.Errorf("nearSubcommand(%q) = %q, want none", word, got)
		}
	}
}

func TestParseParkAge(t *testing.T) {
	for in, want := range map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "90m": 90 * time.Minute} {
		if got, err := parseParkAge(in); err != nil || got != want {
			t.Errorf("parseParkAge(%q) = %v, %v", in, got, err)
		}
	}
	// A zero or negative age put the cutoff at or after now: every
	// snapshot was deleted.
	for _, in := range []string{"0s", "-1h", "0d", "-3d", "soon", ""} {
		if d, err := parseParkAge(in); err == nil {
			t.Errorf("parseParkAge(%q) = %v, want an error", in, d)
		}
	}
}

func TestParkedUnknownSubcommandIsReported(t *testing.T) {
	c := &ChatCLI{}
	out := captureStdout(t, func() { c.handleParkedCommand("/parked prnue") })
	if !strings.Contains(out, i18n.T("park.cmd.unknown_sub", "prnue")) || !strings.Contains(out, "/parked gc") {
		t.Fatalf("a mistyped subcommand was not reported:\n%s", out)
	}
	out = captureStdout(t, func() { c.parkGC([]string{"-1h"}) })
	if !strings.Contains(out, i18n.T("park.cmd.gc_bad_duration", "-1h")) {
		t.Fatalf("a negative age was accepted:\n%s", out)
	}
}

// "/compactx" is not "/compact x".
func TestCompactRouteIsNotAPrefix(t *testing.T) {
	c := &ChatCLI{}
	out := captureStdout(t, func() { c.handleCompactCommand(context.Background(), "/compactx") })
	if !strings.Contains(out, i18n.T("error.unknown_command")) {
		t.Fatalf("/compactx was taken as a compaction:\n%s", out)
	}
}

func TestAutoCompactIsCompleted(t *testing.T) {
	out := suggestTexts((&ChatCLI{}).getAutoCompactSuggestions(docFor("/autocompact ")))
	for _, want := range []string{"default", "60%", "150k"} {
		if !contains(out, want) {
			t.Errorf("/autocompact offers no %q; got %v", want, out)
		}
	}
	if got := suggestTexts((&ChatCLI{}).getAutoCompactSuggestions(docFor("/autocompact 60% "))); len(got) != 0 {
		t.Errorf("/autocompact offers a second value: %v", got)
	}
}
