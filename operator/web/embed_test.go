/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package web

import (
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// The dashboard is one self-contained file the REST server serves as is, so
// a restyle is only safe when the contract the inline script relies on
// survives it: every element the script looks up by id is in the document,
// the tab bar still names the ten sections, and the stylesheet declares the
// design tokens the templates reference. These tests are that contract.

func dashboardSource(t *testing.T) string {
	t.Helper()
	raw, err := StaticFiles.ReadFile("static/index.html")
	if err != nil {
		t.Fatalf("read embedded dashboard: %v", err)
	}
	return string(raw)
}

// parseDashboard returns the ids declared in the markup, the ids declared in
// the inline script, the script text, the style text and the tab bar values.
type dashboardShape struct {
	markupIDs map[string]bool
	scriptIDs map[string]bool
	script    string
	style     string
	tabs      []string
}

func parseDashboard(t *testing.T, src string) dashboardShape {
	t.Helper()
	shape := dashboardShape{markupIDs: map[string]bool{}, scriptIDs: map[string]bool{}}
	tz := html.NewTokenizer(strings.NewReader(src))
	var scripts, styles []string
	for {
		tt := tz.Next()
		switch tt {
		case html.ErrorToken:
			if err := tz.Err(); err != nil && !errors.Is(err, io.EOF) {
				t.Fatalf("tokenize dashboard: %v", err)
			}
			shape.script = strings.Join(scripts, "\n")
			shape.style = strings.Join(styles, "\n")
			// Elements the script builds itself: id="x" inside a template,
			// or el.id = 'x' on a node it created.
			for _, re := range []*regexp.Regexp{
				regexp.MustCompile(`\bid="([A-Za-z0-9_-]+)"`),
				regexp.MustCompile(`\.id = '([A-Za-z0-9_-]+)'`),
			} {
				for _, m := range re.FindAllStringSubmatch(shape.script, -1) {
					shape.scriptIDs[m[1]] = true
				}
			}
			return shape
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := tz.Token()
			var id, tab string
			for _, a := range tok.Attr {
				switch a.Key {
				case "id":
					id = a.Val
				case "data-tab":
					tab = a.Val
				}
			}
			if id != "" {
				if shape.markupIDs[id] {
					t.Errorf("duplicate id %q in markup", id)
				}
				shape.markupIDs[id] = true
			}
			if tok.Data == "button" && tab != "" {
				shape.tabs = append(shape.tabs, tab)
			}
			if tok.Data == "script" || tok.Data == "style" {
				if tz.Next() == html.TextToken {
					text := string(tz.Text())
					if tok.Data == "script" {
						scripts = append(scripts, text)
					} else {
						styles = append(styles, text)
					}
				}
			}
		}
	}
}

func TestDashboardDeclaresDesignTokens(t *testing.T) {
	shape := parseDashboard(t, dashboardSource(t))
	root := shape.style[strings.Index(shape.style, ":root"):]
	root = root[:strings.Index(root, "}")]
	for _, tok := range []string{"--bg", "--panel", "--accent", "--ok", "--err"} {
		if !strings.Contains(root, tok+":") {
			t.Errorf(":root does not declare %s", tok)
		}
	}
	// Legacy names the inline templates use must keep resolving.
	for _, tok := range []string{"--text-muted", "--sev-critical", "--sev-high", "--sev-medium", "--sev-low", "--card-bg", "--success", "--warning"} {
		if !strings.Contains(root, tok+":") {
			t.Errorf(":root does not declare legacy token %s referenced by the script", tok)
		}
	}
	for _, want := range []string{"prefers-color-scheme:light", "prefers-reduced-motion:reduce", ":focus-visible"} {
		if !strings.Contains(shape.style, want) {
			t.Errorf("stylesheet lacks %q", want)
		}
	}
}

func TestDashboardScriptFindsEveryID(t *testing.T) {
	shape := parseDashboard(t, dashboardSource(t))
	refs := map[string]bool{}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`getElementById\('([A-Za-z0-9_-]+)'\)`),
		regexp.MustCompile(`\$\('#([A-Za-z0-9_-]+)'\)`),
	} {
		for _, m := range re.FindAllStringSubmatch(shape.script, -1) {
			refs[m[1]] = true
		}
	}
	if len(refs) < 20 {
		t.Fatalf("expected the script to reference dozens of ids, found %d", len(refs))
	}
	var missing []string
	for id := range refs {
		if !shape.markupIDs[id] && !shape.scriptIDs[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("script references ids that no markup declares: %v", missing)
	}
	// The elements the auth flow and the polling loop drive stay in place.
	for _, id := range []string{"tabBar", "healthBadge", "refreshDot", "refreshSelect", "refreshTimer", "logoutBtn", "timeDropdown", "timeRangeLabel", "toastContainer", "overviewStats", "approvalBadge"} {
		if !shape.markupIDs[id] {
			t.Errorf("markup lacks id %q", id)
		}
	}
	for _, id := range []string{"apiKeyOverlay", "apiKeyInput", "apiKeySubmit", "apiKeyError"} {
		if !shape.scriptIDs[id] && !strings.Contains(shape.script, "'"+id+"'") {
			t.Errorf("login flow lost id %q", id)
		}
	}
}

func TestDashboardTabBarKeepsItsSections(t *testing.T) {
	shape := parseDashboard(t, dashboardSource(t))
	want := []string{"overview", "incidents", "slos", "approvals", "aiinsights", "remediations", "runbooks", "postmortems", "clusters", "audit"}
	if strings.Join(shape.tabs, ",") != strings.Join(want, ",") {
		t.Fatalf("tab bar = %v, want %v", shape.tabs, want)
	}
	for _, tab := range want {
		if !shape.markupIDs["page-"+tab] {
			t.Errorf("tab %q has no page-%s panel", tab, tab)
		}
	}
}

func TestDashboardKeepsItsWiring(t *testing.T) {
	src := dashboardSource(t)
	shape := parseDashboard(t, src)
	for _, want := range []string{
		"const API = '/api/v1'",
		"localStorage.getItem('chatcli_api_key')",
		"'X-API-Key'",
		"fetch('/healthz')",
		"setInterval(",
		"localStorage.getItem('panelOrder_' + containerId)",
	} {
		if !strings.Contains(shape.script, want) {
			t.Errorf("script lost %q", want)
		}
	}
	for _, forbidden := range []string{"<link ", "https://", "http://", "@import", "@font-face"} {
		if strings.Contains(shape.style, forbidden) || strings.Contains(src[:strings.Index(src, "<body")], forbidden) {
			t.Errorf("dashboard head or stylesheet reaches outside the file: %q", forbidden)
		}
	}
}
