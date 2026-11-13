/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package dash

import (
	"context"
	"encoding/json"
	"io"
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
		if !got.Themes[name].Mono || !got.Themes[name].Flat {
			t.Fatalf("%s: flags lost: %+v", name, got.Themes[name])
		}
		code, hdr, body := getHeader(t, s, base(s)+"api/events?run="+runID, s.token)
		var poll struct {
			Events []json.RawMessage `json:"events"`
		}
		if code != 200 || json.Unmarshal([]byte(body), &poll) != nil || len(poll.Events) != 2 {
			t.Fatalf("events = %d %s: the feed must be untouched", code, body)
		}
		if hdr.Get(themeHeader) != name {
			t.Fatalf("%s: the poll names %q", name, hdr.Get(themeHeader))
		}
	}
}

func getHeader(t *testing.T, s *Server, url, token string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set(tokenHeader, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(body)
}

// The page was following the terminal only on the events poll, which it
// reaches after it has a run and after the state read succeeded. With no run
// at all, and on a failing state read, the answers still name the theme, and
// the page reads the name before it looks at the status.
func TestThemeIsNamedOnEveryAnswerEvenWithoutARun(t *testing.T) {
	useTheme(t, "onyx")
	s, err := Start(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	for path, want := range map[string]int{"api/runs": 200, "api/state": 404, "api/events": 404, "api/state?run=nope": 404} {
		code, hdr, _ := getHeader(t, s, base(s)+path, s.token)
		if code != want || hdr.Get(themeHeader) != "onyx" {
			t.Errorf("%s = %d naming %q, want %d naming onyx", path, code, hdr.Get(themeHeader), want)
		}
	}
	if err := theme.SetActive("onyx-light"); err != nil {
		t.Fatal(err)
	}
	if _, hdr, _ := getHeader(t, s, base(s)+"api/runs", s.token); hdr.Get(themeHeader) != "onyx-light" {
		t.Errorf("after a change the runs poll names %q", hdr.Get(themeHeader))
	}
	// Nothing about the terminal is told to a caller without the token.
	if code, hdr, _ := getHeader(t, s, base(s)+"api/runs", ""); code != 403 || hdr.Get(themeHeader) != "" {
		t.Errorf("without the token = %d naming %q", code, hdr.Get(themeHeader))
	}

	page := string(dashHTML)
	read := strings.Index(page, "followTerminal(r.headers.get('"+themeHeader+"'));")
	fail := strings.Index(page, "if (!r.ok) throw new Error(url")
	if read < 0 || fail < 0 || read > fail {
		t.Fatal("the page must read the theme header before it gives up on a failed answer")
	}
	if strings.Contains(page, "ev.theme") {
		t.Fatal("the page must not depend on the events body for the theme")
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
	if len(pairs) != 14 {
		t.Fatalf("skin table has %d entries, want the page's 14 custom properties", len(pairs))
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

// Under a skin every control of the page takes its edge from the
// control-edge role, never from the divider token: the divider is about
// 1.2:1 on the light variant, where a control would be found only by its
// label.
func TestPageControlsUseTheControlEdge(t *testing.T) {
	page := string(dashHTML)
	for _, want := range []string{"--ctl: var(--line);", "'--ctl': 'control'", "html[data-skin] select, html[data-skin] button.hbtn, html[data-skin] .tabs button.active { border-color: var(--ctl); }"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
}
