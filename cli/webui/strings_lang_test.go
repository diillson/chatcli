/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2026 Edilson Freitas
 * License: MIT
 */

package webui

import (
	"testing"

	"github.com/diillson/chatcli/i18n"
)

func TestUICatalogs_ShipEveryLanguage(t *testing.T) {
	i18n.Init()
	cats := UICatalogs()
	for _, lang := range UILanguages {
		got, ok := cats[lang]
		if !ok {
			t.Fatalf("catalog for %q missing", lang)
		}
		if len(got) != len(uiKeys) {
			t.Fatalf("catalog %q has %d keys, want %d", lang, len(got), len(uiKeys))
		}
		for _, k := range uiKeys {
			if got[k] == "" || got[k] == "web.ui."+k {
				t.Fatalf("catalog %q: key %q unresolved (%q)", lang, k, got[k])
			}
		}
	}
	if cats["en"]["composer.send"] == cats["pt-BR"]["composer.send"] {
		t.Fatalf("en and pt-BR catalogs are identical for 'send': %q", cats["en"]["composer.send"])
	}
}

func TestUIStringsFor_UnknownLanguageFallsBack(t *testing.T) {
	i18n.Init()
	got := UIStringsFor("xx-ZZ")
	if len(got) != len(uiKeys) {
		t.Fatalf("got %d keys, want %d", len(got), len(uiKeys))
	}
	// Unknown tags resolve through the default language, never an empty string.
	if got["composer.send"] != UIStringsFor("en")["composer.send"] {
		t.Fatalf("fallback mismatch: %q vs %q", got["composer.send"], UIStringsFor("en")["composer.send"])
	}
}
