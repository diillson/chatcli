/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package dash

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/diillson/chatcli/ui/theme"
)

// useTheme switches the process theme for one test.
func useTheme(t *testing.T, name string) {
	t.Helper()
	prev := theme.Active().Name
	t.Cleanup(func() { _ = theme.SetActive(prev) })
	if err := theme.SetActive(name); err != nil {
		t.Fatal(err)
	}
}

var bootRE = regexp.MustCompile(`(?s)<script id="boot" type="application/json">(.*?)</script>`)

func bootTheme(t *testing.T, s *Server) theme.WebPayload {
	t.Helper()
	code, body := getWith(t, s.URL(), "", "")
	m := bootRE.FindStringSubmatch(body)
	if code != 200 || m == nil || strings.Contains(body, bootPlaceholder) {
		t.Fatalf("index = %d, boot data not injected", code)
	}
	var b boot
	if err := json.Unmarshal([]byte(m[1]), &b); err != nil {
		t.Fatalf("boot JSON: %v", err)
	}
	return b.WebTheme
}

// The page learns the terminal theme at load and again on every events
// poll; a colorful theme is named but not offered, which is the default look.
func TestThemeReachesThePageAndFollowsTheTerminal(t *testing.T) {
	useTheme(t, "nord")
	s, runID := startTestServer(t)

	got := bootTheme(t, s)
	if _, offered := got.Themes["nord"]; got.Active != "nord" || offered {
		t.Fatalf("nord must be named and not offered: %+v", got.Active)
	}

	for _, name := range []string{"onyx", "onyx-light"} {
		if err := theme.SetActive(name); err != nil {
			t.Fatal(err)
		}
		want := theme.Active().Palette
		got = bootTheme(t, s)
		if got.Active != name || got.Themes[name].Roles[theme.WebRoleBackground] != want.Background.Hex ||
			got.Themes[name].Roles[theme.WebRoleWarning] != want.Warning.Hex {
			t.Fatalf("%s: a reload must paint the new theme, got %+v", name, got.Themes[name])
		}
		code, body := get(t, s, base(s)+"api/events?run="+runID)
		var poll struct {
			Events []json.RawMessage `json:"events"`
			Theme  theme.WebPayload  `json:"theme"`
		}
		if code != 200 || json.Unmarshal([]byte(body), &poll) != nil {
			t.Fatalf("events = %d %s", code, body)
		}
		if len(poll.Events) != 2 {
			t.Fatalf("the feed must be untouched, got %d events", len(poll.Events))
		}
		if poll.Theme.Active != name || !poll.Theme.Themes[name].Mono || !poll.Theme.Themes[name].Flat {
			t.Fatalf("%s: the poll must carry the theme, got %+v", name, poll.Theme.Active)
		}
	}
}

// A page without the placeholder is served as it is.
func TestRenderPageWithoutPlaceholder(t *testing.T) {
	if got := string(renderPage([]byte("<p>plain</p>"))); got != "<p>plain</p>" {
		t.Fatalf("got %q", got)
	}
}

// The page maps exported role names onto custom properties it already
// defines and holds no copy of a monochrome palette.
func TestPageSkinIsOnlyARoleMapping(t *testing.T) {
	page := string(dashHTML)
	a := strings.Index(page, "const SKIN_VARS = {")
	if a < 0 {
		t.Fatal("the page has no skin table")
	}
	table := page[a : a+strings.Index(page[a:], "};")]
	roles := theme.WebExport(theme.OnyxTheme()).Roles
	pairs := regexp.MustCompile(`'(--[a-z0-9-]+)':\s*'([a-z_0-9]+)'`).FindAllStringSubmatch(table, -1)
	if len(pairs) != 13 {
		t.Fatalf("skin table has %d entries, want the page's 13 custom properties", len(pairs))
	}
	for _, m := range pairs {
		if _, ok := roles[m[2]]; !ok {
			t.Errorf("%s maps to %q, which ui/theme does not export", m[1], m[2])
		}
		if !strings.Contains(page, m[1]+":") {
			t.Errorf("%s has no default in the page, so the default look would lose it", m[1])
		}
	}
	for name, w := range theme.WebThemes() {
		for role, hex := range w.Roles {
			if role == theme.WebRoleBackground || role == theme.WebRolePrimary || role == theme.WebRoleTextStrong || role == theme.WebRoleOnPrimary {
				continue // pure black and white are not a palette
			}
			if strings.Contains(strings.ToUpper(page), hex) {
				t.Errorf("the page hard-codes %s (%s %s)", hex, name, role)
			}
		}
	}
	for _, forbidden := range []string{"<link ", "https://", "http://", "@import", "@font-face", "<script src="} {
		if strings.Contains(page, forbidden) {
			t.Errorf("page references an external resource: %q", forbidden)
		}
	}
}

// The page URL keeps the token, so a reload keeps working; the page must
// never strip it or park it in storage that outlives the tab.
func TestPageKeepsItsTokenAcrossAReload(t *testing.T) {
	page := string(dashHTML)
	if !strings.Contains(page, "new URLSearchParams(location.search).get('t')") {
		t.Fatal("the page must read the token from its address")
	}
	if strings.Contains(page, "history.replaceState") || regexp.MustCompile(`(?i)Storage\.setItem\([^)]*token`).MatchString(page) {
		t.Fatal("the page must leave the token in its address and out of storage")
	}
}
