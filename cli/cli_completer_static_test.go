/*
 * ChatCLI - Contract tests for the completer's static suggestion lists.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * These helpers replaced inline literals in the legacy completer. Each one
 * is a stable catalog the user sees in autocomplete — adding/removing an
 * entry is a UX-visible change and worth a test that names every member
 * explicitly. We assert the membership AND that no entry has an empty
 * description (which would render as a confusing blank line in go-prompt).
 */
package cli

import (
	"testing"
)

func TestSkillSubcommandSuggestions_MembershipAndDescriptions(t *testing.T) {
	got := skillSubcommandSuggestions()
	want := map[string]bool{
		"search": true, "install": true, "uninstall": true, "list": true,
		"info": true, "registries": true, "registry": true, "prefer": true,
		"pin": true, "unpin": true, "pinned": true, "help": true,
	}
	if len(got) != len(want) {
		t.Errorf("len = %d, want %d (extra/missing entries silently change the UX)", len(got), len(want))
	}
	for _, s := range got {
		if !want[s.Text] {
			t.Errorf("unexpected subcommand %q in the catalog", s.Text)
		}
		if s.Description == "" {
			t.Errorf("subcommand %q has an empty description (would render as blank line)", s.Text)
		}
		delete(want, s.Text)
	}
	for missing := range want {
		t.Errorf("missing subcommand %q", missing)
	}
}

func TestContextSubcommands_Membership(t *testing.T) {
	got := contextSubcommands()
	want := map[string]bool{
		"create": true, "update": true, "attach": true, "detach": true,
		"list": true, "show": true, "inspect": true, "delete": true,
		"merge": true, "attached": true, "export": true, "import": true,
		"metrics": true, "help": true,
	}
	if len(got) != len(want) {
		t.Errorf("len = %d, want %d", len(got), len(want))
	}
	for _, s := range got {
		if !want[s.Text] {
			t.Errorf("unexpected subcommand %q", s.Text)
		}
		if s.Description == "" {
			t.Errorf("subcommand %q has empty description", s.Text)
		}
		delete(want, s.Text)
	}
	for missing := range want {
		t.Errorf("missing context sub %q", missing)
	}
}

func TestContextAttachFlagSuggestions_AllPairsPresent(t *testing.T) {
	// /context attach exposes three flags, each with a long form and a
	// short form. Both forms must be present so users can tab-complete
	// either spelling.
	got := contextAttachFlagSuggestions()
	wantFlags := map[string]bool{
		"--priority": true, "-p": true,
		"--chunk": true, "-c": true,
		"--chunks": true, "-C": true,
	}
	if len(got) != len(wantFlags) {
		t.Errorf("len = %d, want %d", len(got), len(wantFlags))
	}
	for _, s := range got {
		if !wantFlags[s.Text] {
			t.Errorf("unexpected attach flag %q", s.Text)
		}
		if s.Description == "" {
			t.Errorf("attach flag %q missing description", s.Text)
		}
		delete(wantFlags, s.Text)
	}
	for missing := range wantFlags {
		t.Errorf("attach flag %q missing", missing)
	}
}

func TestContextInspectFlags_OnlyChunkPair(t *testing.T) {
	got := contextInspectFlags()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (--chunk, -c)", len(got))
	}
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.Text] = true
		if s.Description == "" {
			t.Errorf("inspect flag %q missing description", s.Text)
		}
	}
	if !seen["--chunk"] || !seen["-c"] {
		t.Errorf("inspect must offer both --chunk and -c; got %+v", got)
	}
}

func TestContextCreateUpdateFlagSuggestions_Membership(t *testing.T) {
	got := contextCreateUpdateFlagSuggestions()
	wantFlags := map[string]bool{
		"--mode": true, "-m": true,
		"--description": true, "--desc": true, "-d": true,
		"--tags": true, "-t": true,
		"--force": true, "-f": true,
	}
	if len(got) != len(wantFlags) {
		t.Errorf("len = %d, want %d", len(got), len(wantFlags))
	}
	for _, s := range got {
		if !wantFlags[s.Text] {
			t.Errorf("unexpected create/update flag %q", s.Text)
		}
		delete(wantFlags, s.Text)
	}
	for missing := range wantFlags {
		t.Errorf("create/update flag %q missing", missing)
	}
}

func TestContextModeValueSuggestions_KnownValuesOnly(t *testing.T) {
	got := contextModeValueSuggestions()
	wantValues := map[string]bool{
		"full": true, "summary": true, "chunked": true, "smart": true,
	}
	if len(got) != len(wantValues) {
		t.Errorf("len = %d, want %d", len(got), len(wantValues))
	}
	for _, s := range got {
		if !wantValues[s.Text] {
			t.Errorf("unexpected mode value %q", s.Text)
		}
		if s.Description == "" {
			t.Errorf("mode value %q missing description", s.Text)
		}
		delete(wantValues, s.Text)
	}
	for missing := range wantValues {
		t.Errorf("mode value %q missing", missing)
	}
}

func TestSkillSubcommandHandler_AllExpectedRoutesNonNil(t *testing.T) {
	// We verify the routing TABLE: each documented sub must yield a
	// non-nil handler. We do NOT invoke the handlers here — that's a
	// different test concern in cli_completer_dispatch_test.go.
	cli := &ChatCLI{}
	wantNonNil := []string{"uninstall", "remove", "install", "info", "registry", "pin", "unpin", "prefer"}
	for _, sub := range wantNonNil {
		if h := cli.skillSubcommandHandler(sub); h == nil {
			t.Errorf("expected non-nil handler for %q", sub)
		}
	}
	wantNil := []string{"unknown", "list", "search", "help", ""}
	for _, sub := range wantNil {
		if h := cli.skillSubcommandHandler(sub); h != nil {
			t.Errorf("expected nil handler for %q (no contextual suggestion)", sub)
		}
	}
}
