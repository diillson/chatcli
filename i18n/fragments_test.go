/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package i18n

import (
	"strings"
	"testing"

	"golang.org/x/text/language"
)

func TestFragments_MergeIntoTheirLocale(t *testing.T) {
	Init()
	for _, tag := range []language.Tag{language.MustParse("en"), language.MustParse("en-US"), language.MustParse("pt-BR")} {
		v, ok := rawByTag[tag]["chat.recovery.retrying"]
		if !ok || strings.TrimSpace(v) == "" || v == "chat.recovery.retrying" {
			t.Fatalf("%s: fragment key must load into the locale (got %q)", tag, v)
		}
		if _, ok := rawByTag[tag]["cmd.help"]; !ok {
			// The base catalog must still be there alongside the fragment.
			if len(rawByTag[tag]) < 1000 {
				t.Fatalf("%s: base catalog missing: %d keys", tag, len(rawByTag[tag]))
			}
		}
	}
	// No phantom "en.recovery" locale was registered.
	for tag := range rawByTag {
		if strings.Contains(tag.String(), "recovery") {
			t.Fatalf("fragment registered as a locale: %s", tag)
		}
	}
}
