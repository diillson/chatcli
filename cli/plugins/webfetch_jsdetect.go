/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * JS-shell detection for @webfetch.
 *
 * SPAs ship an HTML "shell" (an empty root div plus script tags) and only
 * materialize content client-side. A static fetch of such a page yields a
 * few words of boilerplate — the model then concludes the page is empty.
 * The heuristics here decide when that happened so the plugin can escalate
 * to a headless render (webfetch_render.go), and extract framework state
 * embedded in the static HTML when it is available for free.
 */
package plugins

import (
	"regexp"
	"strings"
)

const (
	// jsShellTextFloor: below this many characters of extracted text a page
	// is "thin". Real articles rarely extract to less; SPA shells almost
	// always do (nav links + a noscript warning).
	jsShellTextFloor = 400
	// jsShellMinHTML: shells under this raw size are more likely a genuinely
	// tiny page (healthz endpoints, redirect stubs) than a JS app.
	jsShellMinHTML = 2_000
	// maxEmbeddedStateBytes caps how much framework state JSON is returned —
	// Next.js dehydrated stores can reach megabytes.
	maxEmbeddedStateBytes = 60_000
)

// jsFrameworkMarkers are substrings whose presence in the raw HTML strongly
// indicates a client-rendered application.
var jsFrameworkMarkers = []string{
	"__NEXT_DATA__",         // Next.js
	"data-reactroot",        // React (legacy hydrate)
	"data-reactid",          // React (very legacy)
	"ng-version=",           // Angular
	"data-v-app",            // Vue 3
	"window.__NUXT__",       // Nuxt
	"data-sveltekit",        // SvelteKit
	"id=\"___gatsby\"",      // Gatsby
	"flutter_bootstrap",     // Flutter web
	"window.__remixContext", // Remix
}

// emptyRootPattern matches the canonical SPA mount points when they have no
// server-rendered children: <div id="root"></div>, <div id="app"> </div>,
// <div id="__next"></div> and friends.
var emptyRootPattern = regexp.MustCompile(
	`(?is)<div[^>]*\bid=["'](?:root|app|__next|___gatsby|q-app|main-app)["'][^>]*>\s*</div>`)

// noscriptWarnPattern matches the standard "you need JavaScript" fallback.
var noscriptWarnPattern = regexp.MustCompile(
	`(?is)<noscript>[^<]*(?:enable\s+javascript|javascript\s+(?:is\s+)?(?:required|disabled)|precisa\s+(?:de\s+)?javascript)`)

// looksJSRendered reports whether the static fetch most likely returned an
// SPA shell instead of real content: the extracted text is thin while the
// raw HTML is not, AND the page carries at least one structural signal of
// client-side rendering. Both conditions are required so that genuinely
// small pages (status endpoints) and big-but-static pages never escalate.
func looksJSRendered(rawHTML, extractedText string) bool {
	if len(rawHTML) < jsShellMinHTML {
		return false
	}
	if len(strings.TrimSpace(extractedText)) >= jsShellTextFloor {
		return false
	}
	if noscriptWarnPattern.MatchString(rawHTML) || emptyRootPattern.MatchString(rawHTML) {
		return true
	}
	for _, marker := range jsFrameworkMarkers {
		if strings.Contains(rawHTML, marker) {
			return true
		}
	}
	return false
}

// nextDataPattern captures the JSON body of the Next.js dehydrated store.
// It is a plain <script type="application/json"> so the content is data,
// not code — safe to extract textually.
var nextDataPattern = regexp.MustCompile(
	`(?is)<script[^>]*\bid=["']__NEXT_DATA__["'][^>]*>(.*?)</script>`)

// extractEmbeddedState pulls framework-embedded page state out of the raw
// HTML when present, so JS-rendered content can be recovered without a
// browser. Currently supports the Next.js __NEXT_DATA__ JSON store (the
// most common case by far). Returns ok=false when nothing usable exists.
func extractEmbeddedState(rawHTML string) (string, bool) {
	m := nextDataPattern.FindStringSubmatch(rawHTML)
	if m == nil {
		return "", false
	}
	state := strings.TrimSpace(m[1])
	if state == "" || !strings.HasPrefix(state, "{") {
		return "", false
	}
	if len(state) > maxEmbeddedStateBytes {
		state = state[:maxEmbeddedStateBytes] + "\n…(embedded state truncated)"
	}
	return state, true
}
