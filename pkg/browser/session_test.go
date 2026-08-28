/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * session_test.go
 *
 * Pure units (selector resolution, binary discovery, headless toggle) plus a
 * real end-to-end pass against a locally installed Chromium-family browser —
 * skipped cleanly where none exists. The e2e page is a data: URL, so no
 * network is touched.
 */
package browser

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveSelector(t *testing.T) {
	cases := map[string]string{
		"3":       `[data-chatcli-ref="3"]`,
		"[12]":    `[data-chatcli-ref="12"]`,
		"#submit": "#submit",
		"a.nav":   "a.nav",
		"":        "",
	}
	for in, want := range cases {
		if got := resolveSelector(in); got != want {
			t.Fatalf("resolveSelector(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLocateChrome_EnvOverride(t *testing.T) {
	t.Setenv(BinEnv, "/definitely/not/a/browser")
	if _, err := locateChrome(); err == nil {
		t.Fatal("bogus CHATCLI_BROWSER_BIN must error, not fall through to discovery")
	}
}

func TestHeadlessEnabled(t *testing.T) {
	t.Setenv(HeadlessEnv, "")
	if !headlessEnabled() {
		t.Fatal("headless must default to on")
	}
	t.Setenv(HeadlessEnv, "false")
	if headlessEnabled() {
		t.Fatal("CHATCLI_BROWSER_HEADLESS=false must disable headless")
	}
}

// e2ePage is the self-contained page the integration test drives.
const e2ePage = `<html><head><title>ChatCLI E2E</title></head><body>
<h1>Hello</h1>
<a href="#next" id="lnk">Next page</a>
<input id="q" type="text" placeholder="query">
<button id="go" onclick="document.title='Clicked'; console.log('clicked go')">Go</button>
<script>console.error('boot error 42');</script>
</body></html>`

func newE2ESession(t *testing.T) *Session {
	t.Helper()
	if _, err := locateChrome(); err != nil {
		t.Skipf("no local Chromium-family browser: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	s, err := NewSession(ctx)
	if err != nil {
		t.Skipf("browser failed to launch in this environment: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestSessionEndToEnd(t *testing.T) {
	s := newE2ESession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	dataURL := "data:text/html," + url.PathEscape(e2ePage)
	title, _, err := s.Navigate(ctx, dataURL)
	if err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if title != "ChatCLI E2E" {
		t.Fatalf("unexpected title %q", title)
	}

	snap, err := s.Snapshot(ctx, 0)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	for _, want := range []string{"ChatCLI E2E", "Next page", "<button> Go", "Hello"} {
		if !strings.Contains(snap, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, snap)
		}
	}

	// Type into the input by its snapshot ref (the input is stamped).
	if err := s.Type(ctx, "#q", "golang", false); err != nil {
		t.Fatalf("type: %v", err)
	}
	got, err := s.Eval(ctx, `document.querySelector('#q').value`)
	if err != nil || got != "golang" {
		t.Fatalf("typed value = %q (err %v)", got, err)
	}

	// Click the button via CSS selector and observe its effect.
	if err := s.Click(ctx, "#go"); err != nil {
		t.Fatalf("click: %v", err)
	}
	title, _, err = s.Identity(ctx)
	if err != nil || title != "Clicked" {
		t.Fatalf("click effect not observed: title=%q err=%v", title, err)
	}

	// Console capture: the page's boot error and the click log.
	deadline := time.Now().Add(5 * time.Second)
	var joined string
	for time.Now().Before(deadline) {
		entries := s.ConsoleTail(0)
		var parts []string
		for _, e := range entries {
			parts = append(parts, e.Kind+":"+e.Text)
		}
		joined = strings.Join(parts, "\n")
		if strings.Contains(joined, "boot error 42") && strings.Contains(joined, "clicked go") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(joined, "boot error 42") || !strings.Contains(joined, "clicked go") {
		t.Fatalf("console capture incomplete:\n%s", joined)
	}

	// Screenshot lands as a real PNG.
	shot := filepath.Join(t.TempDir(), "shot.png")
	if err := s.Screenshot(ctx, shot); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	data, err := os.ReadFile(shot)
	if err != nil || len(data) < 8 || string(data[1:4]) != "PNG" {
		t.Fatalf("screenshot is not a PNG (%d bytes, err %v)", len(data), err)
	}

	// Click by ref: resolve the link's stamped ref from the snapshot.
	if err := s.Click(ctx, refOf(t, snap, "Next page")); err != nil {
		t.Fatalf("click by ref: %v", err)
	}
}

// refOf extracts the [n] ref of the snapshot line containing label.
func refOf(t *testing.T, snapshot, label string) string {
	t.Helper()
	for _, line := range strings.Split(snapshot, "\n") {
		if strings.Contains(line, label) && strings.HasPrefix(line, "[") {
			end := strings.Index(line, "]")
			if end > 1 {
				return line[1:end]
			}
		}
	}
	t.Fatalf("no snapshot line with %q", label)
	return ""
}
