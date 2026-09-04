/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package i18nparity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLocales_MergesFragments(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("en.json", `{"a":"A"}`)
	write("en.feature.json", `{"b":"B"}`)
	write("pt-BR.json", `{"a":"A"}`)
	write("pt-BR.feature.json", `{"b":"B"}`)
	write("en-US.json", `{"a":"A"}`)
	locales, err := LoadLocales(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(locales) != 3 {
		t.Fatalf("fragments must not become locales: %d", len(locales))
	}
	for _, l := range locales {
		switch l.Name {
		case "en", "pt-BR":
			if l.Keys["b"] != "B" || len(l.Fragments) != 1 {
				t.Fatalf("%s: fragment not merged: %+v", l.Name, l)
			}
		case "en-US":
			if _, ok := l.Keys["b"]; ok {
				t.Fatal("en-US has no fragment")
			}
		default:
			t.Fatalf("unexpected locale %q", l.Name)
		}
	}
	// Parity still catches the locale that lacks the fragment's key.
	missing := MissingByLocale(locales)
	if len(missing["en-US"]) != 1 || missing["en-US"][0] != "b" {
		t.Fatalf("missing = %v", missing)
	}
}
