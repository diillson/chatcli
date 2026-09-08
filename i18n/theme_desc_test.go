/*
 * ChatCLI - locale parity for the theme description strings
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * T falls back to the default language for a missing key, so a theme
 * description present only in en.json looks fine in an English session and
 * silently prints English inside a pt-BR one. This test compares the KEY SETS
 * across locales directly, which is the only way that drift shows up before a
 * user in the other locale hits it.
 */
package i18n

import (
	"sort"
	"strings"
	"testing"

	"golang.org/x/text/language"
)

const themeDescPrefix = "cfg.ui.theme_desc_"

func themeDescKeys(tag language.Tag) []string {
	var out []string
	for k := range rawByTag[tag] {
		if strings.HasPrefix(k, themeDescPrefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func TestThemeDescriptions_SameKeysInEveryLocale(t *testing.T) {
	Init()
	locales := []language.Tag{
		language.MustParse("en"),
		language.MustParse("en-US"),
		language.MustParse("pt-BR"),
	}
	want := themeDescKeys(locales[0])
	if len(want) == 0 {
		t.Fatalf("no %s* keys loaded at all", themeDescPrefix)
	}
	for _, tag := range locales[1:] {
		got := themeDescKeys(tag)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s theme descriptions drifted from en:\n  en: %v\n  %s: %v", tag, want, tag, got)
		}
	}
	// A description that is only the key, or empty, is worse than missing:
	// it renders as literal noise in the completer and the palette.
	for _, tag := range locales {
		for _, k := range themeDescKeys(tag) {
			if v := strings.TrimSpace(rawByTag[tag][k]); v == "" || v == k {
				t.Fatalf("%s: %s is not a real description (%q)", tag, k, v)
			}
		}
	}
}
