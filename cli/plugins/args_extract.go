/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"encoding/json"
	"strings"
)

// extractStringArg looks for a string value at any of the given JSON keys
// (in the first positional arg if it parses as JSON) and falls back to
// --flag style scanning across the positional args. Used by builtins to
// surface a single contextual value in DescribeCall without depending on
// the heavier per-plugin argument parsing pipeline.
//
// Keys are tried in order. Empty values are skipped.
func extractStringArg(args []string, keys ...string) string {
	if len(args) == 0 {
		return ""
	}
	// 1. JSON in the first arg (the common case for native tool calls).
	if v := stringFromJSONArg(args[0], keys); v != "" {
		return v
	}
	// 2. --flag value across all args.
	if v := stringFromFlagArgs(args, keys); v != "" {
		return v
	}
	// 3. Whole-arg fallback for simple plugins: the first non-flag arg.
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" || strings.HasPrefix(a, "-") || strings.HasPrefix(a, "{") || strings.HasPrefix(a, "[") {
			continue
		}
		return a
	}
	return ""
}

// stringFromJSONArg pulls a string value out of a single JSON argument
// blob, handling both flat formats (`{"url":"…"}`) and the nested @coder
// envelope (`{"cmd":"…","args":{"url":"…"}}`).
func stringFromJSONArg(raw string, keys []string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || (raw[0] != '{' && raw[0] != '[') {
		return ""
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return ""
	}
	// Look in top level.
	if v := stringFromMap(top, keys); v != "" {
		return v
	}
	// Then in nested "args" if present.
	if argsField, ok := top["args"]; ok {
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(argsField, &inner); err == nil {
			if v := stringFromMap(inner, keys); v != "" {
				return v
			}
		}
	}
	return ""
}

// stringFromMap returns the first non-empty string value found for any
// of the given keys in the JSON map. Numbers and booleans are ignored
// — DescribeCall is for showing human-readable identifiers.
func stringFromMap(m map[string]json.RawMessage, keys []string) string {
	for _, k := range keys {
		if raw, ok := m[k]; ok {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil && s != "" {
				return s
			}
		}
	}
	return ""
}

// stringFromFlagArgs scans positional args for `--key value` pairs. The
// value may be the next arg or, after stripping quotes, embedded as
// `--key=value`. Returns the first match found across keys.
func stringFromFlagArgs(args []string, keys []string) string {
	for _, k := range keys {
		flag := "--" + k
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return trimQuotes(args[i+1])
			}
			if strings.HasPrefix(a, flag+"=") {
				return trimQuotes(strings.TrimPrefix(a, flag+"="))
			}
		}
	}
	return ""
}

// trimQuotes strips a single matching pair of surrounding ASCII quotes.
func trimQuotes(s string) string {
	if len(s) < 2 {
		return s
	}
	if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// extractQueryArg is a convenience wrapper for builtins that show a
// query string in the spinner. Tries the common key names used by both
// @websearch (query/q) and assistant-style payloads.
func extractQueryArg(args []string) string {
	return extractStringArg(args, "query", "q", "term", "search")
}

// extractURLArg is the analog for @webfetch.
func extractURLArg(args []string) string {
	return extractStringArg(args, "url", "uri", "address")
}

// extractPathArg is the analog for file-oriented plugins (@coder read /
// future Read tool). It accepts both "file" and "path" because both
// appear in the codebase's existing schemas.
func extractPathArg(args []string) string {
	return extractStringArg(args, "file", "path", "filepath")
}

// extractNestedArg pulls a string from inside the `args` field of the
// @coder envelope (or any other plugin that uses the same nested shape).
// Useful when a key collides with a top-level field — e.g. @coder's
// `{"cmd":"exec","args":{"cmd":"go test"}}` has "cmd" in both layers.
// extractStringArg returns the OUTER cmd (which is the subcommand name);
// this helper returns the INNER one (the actual user command).
func extractNestedArg(args []string, keys ...string) string {
	if len(args) == 0 {
		return ""
	}
	raw := strings.TrimSpace(args[0])
	if raw == "" || raw[0] != '{' {
		// No JSON envelope to dig into — fall back to flag-style scan.
		return stringFromFlagArgs(args, keys)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return stringFromFlagArgs(args, keys)
	}
	if inner, ok := top["args"]; ok {
		var innerMap map[string]json.RawMessage
		if err := json.Unmarshal(inner, &innerMap); err == nil {
			if v := stringFromMap(innerMap, keys); v != "" {
				return v
			}
		}
	}
	return stringFromFlagArgs(args, keys)
}
