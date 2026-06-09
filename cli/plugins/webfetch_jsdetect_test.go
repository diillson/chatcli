package plugins

import (
	"strings"
	"testing"
)

func spaShell(marker string) string {
	return `<!doctype html><html><head><title>App</title>
<script src="/static/js/main.8f3a.js"></script></head>
<body>` + marker + `<div id="root"></div>
<noscript>You need to enable JavaScript to run this app.</noscript>
` + strings.Repeat("<script>/* bundle chunk */</script>", 80) + `
</body></html>`
}

func TestLooksJSRendered_SPAShell(t *testing.T) {
	html := spaShell("")
	text := extractText(html)
	if !looksJSRendered(html, text) {
		t.Error("empty-root SPA shell with noscript warning must be flagged")
	}
}

func TestLooksJSRendered_FrameworkMarkers(t *testing.T) {
	for _, marker := range []string{
		`<script id="__NEXT_DATA__" type="application/json">{}</script>`,
		`<div data-reactroot=""></div>`,
		`<app-root ng-version="17.0.0"></app-root>`,
		`<script>window.__NUXT__={};</script>`,
	} {
		html := spaShell(marker)
		if !looksJSRendered(html, "thin") {
			t.Errorf("marker %q must flag the page as JS-rendered", marker)
		}
	}
}

func TestLooksJSRendered_StaticPageNotFlagged(t *testing.T) {
	// Real article: plenty of extracted text → never escalate, regardless
	// of any incidental script tags.
	body := strings.Repeat("<p>Real server-rendered paragraph with content.</p>\n", 40)
	html := "<html><body>" + body + "</body></html>"
	if looksJSRendered(html, extractText(html)) {
		t.Error("text-rich page must not be flagged")
	}
}

func TestLooksJSRendered_TinyPageNotFlagged(t *testing.T) {
	html := `<html><body>ok</body></html>`
	if looksJSRendered(html, "ok") {
		t.Error("tiny page (healthz-style) must not be flagged")
	}
}

func TestLooksJSRendered_ThinButNoSignals(t *testing.T) {
	// Thin text but zero SPA signals (e.g. a big HTML page of empty divs)
	// must not boot a browser.
	html := "<html><body>" + strings.Repeat("<div class='sp'></div>", 300) + "short</body></html>"
	if looksJSRendered(html, "short") {
		t.Error("thin page without any JS-framework signal must not be flagged")
	}
}

func TestExtractEmbeddedState_NextData(t *testing.T) {
	state := `{"props":{"pageProps":{"rows":[{"name":"alpha","value":42}]}},"page":"/table"}`
	html := `<html><body><div id="__next"></div>
<script id="__NEXT_DATA__" type="application/json">` + state + `</script></body></html>`
	got, ok := extractEmbeddedState(html)
	if !ok {
		t.Fatal("expected __NEXT_DATA__ to be recovered")
	}
	if !strings.Contains(got, `"rows"`) || !strings.Contains(got, `"alpha"`) {
		t.Errorf("recovered state lost payload: %q", got)
	}
}

func TestExtractEmbeddedState_AbsentOrInvalid(t *testing.T) {
	if _, ok := extractEmbeddedState("<html><body>plain</body></html>"); ok {
		t.Error("no marker → no state")
	}
	html := `<script id="__NEXT_DATA__" type="application/json">not-json</script>`
	if _, ok := extractEmbeddedState(html); ok {
		t.Error("non-JSON body → no state")
	}
}

func TestExtractEmbeddedState_TruncatesHugeState(t *testing.T) {
	state := `{"big":"` + strings.Repeat("x", maxEmbeddedStateBytes) + `"}`
	html := `<script id="__NEXT_DATA__" type="application/json">` + state + `</script>`
	got, ok := extractEmbeddedState(html)
	if !ok {
		t.Fatal("expected state")
	}
	if len(got) > maxEmbeddedStateBytes+100 {
		t.Errorf("state not truncated: %d bytes", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("truncation must be announced in the payload")
	}
}

func TestEffectiveRenderMode_Precedence(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_RENDER", "never")
	if got := effectiveRenderMode(renderModeAlways); got != renderModeAlways {
		t.Errorf("per-call always must beat env never, got %q", got)
	}
	if got := effectiveRenderMode(""); got != renderModeNever {
		t.Errorf("empty call flag must defer to env, got %q", got)
	}
	t.Setenv("CHATCLI_WEBFETCH_RENDER", "definitely-a-typo")
	if got := effectiveRenderMode(""); got != renderModeAuto {
		t.Errorf("unknown env value must collapse to auto, got %q", got)
	}
}

func TestWebRenderTimeout(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_RENDER_TIMEOUT", "7")
	if got := webRenderTimeout(); got.Seconds() != 7 {
		t.Errorf("timeout = %v, want 7s", got)
	}
	t.Setenv("CHATCLI_WEBFETCH_RENDER_TIMEOUT", "garbage")
	if got := webRenderTimeout(); got != defaultRenderTimeout {
		t.Errorf("garbage timeout must fall back to default, got %v", got)
	}
}

func TestRenderRequestAllowed_Policy(t *testing.T) {
	// Non-HTTP schemes never leave the renderer.
	if !renderRequestAllowed("data:image/png;base64,AAAA") {
		t.Error("data: URLs must be allowed")
	}
	// Cloud metadata is always blocked.
	if renderRequestAllowed("http://169.254.169.254/latest/meta-data/") {
		t.Error("metadata IP must be blocked")
	}
	if renderRequestAllowed("http://metadata.google.internal/computeMetadata/v1/") {
		t.Error("metadata hostname must be blocked")
	}
	// Public HTTPS passes the upfront policy.
	if !renderRequestAllowed("https://example.com/app.js") {
		t.Error("public https sub-request must be allowed")
	}
	// Garbage URL fails closed.
	if renderRequestAllowed("http://%zz") {
		t.Error("unparsable URL must be blocked")
	}
}

func TestRenderRequestAllowed_BlockPrivate(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "true")
	if renderRequestAllowed("http://127.0.0.1:8080/internal") {
		t.Error("loopback literal must be blocked in hardened mode")
	}
	if renderRequestAllowed("http://localhost:8080/internal") {
		t.Error("localhost must resolve to loopback and be blocked in hardened mode")
	}
}

