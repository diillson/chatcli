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
	t.Cleanup(func() { s.Close(context.Background()) })
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

// TestRelaunchKeepsProfileAndPage exercises the visibility flip machinery
// against a real browser without opening a window: relaunching headless →
// headless still proves the profile directory is reused (not deleted) and
// the page is restored.
func TestRelaunchKeepsProfileAndPage(t *testing.T) {
	first := newE2ESession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dataURL := "data:text/html," + url.PathEscape(e2ePage)
	if _, _, err := first.Navigate(ctx, dataURL); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	dir := first.ProfileDir()
	if dir == "" || !first.ephemeral {
		t.Fatalf("e2e session must own a throwaway profile, got %q", dir)
	}

	second, err := relaunch(ctx, first, true)
	if err != nil {
		t.Fatalf("relaunch: %v", err)
	}
	t.Cleanup(func() { second.Close(context.Background()) })
	if second.ProfileDir() != dir {
		t.Fatalf("relaunch must reuse the profile dir: %q vs %q", second.ProfileDir(), dir)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("profile dir must survive the flip: %v", err)
	}
	if !second.ephemeral {
		t.Fatal("throwaway ownership must transfer to the new session")
	}
	title, _, err := second.Identity(ctx)
	if err != nil || title != "ChatCLI E2E" {
		t.Fatalf("page must be restored after relaunch: title=%q err=%v", title, err)
	}
	if first.Alive() {
		t.Fatal("previous session must be closed")
	}
	second.Close(ctx)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("throwaway profile must be removed on final close, stat err=%v", err)
	}
}

// e2ePageV2 exercises what the DOM scan must see through: an open shadow
// root, a same-origin iframe, a labeled password field, an editable
// region and a cross-origin-looking frame the scan cannot read.
const e2ePageV2 = `<html><head><title>ChatCLI DOM v2</title></head><body>
<label for="pw">Secret phrase</label><input id="pw" type="password">
<input id="name" type="text" placeholder="Your name">
<div id="editor" contenteditable="true">draft</div>
<div id="host"></div>
<iframe id="inner" srcdoc="<button id='deep' onclick='parent.document.title=&quot;FrameClicked&quot;'>Deep button</button><p>inside frame text</p>"></iframe>
<p>Alpha paragraph with the word needle in it.</p><p>Beta paragraph, another needle here.</p>
<script>
  const root = document.getElementById('host').attachShadow({mode: 'open'});
  root.innerHTML = '<button id="shadowbtn" onclick="document.title=\'ShadowClicked\'">Shadow button</button>';
  document.getElementById('pw').value = 'hun' + 'ter2';
  let counter = 0;
  document.getElementById('editor').addEventListener('input', () => { counter++; document.title = 'Edited' + counter; });
</script>
</body></html>`

