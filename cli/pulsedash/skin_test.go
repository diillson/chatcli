/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package pulsedash

import (
	"encoding/json"
	"net/http"
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

func bootTheme(t *testing.T, s *Server) theme.WebPayload {
	t.Helper()
	code, body, _ := call(t, s, "/", "", "")
	if code != http.StatusOK {
		t.Fatalf("index = %d", code)
	}
	start := strings.Index(body, "const BOOT = ") + len("const BOOT = ")
	var b struct {
		WebTheme theme.WebPayload `json:"web_theme"`
	}
	if err := json.Unmarshal([]byte(body[start:start+strings.Index(body[start:], ";\n")]), &b); err != nil {
		t.Fatalf("boot JSON: %v", err)
	}
	return b.WebTheme
}

// The page learns the terminal theme at load and again on every instances
// poll; a colorful theme is named but not offered, which is the default look.
func TestThemeReachesThePageAndFollowsTheTerminal(t *testing.T) {
	useTheme(t, "dracula")
	s, _ := startDash(t, Options{})

	got := bootTheme(t, s)
	if _, offered := got.Themes["dracula"]; got.Active != "dracula" || offered {
		t.Fatalf("dracula must be named and not offered: %+v", got.Active)
	}

	for _, name := range []string{"onyx", "onyx-light"} {
		if err := theme.SetActive(name); err != nil {
			t.Fatal(err)
		}
		want := theme.Active().Palette
		got = bootTheme(t, s)
		if got.Active != name || got.Themes[name].Roles[theme.WebRoleBackground] != want.Background.Hex ||
			got.Themes[name].Roles[theme.WebRoleDanger] != want.Danger.Hex {
			t.Fatalf("%s: a reload must paint the new theme, got %+v", name, got.Themes[name])
		}
		code, body, _ := call(t, s, "/api/instances", s.token, "")
		var poll struct {
			Theme theme.WebPayload `json:"theme"`
		}
		if code != http.StatusOK || json.Unmarshal([]byte(body), &poll) != nil {
			t.Fatalf("instances = %d %s", code, body)
		}
		if poll.Theme.Active != name || !poll.Theme.Themes[name].Mono || !poll.Theme.Themes[name].Flat {
			t.Fatalf("%s: the poll must carry the theme, got %+v", name, poll.Theme.Active)
		}
	}
}

// The page maps exported role names onto tokens it already defines and holds
// no copy of a monochrome palette: the colors exist in ui/theme only.
func TestPageSkinIsOnlyARoleMapping(t *testing.T) {
	raw, err := assets.ReadFile("assets/dash.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	a := strings.Index(page, "const SKIN_VARS = {")
	if a < 0 {
		t.Fatal("the page has no skin table")
	}
	table := page[a : a+strings.Index(page[a:], "};")]
	roles := theme.WebExport(theme.OnyxTheme()).Roles
	pairs := regexp.MustCompile(`"(--[a-z0-9-]+)":\s*"([a-z_0-9]+)"`).FindAllStringSubmatch(table, -1)
	if len(pairs) < 13 {
		t.Fatalf("skin table has %d entries", len(pairs))
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

// The dash takes its token out of the address bar. It must first park it
// where a reload of the same tab finds it, and only there: sessionStorage,
// never storage that outlives the tab. When storing throws, the statement
// that strips the address is skipped with it, so the token stays in the URL.
func TestPageKeepsItsTokenAcrossAReload(t *testing.T) {
	raw, err := assets.ReadFile("assets/dash.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	keep := regexp.MustCompile(`(?s)try \{\s*if \(TOKEN\) \{ sessionStorage\.setItem\(TOKEN_KEY, TOKEN\); history\.replaceState\(null, "", location\.pathname\); \}\s*else TOKEN = sessionStorage\.getItem\(TOKEN_KEY\) \|\| "";\s*\} catch`)
	if !keep.MatchString(page) {
		t.Fatal("the token must be stored for the tab before the address is cleaned, inside one try")
	}
	if strings.Count(page, "history.replaceState") != 1 {
		t.Fatal("the address may only be cleaned after the token is stored")
	}
	if regexp.MustCompile(`localStorage[^;\n]*TOKEN`).MatchString(page) {
		t.Fatal("the token must never reach localStorage")
	}
	if !strings.Contains(page, `{headers: {"X-Pulse-Token": TOKEN}`) {
		t.Fatal("the API calls must still carry the token in the header")
	}
}

// The API answers a caller that presents the token again after a reload,
// and still refuses one without it: the check itself is untouched.
func TestTokenCheckIsUnchanged(t *testing.T) {
	s, _ := startDash(t, Options{})
	for i := 0; i < 2; i++ {
		if code, _, _ := call(t, s, "/api/instances", s.token, ""); code != http.StatusOK {
			t.Fatalf("with the token = %d", code)
		}
	}
	if code, _, _ := call(t, s, "/api/instances", "", ""); code != http.StatusForbidden {
		t.Fatalf("without the token = %d, want 403", code)
	}
}
