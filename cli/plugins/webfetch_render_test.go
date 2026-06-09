package plugins

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRenderSession_CooldownGate(t *testing.T) {
	s := &renderSession{cooldownUntil: time.Now().Add(time.Minute)}
	if _, err := s.acquire(); err == nil || !strings.Contains(err.Error(), "suspended") {
		t.Fatalf("acquire during cooldown must fail fast with the suspension message, got %v", err)
	}
}

func TestRenderSession_BreakerTripsAfterThreshold(t *testing.T) {
	s := &renderSession{}
	for i := 0; i < renderFailureThreshold-1; i++ {
		s.registerFailureLocked()
		if !s.cooldownUntil.IsZero() {
			t.Fatalf("breaker must not trip before %d failures", renderFailureThreshold)
		}
	}
	s.registerFailureLocked()
	if time.Until(s.cooldownUntil) <= 0 {
		t.Fatal("breaker must trip after the failure threshold")
	}
	if s.consecutiveFailures != 0 {
		t.Error("failure counter must reset when the breaker trips")
	}
}

func TestRenderSession_MissingBrowserDoesNotTripBreaker(t *testing.T) {
	// A deterministic resolve failure (explicit override pointing nowhere)
	// must keep its actionable message and never trip the breaker — the
	// breaker exists for flaky launches, not for configuration errors.
	t.Setenv("CHATCLI_WEBFETCH_RENDER_BROWSER", "/nonexistent/browser-bin")

	s := &renderSession{}
	for i := 0; i < renderFailureThreshold+1; i++ {
		_, err := s.acquire()
		if err == nil || !strings.Contains(err.Error(), "not accessible") {
			t.Fatalf("expected the explicit-override error, got %v", err)
		}
	}
	if !s.cooldownUntil.IsZero() {
		t.Error("missing-binary errors must not trip the circuit breaker")
	}
}

func TestResolveRenderBrowser_InstallHintWithoutAnyBrowser(t *testing.T) {
	// No override, empty PATH and no fallback hit → the install hint must
	// mention every recovery path the user has.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CHATCLI_WEBFETCH_RENDER_AUTOPROVISION", "")
	prev := fallbackBrowserCandidates
	fallbackBrowserCandidates = nil
	t.Cleanup(func() { fallbackBrowserCandidates = prev })

	_, ok, err := resolveRenderBrowser()
	if ok {
		t.Fatal("expected resolution to fail with nothing installed")
	}
	for _, want := range []string{"RENDER_BROWSER", "AUTOPROVISION"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("install hint must mention %s, got: %v", want, err)
		}
	}
}

func TestResolveRenderBrowser_ExplicitOverrideWins(t *testing.T) {
	fake := t.TempDir() + "/my-chromium"
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHATCLI_WEBFETCH_RENDER_BROWSER", fake)
	bin, ok, err := resolveRenderBrowser()
	if err != nil || !ok || bin != fake {
		t.Fatalf("explicit override must win: bin=%q ok=%v err=%v", bin, ok, err)
	}
}

func TestRenderSession_IdleTimerLifecycle(t *testing.T) {
	s := &renderSession{}
	s.mu.Lock()
	s.touchLocked()
	if s.idle == nil {
		t.Fatal("touch must arm the idle timer")
	}
	s.closeLocked()
	if s.idle != nil {
		t.Error("close must release the idle timer")
	}
	s.mu.Unlock()
}

func TestResolvedHostAllowed_CachesVerdicts(t *testing.T) {
	hostVerdicts.mu.Lock()
	hostVerdicts.entries = map[string]hostVerdict{
		"cached-allowed.test": {allowed: true, expires: time.Now().Add(time.Minute)},
		"cached-blocked.test": {allowed: false, expires: time.Now().Add(time.Minute)},
	}
	hostVerdicts.mu.Unlock()

	if !resolvedHostAllowed("cached-allowed.test") {
		t.Error("fresh allowed verdict must be served from cache")
	}
	if resolvedHostAllowed("cached-blocked.test") {
		t.Error("fresh blocked verdict must be served from cache")
	}

	// Literal IPs bypass the cache entirely (already vetted upstream).
	if !resolvedHostAllowed("203.0.113.10") {
		t.Error("literal IP must not consult the resolver")
	}
}

// jsTablePage builds its table rows client-side — a static fetch sees an
// empty <tbody>; only a real render exposes the cell values.
var jsTablePage = `<!doctype html><html><head><title>fleet</title></head><body>
<div id="root"></div>
<noscript>You need to enable JavaScript to run this app.</noscript>
<script>
const rows = [["sirius-7","healthy"],["vega-3","degraded"]];
const tbl = document.createElement("table");
for (const [name, status] of rows) {
  const tr = tbl.insertRow();
  tr.insertCell().textContent = name;
  tr.insertCell().textContent = status;
}
document.getElementById("root").appendChild(tbl);
</script>` + strings.Repeat("<!-- pad so the shell heuristic sees a real app -->", 60) + `
</body></html>`

func TestRenderPageHTML_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	bin, ok, _ := resolveRenderBrowser()
	if !ok || bin == "" {
		t.Skip("no Chromium-based browser available")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(jsTablePage))
	}))
	defer srv.Close()

	// Sanity: the static path must NOT see the table (that is the gap).
	staticText := extractText(jsTablePage)
	if strings.Contains(staticText, "sirius-7") {
		t.Fatal("test page leaks content statically; it no longer exercises the gap")
	}
	if !looksJSRendered(jsTablePage, staticText) {
		t.Fatal("test page must be detected as a JS shell")
	}

	html, err := renderPageHTML(t.Context(), srv.URL)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	rendered := extractText(html)
	for _, want := range []string{"sirius-7", "healthy", "vega-3", "degraded"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered text missing JS-built cell %q", want)
		}
	}

	// Second render reuses the shared browser (no relaunch) — just assert
	// it still works and the session kept a live instance.
	if _, err := renderPageHTML(t.Context(), srv.URL); err != nil {
		t.Fatalf("second render (browser reuse): %v", err)
	}
	renderShared.mu.Lock()
	alive := renderShared.browser != nil
	renderShared.mu.Unlock()
	if !alive {
		t.Error("shared browser must stay alive between renders (idle window)")
	}
}

func TestResolvedHostAllowed_FailsClosedOnResolutionError(t *testing.T) {
	hostVerdicts.mu.Lock()
	hostVerdicts.entries = map[string]hostVerdict{}
	hostVerdicts.mu.Unlock()

	if resolvedHostAllowed("definitely-not-a-real-host.invalid") {
		t.Error("unresolvable host must be blocked (fail closed)")
	}
}
