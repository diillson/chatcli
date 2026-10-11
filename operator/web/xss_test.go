/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package web

import (
	"regexp"
	"strings"
	"testing"
)

// escapeHtml must escape both quote characters: its output lands inside
// quoted attribute values (title="…", data-name="…") filled with text the
// LLM and the cluster's logs produced. The test reads the escape table the
// script declares and applies it the way the script's replace() does.
func TestDashboardEscapeHtmlCoversQuotes(t *testing.T) {
	script := parseDashboard(t, dashboardSource(t)).script
	fn := regexp.MustCompile(`(?s)function escapeHtml\(str\) \{.*?\n\}`).FindString(script)
	if fn == "" {
		t.Fatal("escapeHtml not found")
	}
	if !strings.Contains(fn, `.replace(/[&<>"']/g, c => HTML_ESCAPES[c])`) {
		t.Fatalf("escapeHtml must replace every one of & < > \" ' through HTML_ESCAPES:\n%s", fn)
	}
	table := regexp.MustCompile(`const HTML_ESCAPES = \{([^}]*)\};`).FindStringSubmatch(script)
	if table == nil {
		t.Fatal("HTML_ESCAPES table not found")
	}
	escapes := map[string]string{}
	for _, m := range regexp.MustCompile(`(?:'([^']+)'|"([^"]+)"): '([^']+)'`).FindAllStringSubmatch(table[1], -1) {
		escapes[m[1]+m[2]] = m[3]
	}
	want := map[string]string{"&": "&amp;", "<": "&lt;", ">": "&gt;", `"`: "&quot;", "'": "&#39;"}
	for ch, ent := range want {
		if escapes[ch] != ent {
			t.Errorf("HTML_ESCAPES[%q] = %q, want %q", ch, escapes[ch], ent)
		}
	}
	hostile := `x" onmouseover="alert(1)" data-x='<img src=x onerror=alert(2)>&`
	var out strings.Builder
	for _, r := range hostile {
		if e, ok := escapes[string(r)]; ok {
			out.WriteString(e)
		} else {
			out.WriteRune(r)
		}
	}
	if strings.ContainsAny(out.String(), `"'<>`) {
		t.Fatalf("escaped output still breaks out of an attribute: %s", out.String())
	}
}

// No inline event handler anywhere, in the markup or in the templates the
// script builds: the Content-Security-Policy refuses them, and an argument
// built into handler code is parsed as script after the HTML parser has
// already decoded the entities that escaped it.
func TestDashboardHasNoInlineHandlers(t *testing.T) {
	src := dashboardSource(t)
	if m := regexp.MustCompile(`(?i)<[^>]*\son[a-z]+\s*=`).FindString(src); m != "" {
		t.Fatalf("inline event handler in the dashboard: %s", m)
	}
	if strings.Contains(strings.ToLower(src), "javascript:") {
		t.Fatal("javascript: URL in the dashboard")
	}
}

// Every data-action the markup or a template names is wired in ACTIONS.
func TestDashboardActionsAreRegistered(t *testing.T) {
	src := dashboardSource(t)
	script := parseDashboard(t, src).script
	block := regexp.MustCompile(`(?s)const ACTIONS = \{(.*?)\n\};`).FindStringSubmatch(script)
	if block == nil {
		t.Fatal("ACTIONS table not found")
	}
	registered := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s+([A-Za-z]+):`).FindAllStringSubmatch(block[1], -1) {
		registered[m[1]] = true
	}
	used := regexp.MustCompile(`data-action="([A-Za-z]+)"`).FindAllStringSubmatch(src, -1)
	if len(used) < 20 {
		t.Fatalf("expected the dashboard to use data-action widely, found %d", len(used))
	}
	for _, m := range used {
		if !registered[m[1]] {
			t.Errorf("data-action %q has no ACTIONS entry", m[1])
		}
	}
	// showError builds its retry button from the loader's name.
	for _, m := range regexp.MustCompile(`showError\([^)]*'([A-Za-z]+)\(\)'\)`).FindAllStringSubmatch(script, -1) {
		if !registered[m[1]] {
			t.Errorf("retry action %q has no ACTIONS entry", m[1])
		}
	}
}
