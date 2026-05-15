/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
// Package i18nparity verifies that every i18n key referenced from Go
// source exists in every JSON locale file, and that no locale is missing
// keys present in its siblings. Catches the most common i18n bug class:
// a PR adds a key to en.json but forgets pt-BR.json, the gate stays
// green, and the missing translation only surfaces at runtime in the
// untouched locale.
package i18nparity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Locale is a single JSON file's contents: filename plus its flat key→value map.
type Locale struct {
	Name string            // basename without extension, e.g. "en-US"
	Path string            // full path to the JSON file
	Keys map[string]string // top-level flat keys (this project's convention)
}

// LoadLocales reads every *.json under dir into a slice of Locale. The
// directory is expected to be `i18n/locales/`. Keys are taken as a flat
// map — the chatcli i18n convention is dot-namespaced keys at the top
// level ("cfg.sub.prov.moonshot"), not nested objects.
func LoadLocales(dir string) ([]Locale, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("i18nparity: read dir %s: %w", dir, err)
	}

	locales := make([]Locale, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		// #nosec G304 -- path is dir (caller-controlled at the CLI flag)
		// plus a directory entry returned by os.ReadDir; cannot escape dir.
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return nil, fmt.Errorf("i18nparity: read %s: %w", path, err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("i18nparity: parse %s: %w", path, err)
		}
		keys := make(map[string]string, len(raw))
		for k, v := range raw {
			// Only string values count as "translation entries". Nested
			// objects are out of scope for the chatcli convention but we
			// don't error on them — future schema evolution can extend.
			var s string
			if err := json.Unmarshal(v, &s); err == nil {
				keys[k] = s
			}
		}
		locales = append(locales, Locale{
			Name: strings.TrimSuffix(e.Name(), ".json"),
			Path: path,
			Keys: keys,
		})
	}

	if len(locales) == 0 {
		return nil, fmt.Errorf("i18nparity: no JSON locales found in %s", dir)
	}

	// Sort for stable output. Locale "en" comes before "en-US" before "pt-BR"
	// alphabetically, which is the order reviewers expect.
	sort.Slice(locales, func(i, j int) bool { return locales[i].Name < locales[j].Name })
	return locales, nil
}

// MissingByLocale reports keys present in at least one locale but missing
// in another. Returns map locale-name → sorted list of missing keys.
// A locale with no entries returns the full union.
func MissingByLocale(locales []Locale) map[string][]string {
	union := map[string]struct{}{}
	for _, l := range locales {
		for k := range l.Keys {
			union[k] = struct{}{}
		}
	}

	out := map[string][]string{}
	for _, l := range locales {
		var missing []string
		for k := range union {
			if _, ok := l.Keys[k]; !ok {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			out[l.Name] = missing
		}
	}
	return out
}
