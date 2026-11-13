/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli"
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

func bootTheme(t *testing.T, srv *Server) theme.WebPayload {
	t.Helper()
	code, raw := call(t, srv, http.MethodGet, "/?t="+srv.Token(), nil, false)
	body := string(raw)
	if code != http.StatusOK || strings.Contains(body, bootPlaceholder) {
		t.Fatalf("index = %d, page not rendered", code)
	}
	start := strings.Index(body, "const BOOT = ") + len("const BOOT = ")
	var b boot
	if err := json.Unmarshal([]byte(body[start:start+strings.Index(body[start:], ";\n")]), &b); err != nil {
		t.Fatalf("boot JSON: %v", err)
	}
	return b.WebTheme
}

// The page learns the process theme at load and again on every status poll;
// a colorful theme is named but not offered, which is the default look. The
// status poll keeps every field it had.
func TestThemeReachesThePageAndFollowsTheProcess(t *testing.T) {
	useTheme(t, "tokyo-night")
	srv, _ := startTest(t)

	got := bootTheme(t, srv)
	if _, offered := got.Themes["tokyo-night"]; got.Active != "tokyo-night" || offered {
		t.Fatalf("tokyo-night must be named and not offered: %+v", got.Active)
	}

	for _, name := range []string{"onyx", "onyx-light"} {
		if err := theme.SetActive(name); err != nil {
			t.Fatal(err)
		}
		want := theme.Active().Palette
		got = bootTheme(t, srv)
		if got.Active != name || got.Themes[name].Roles[theme.WebRoleBackground] != want.Background.Hex ||
			got.Themes[name].Roles[theme.WebRoleSuccess] != want.Success.Hex {
			t.Fatalf("%s: a reload must paint the new theme, got %+v", name, got.Themes[name])
		}
		code, raw := call(t, srv, http.MethodGet, "/api/status", nil, true)
		var poll struct {
			Version  string           `json:"version"`
			Provider string           `json:"provider"`
			WebTheme theme.WebPayload `json:"web_theme"`
		}
		if code != http.StatusOK || json.Unmarshal(raw, &poll) != nil {
			t.Fatalf("status = %d %s", code, raw)
		}
		if poll.Provider != "FAKE" {
			t.Fatalf("the status fields must survive, got %s", raw)
		}
		if poll.WebTheme.Active != name || !poll.WebTheme.Themes[name].Mono || !poll.WebTheme.Themes[name].Flat {
			t.Fatalf("%s: the poll must carry the theme, got %+v", name, poll.WebTheme.Active)
		}
	}
}

// The page maps exported role names onto tokens it already defines and holds
// no copy of a monochrome palette: the colors exist in ui/theme only.
func TestPageSkinIsOnlyARoleMapping(t *testing.T) {
	page := string(Page())
	a := strings.Index(page, "const SKIN_VARS = {")
	if a < 0 {
		t.Fatal("the page has no skin table")
	}
	table := page[a : a+strings.Index(page[a:], "};")]
	roles := theme.WebExport(theme.OnyxTheme()).Roles
	pairs := regexp.MustCompile(`'(--[a-z0-9-]+)':'([a-z_0-9]+)'`).FindAllStringSubmatch(table, -1)
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
}

// The terminal is another process: it reaches this one through the API,
// with the token from the address it was handed. This is the real seam: the
// terminal's own poster against this server, then the poll the page makes.
func TestTerminalThemeChangeReachesTheNextStatusPoll(t *testing.T) {
	useTheme(t, "dark")
	srv, _ := startTest(t)
	ctx := context.Background()
	active := func() string {
		t.Helper()
		code, raw := call(t, srv, http.MethodGet, "/api/status", nil, true)
		var poll struct {
			WebTheme theme.WebPayload `json:"web_theme"`
		}
		if code != http.StatusOK || json.Unmarshal(raw, &poll) != nil {
			t.Fatalf("status = %d %s", code, raw)
		}
		return poll.WebTheme.Active
	}

	if err := cli.PostWebTheme(ctx, srv.URL(), "onyx-light"); err != nil {
		t.Fatalf("the terminal's post must be accepted: %v", err)
	}
	if got := active(); got != "onyx-light" {
		t.Fatalf("the next status poll says %q, want onyx-light", got)
	}

	// The token guard covers this route like every other one.
	if err := cli.PostWebTheme(ctx, "http://"+srv.Host()+"/?t=0000", "onyx"); err == nil {
		t.Fatal("a wrong token must be refused")
	}
	// An unknown theme and a broken body are refused and change nothing.
	if err := cli.PostWebTheme(ctx, srv.URL(), "no-such-theme"); err == nil {
		t.Fatal("an unknown theme must be refused")
	}
	if code, _ := call(t, srv, http.MethodPost, "/api/"+cli.WebThemeResource, "not an object", true); code != http.StatusBadRequest {
		t.Fatalf("a broken body = %d, want 400", code)
	}
	if got := active(); got != "onyx-light" {
		t.Fatalf("a refused change moved the theme to %q", got)
	}
}

// The page URL keeps the token, so a reload keeps working; the page must
// never strip it or park it in storage that outlives the tab.
func TestPageKeepsItsTokenAcrossAReload(t *testing.T) {
	page := string(Page())
	if !strings.Contains(page, "new URLSearchParams(location.search).get('t')") {
		t.Fatal("the page must read the token from its address")
	}
	if strings.Contains(page, "history.replaceState") || regexp.MustCompile(`(?i)Storage\.setItem\([^)]*token`).MatchString(page) {
		t.Fatal("the page must leave the token in its address and out of storage")
	}
}
