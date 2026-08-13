/*
 * ChatCLI - i18n catalog behavior tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package i18n

import (
	"strings"
	"testing"
)

// initFor re-runs catalog initialization for a given language, bypassing the
// sync.Once (same-package test privilege) so each case starts deterministic.
func initFor(t *testing.T, lang string) {
	t.Helper()
	t.Setenv("CHATCLI_LANG", lang)
	initI18n()
}

// TestT_ArglessReturnsVerbatimTemplate pins the core invariant behind the
// "%!s(MISSING)" regression: an argless T must return the catalog string
// verbatim (modulo %% unescaping), never run it through the formatter —
// templates with pending verbs must reach the caller's fmt/Sprintf intact.
// (Catalog strings no longer carry %w: every error call site wraps with
// fmt.Errorf("%s: %w", i18n.T(key), err), so a %w inside the catalog value
// rendered as a literal "%w" in user-facing errors and was purged.)
func TestT_ArglessReturnsVerbatimTemplate(t *testing.T) {
	initFor(t, "pt-BR")

	if got := T("server.listen.failed"); !strings.Contains(got, "%s") || strings.Contains(got, "MISSING") {
		t.Errorf("argless %%s template must come back verbatim, got %q", got)
	}
	if got := T("chan.cmd.box_title_filtered"); !strings.Contains(got, "%s") || strings.Contains(got, "MISSING") {
		t.Errorf("argless %%s template must come back verbatim, got %q", got)
	}
}

func TestT_WithArgsFormats(t *testing.T) {
	initFor(t, "pt-BR")
	if got := T("chan.cmd.box_title_filtered", "general"); got != "CHANNEL: general" {
		t.Errorf("T with args = %q, want %q", got, "CHANNEL: general")
	}
	if got := T("chan.cmd.ack_done", 2, 3); strings.Contains(got, "MISSING") || !strings.Contains(got, "2") {
		t.Errorf("T with args must format, got %q", got)
	}
}

func TestT_ArglessUnescapesLiteralPercent(t *testing.T) {
	initFor(t, "pt-BR")
	got := T("cost.cmd.cache_savings")
	if strings.Contains(got, "%%") || strings.Contains(got, "MISSING") {
		t.Errorf("escaped %%%% must render as a single %% argless, got %q", got)
	}
	if !strings.Contains(got, "90%") {
		t.Errorf("literal percent lost: %q", got)
	}
}

// TestT_DiagramDescriptionLocalized pins the @diagram catalog entry: the key
// must exist in both shipped locales and its escaped "100%%" must render as a
// single "100%" — proving the builtin's primary tool-catalog string is wired.
func TestT_DiagramDescriptionLocalized(t *testing.T) {
	for _, lang := range []string{"en-US", "pt-BR"} {
		initFor(t, lang)
		got := T("plugins.diagram.description")
		if got == "plugins.diagram.description" || got == "" {
			t.Fatalf("[%s] description not in catalog, got raw key: %q", lang, got)
		}
		if strings.Contains(got, "%%") || strings.Contains(got, "MISSING") {
			t.Errorf("[%s] escaped percent must render as a single %%, got %q", lang, got)
		}
		if !strings.Contains(got, "100%") {
			t.Errorf("[%s] literal percent lost: %q", lang, got)
		}
	}
}

// TestT_CommandAutorouteKeysExist pins the chat→coder auto-route strings in
// both shipped locales: a missing key would surface the raw key in the REPL
// exactly at the moment the mode switch needs to be legible.
func TestT_CommandAutorouteKeysExist(t *testing.T) {
	keys := []string{
		"commands.autoroute.notice",
		"commands.autoroute.notice_oneshot",
		"commands.autoroute.busy",
		"commands.autoroute.disabled_hint",
		"commands.autoroute.expand_failed",
		"commands.autoroute.banner_mode",
		"commands.autoroute.returned_to_chat",
		"complete.command.coder_marker",
	}
	for _, lang := range []string{"en-US", "pt-BR"} {
		initFor(t, lang)
		for _, key := range keys {
			if got := T(key); got == key || got == "" {
				t.Errorf("[%s] %s not in catalog, got raw key", lang, key)
			}
		}
	}
}

func TestT_FallbackToDefaultLanguage(t *testing.T) {
	// A locale that doesn't exist resolves to the default (en) catalog.
	initFor(t, "fr-FR")
	if got := T("llm.error.read_stream"); got != "error reading stream" {
		t.Errorf("unmatched locale must fall back to en raw catalog, got %q", got)
	}
}

func TestT_UnknownKeyReturnsKey(t *testing.T) {
	initFor(t, "pt-BR")
	if got := T("zz.unknown.key.for.test"); got != "zz.unknown.key.for.test" {
		t.Errorf("unknown key must echo the key, got %q", got)
	}
}
