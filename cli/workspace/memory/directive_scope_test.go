/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"strings"
	"testing"
)

// Synthetic fixtures only — never real user data.

func TestParseDirectiveScope(t *testing.T) {
	cases := []struct{ in, scope, text string }{
		{"[scope:projeto-x] nunca editar o gate", "projeto-x", "nunca editar o gate"},
		{"[scope: Projeto-X ] sempre rodar testes", "projeto-x", "sempre rodar testes"},
		{"evitar jargão", "", "evitar jargão"},
		{"[scope:] texto", "", "[scope:] texto"}, // empty scope → treated as plain text
	}
	for _, c := range cases {
		scope, text := parseDirectiveScope(c.in)
		if scope != c.scope || text != c.text {
			t.Errorf("parseDirectiveScope(%q) = (%q, %q), want (%q, %q)", c.in, scope, text, c.scope, c.text)
		}
	}
}

func TestDirectiveMatchesWorkspace(t *testing.T) {
	ws := "/home/user/projects/projeto-x"
	if !directiveMatchesWorkspace("projeto-x", ws) {
		t.Error("base-name scope must match its workspace")
	}
	if !directiveMatchesWorkspace("Projeto-X", ws) {
		t.Error("matching must be case-insensitive")
	}
	if directiveMatchesWorkspace("outro-repo", ws) {
		t.Error("unrelated scope must not match")
	}
	if !directiveMatchesWorkspace("projects", ws) {
		t.Error("any path segment may scope a directive")
	}
	if directiveMatchesWorkspace("proj", ws) {
		t.Error("partial segment must not match (no substring false positives)")
	}
}

func TestFormatForPromptScoped_FiltersByWorkspace(t *testing.T) {
	ps := NewUserProfileStore(t.TempDir(), testLogger())
	ps.Update(map[string]string{
		"directives": "nunca usar atalho Z; [scope:projeto-x] sempre rodar o linter local; [scope:outro-repo] nunca commitar direto",
	})

	// Inside projeto-x: global + matching scoped (labeled); other scope hidden.
	out := ps.FormatForPromptScoped("/home/user/projects/projeto-x")
	if !strings.Contains(out, "nunca usar atalho Z") {
		t.Errorf("global directive must always be present:\n%s", out)
	}
	if !strings.Contains(out, "[projeto-x] sempre rodar o linter local") {
		t.Errorf("matching scoped directive must be shown with its scope label:\n%s", out)
	}
	if strings.Contains(out, "outro-repo") {
		t.Errorf("out-of-scope directive must be omitted:\n%s", out)
	}

	// Severity partition still applies to scoped entries (text after the tag).
	if !strings.Contains(out, "hard rules (MUST follow)") || !strings.Contains(strings.Split(out, "hard rules")[1], "sempre rodar o linter") {
		t.Errorf("scoped obligation must rank as hard rule:\n%s", out)
	}

	// No workspace context: nothing is silently hidden — all shown, labeled.
	all := ps.FormatForPromptScoped("")
	for _, want := range []string{"[projeto-x]", "[outro-repo]"} {
		if !strings.Contains(all, want) {
			t.Errorf("without workspace every scoped directive must be visible (%s):\n%s", want, all)
		}
	}

	// FormatForPrompt keeps the show-everything behavior.
	if got := ps.FormatForPrompt(); !strings.Contains(got, "[outro-repo]") {
		t.Errorf("FormatForPrompt must show all scopes:\n%s", got)
	}
}

func TestScopedDirectives_UpsertPerScope(t *testing.T) {
	ps := NewUserProfileStore(t.TempDir(), testLogger())
	ps.Update(map[string]string{"directives": "[scope:projeto-x] sempre rodar o linter local"})
	// Same rule, same scope, restated → supersedes.
	ps.Update(map[string]string{"directives": "[scope:projeto-x] sempre rodar o linter local (antes do push)"})
	// Same rule, other scope → distinct entry.
	ps.Update(map[string]string{"directives": "[scope:outro-repo] sempre rodar o linter local"})

	p := ps.Get()
	if len(p.Directives) != 2 {
		t.Fatalf("expected 2 directives (per-scope identity), got %#v", p.Directives)
	}
	if !strings.Contains(strings.Join(p.Directives, "|"), "antes do push") {
		t.Errorf("restated scoped directive must supersede: %#v", p.Directives)
	}
}