func TestSessionEndToEnd_DOMv2(t *testing.T) {
	s := newE2ESession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, _, err := s.Navigate(ctx, "data:text/html,"+url.PathEscape(e2ePageV2)); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // srcdoc frame and shadow script settle

	snap, err := s.Snapshot(ctx, 0)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if strings.Contains(snap, "hunter2") {
		t.Fatalf("password value leaked into the snapshot:\n%s", snap)
	}
	for _, want := range []string{"<input> Secret phrase type=password value=<hidden>", "<input> Your name type=text", "<div> draft editable", "<button> Shadow button", "<button> Deep button", "in iframe", "inside frame text"} {
		if !strings.Contains(snap, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, snap)
		}
	}

	// A button inside an open shadow root is clickable by its ref, with a
	// real mouse click.
	note, err := s.ClickWith(ctx, refOf(t, snap, "Shadow button"), ClickOptions{})
	if err != nil || note != "" {
		t.Fatalf("shadow click: %q %v", note, err)
	}
	if title, _, _ := s.Identity(ctx); title != "ShadowClicked" {
		t.Fatalf("shadow click had no effect: title=%q", title)
	}
	// So is one inside a same-origin iframe.
	if _, err := s.ClickWith(ctx, refOf(t, snap, "Deep button"), ClickOptions{}); err != nil {
		t.Fatalf("iframe click: %v", err)
	}
	if title, _, _ := s.Identity(ctx); title != "FrameClicked" {
		t.Fatalf("iframe click had no effect: title=%q", title)
	}

	// Typing into an editable region goes through the input pipeline, so
	// the page's input listener fires; the previous text is replaced.
	if _, err := s.TypeWith(ctx, "#editor", "final text", false, false); err != nil {
		t.Fatalf("type editable: %v", err)
	}
	got, _ := s.Eval(ctx, `document.getElementById('editor').innerText`)
	if strings.TrimSpace(got) != "final text" {
		t.Fatalf("editable content = %q", got)
	}
	if title, _, _ := s.Identity(ctx); !strings.HasPrefix(title, "Edited") {
		t.Fatalf("input event did not reach the page: title=%q", title)
	}
	if _, err := s.TypeWith(ctx, "#editor", " +more", true, false); err != nil {
		t.Fatalf("append editable: %v", err)
	}
	got, _ = s.Eval(ctx, `document.getElementById('editor').innerText`)
	if strings.TrimSpace(got) != "final text +more" {
		t.Fatalf("appended content = %q", got)
	}
	if err := s.Clear(ctx, "#editor"); err != nil {
		t.Fatalf("clear editable: %v", err)
	}
	if got, _ = s.Eval(ctx, `document.getElementById('editor').innerText`); strings.TrimSpace(got) != "" {
		t.Fatalf("editable not cleared: %q", got)
	}

	// A password field is reported so the caller never echoes the text.
	pw, err := s.TypeWith(ctx, "#pw", "s3cret", false, false)
	if err != nil || !pw {
		t.Fatalf("password report: %v %v", pw, err)
	}
	if _, err := s.TypeWith(ctx, "#name", "Ada", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TypeWith(ctx, "#name", " Lovelace", true, false); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Eval(ctx, `document.getElementById('name').value`); got != "Ada Lovelace" {
		t.Fatalf("append on input = %q", got)
	}
	if err := s.Clear(ctx, "#name"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Eval(ctx, `document.getElementById('name').value`); got != "" {
		t.Fatalf("input not cleared: %q", got)
	}

	// Eval tolerates a redeclaration across calls (REPL mode) and awaits.
	if _, err := s.Eval(ctx, `const answer = 40; answer`); err != nil {
		t.Fatalf("first const: %v", err)
	}
	if got, err := s.Eval(ctx, `const answer = 42; answer`); err != nil || got != "42" {
		t.Fatalf("second const must not fail with 'already declared': %q %v", got, err)
	}
	if got, err := s.Eval(ctx, `await new Promise(r => setTimeout(() => r('later'), 10))`); err != nil || got != "later" {
		t.Fatalf("top-level await: %q %v", got, err)
	}

	// find lists matching elements and text occurrences; text pages.
	out, err := s.Find(ctx, "needle", 10)
	if err != nil || !strings.Contains(out, "Text matches for \"needle\" (2 shown of 2)") {
		t.Fatalf("find: %q %v", out, err)
	}
	out, err = s.Find(ctx, "deep button", 10)
	if err != nil || !strings.Contains(out, "<button> Deep button") {
		t.Fatalf("find element: %q %v", out, err)
	}
	full, err := s.Text(ctx, "", 0, 0)
	if err != nil || !strings.Contains(full, "Alpha paragraph") || !strings.Contains(full, "end of page") {
		t.Fatalf("text: %q %v", full, err)
	}
	part, err := s.Text(ctx, "", 0, 20)
	if err != nil || !strings.Contains(part, "`--offset 20` continues") {
		t.Fatalf("text paging: %q %v", part, err)
	}
	if ch, err := s.Challenge(ctx); err != nil || ch != "" {
		t.Fatalf("plain page reported as challenge %q (%v)", ch, err)
	}

	// An interstitial is recognized and the snapshot says so.
	block := `<html><head><title>Just a moment...</title></head><body><div id="challenge-running">Checking your browser before accessing example.com.</div></body></html>`
	if _, _, err := s.Navigate(ctx, "data:text/html,"+url.PathEscape(block)); err != nil {
		t.Fatalf("navigate block: %v", err)
	}
	if ch, err := s.Challenge(ctx); err != nil || ch != "Cloudflare" {
		t.Fatalf("challenge = %q (%v)", ch, err)
	}
	snap, _ = s.Snapshot(ctx, 0)
	if !strings.Contains(snap, "Bot challenge detected (Cloudflare)") {
		t.Fatalf("snapshot must warn:\n%s", snap)
	}
}