func TestParseFetchArgs_RenderFlag(t *testing.T) {
	// JSON boolean form.
	out, err := parseFetchArgs([]string{`{"url":"https://x.dev","render":true}`})
	if err != nil || out.Render != renderModeAlways {
		t.Errorf("render:true → always, got %q (%v)", out.Render, err)
	}
	out, _ = parseFetchArgs([]string{`{"url":"https://x.dev","render":false}`})
	if out.Render != renderModeNever {
		t.Errorf("render:false → never, got %q", out.Render)
	}
	// JSON string form.
	out, _ = parseFetchArgs([]string{`{"url":"https://x.dev","render":"always"}`})
	if out.Render != renderModeAlways {
		t.Errorf("render:\"always\" → always, got %q", out.Render)
	}
	// Positional forms.
	out, _ = parseFetchArgs([]string{"fetch", "--url", "https://x.dev", "--render"})
	if out.Render != renderModeAlways {
		t.Errorf("--render → always, got %q", out.Render)
	}
	out, _ = parseFetchArgs([]string{"fetch", "--url", "https://x.dev", "--no-render"})
	if out.Render != renderModeNever {
		t.Errorf("--no-render → never, got %q", out.Render)
	}
}

func TestExtractWithRenderEscalation_NeverAndStatic(t *testing.T) {
	p := NewBuiltinWebFetchPlugin()
	html := spaShell("")

	// mode=never → static text untouched even for an obvious shell.
	t.Setenv("CHATCLI_WEBFETCH_RENDER", "never")
	got := p.extractWithRenderEscalation(t.Context(), fetchArgs{}, "https://x.dev", html, nil)
	if strings.Contains(got, "client-side") {
		t.Error("never mode must not annotate or escalate")
	}

	// Static page in auto mode → no escalation, plain extraction.
	t.Setenv("CHATCLI_WEBFETCH_RENDER", "auto")
	staticHTML := "<html><body>" + strings.Repeat("<p>real content here</p>", 60) + "</body></html>"
	got = p.extractWithRenderEscalation(t.Context(), fetchArgs{}, "https://x.dev", staticHTML, nil)
	if strings.Contains(got, "client-side") || !strings.Contains(got, "real content") {
		t.Error("static page must take the plain extraction path")
	}
}

func TestExtractWithRenderEscalation_FallbackToEmbeddedState(t *testing.T) {
	// Force a deterministic resolve failure (override pointing nowhere) so
	// no browser launches and the embedded __NEXT_DATA__ fallback kicks in.
	t.Setenv("CHATCLI_WEBFETCH_RENDER_BROWSER", "/nonexistent/browser-bin")
	t.Setenv("CHATCLI_WEBFETCH_RENDER", "auto")

	p := NewBuiltinWebFetchPlugin()
	state := `{"props":{"pageProps":{"rows":[1,2,3]}}}`
	html := spaShell(`<script id="__NEXT_DATA__" type="application/json">` + state + `</script>`)

	var notes []string
	got := p.extractWithRenderEscalation(t.Context(), fetchArgs{}, "https://x.dev", html, func(s string) {
		notes = append(notes, s)
	})
	if !strings.Contains(got, `"rows"`) {
		t.Errorf("embedded state must be surfaced when no browser exists, got: %q", got)
	}
	if len(notes) == 0 {
		t.Error("escalation attempt must be narrated via onOutput")
	}
}

func TestExtractWithRenderEscalation_HonestNoteWithoutAnyFallback(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_RENDER_BROWSER", "/nonexistent/browser-bin")
	t.Setenv("CHATCLI_WEBFETCH_RENDER", "auto")

	p := NewBuiltinWebFetchPlugin()
	html := spaShell("") // shell without __NEXT_DATA__
	got := p.extractWithRenderEscalation(t.Context(), fetchArgs{}, "https://x.dev", html, nil)
	if !strings.Contains(got, "client-side") {
		t.Error("must annotate the limitation when neither render nor state are available")
	}
}
