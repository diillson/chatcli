package plugins

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// braveSERPFixture mirrors the real Brave Search markup shape: Svelte
// content-hashed classes, an llm-snippet block WITHOUT data-type that must
// be skipped, and organic results carrying data-type="web" with the title
// nested inside the anchor and the description as a free text node.
const braveSERPFixture = `<!doctype html><html><body>
<div id="results" class="svelte-e12qt1">
  <div id="llm-snippet" class="snippet noscript-hide streaming svelte-jmfu5f standalone">
    <div class="snippet-content svelte-3n8qdv">An AI generated answer that must never be scraped as an organic result because it has no data-type marker.</div>
  </div>
  <div class="snippet svelte-jmfu5f" data-pos="0" data-type="web">
    <a href="https://pkg.go.dev/context" target="_self" class="svelte-14r20fy l1">
      <div class="site-name-wrapper svelte-on1hvy"><img alt="x" src="favicon.png"/></div>
      <div class="title search-snippet-title line-clamp-1 svelte-14r20fy">context package - context - Go Packages</div>
    </a>
    <div class="generic-text svelte-abc123">Package context defines the Context type, which carries deadlines, cancellation signals, and other request-scoped values.</div>
  </div>
  <div class="snippet svelte-jmfu5f" data-pos="1" data-type="web">
    <a href="https://www.digitalocean.com/community/tutorials/how-to-use-contexts-in-go" class="svelte-14r20fy l1">
      <div class="title search-snippet-title svelte-14r20fy">How To Use Contexts in Go | DigitalOcean</div>
    </a>
    <div class="generic-text">In this tutorial, you will start by creating a Go program that uses a context within a function.</div>
  </div>
  <div class="snippet svelte-jmfu5f" data-pos="2" data-type="videos">
    <a href="https://example.com/video"><div class="title">A video card that is not an organic web hit</div></a>
    <div>Video carousels must not pollute the organic result list returned to the model.</div>
  </div>
</div>
</body></html>`

func TestParseBraveResults(t *testing.T) {
	results := parseBraveResults(braveSERPFixture, 10)
	if len(results) != 2 {
		t.Fatalf("expected 2 organic results, got %d: %+v", len(results), results)
	}
	first := results[0]
	if first.URL != "https://pkg.go.dev/context" {
		t.Errorf("URL = %q", first.URL)
	}
	if first.Title != "context package - context - Go Packages" {
		t.Errorf("Title = %q", first.Title)
	}
	if !strings.Contains(first.Snippet, "carries deadlines") {
		t.Errorf("Snippet = %q", first.Snippet)
	}
	if results[1].Title != "How To Use Contexts in Go | DigitalOcean" {
		t.Errorf("second Title = %q", results[1].Title)
	}
}

func TestParseBraveResults_MaxAndGarbage(t *testing.T) {
	if got := parseBraveResults(braveSERPFixture, 1); len(got) != 1 {
		t.Errorf("maxResults=1 must clip, got %d", len(got))
	}
	if got := parseBraveResults("not html at all", 5); len(got) != 0 {
		t.Errorf("garbage input must yield no results, got %d", len(got))
	}
}

// mojeekSERPFixture mirrors Mojeek's classic results markup.
const mojeekSERPFixture = `<!doctype html><html><body>
<ul class="results-standard">
  <li>
    <h2><a href="https://go.dev/blog/context">Go Concurrency Patterns: Context</a></h2>
    <p class="s">In Go servers, each incoming request is handled in its own goroutine.</p>
    <a class="ob" href="https://www.mojeek.com/redirect">open</a>
  </li>
  <li>
    <h2><a href="https://pkg.go.dev/context">context package</a></h2>
    <p class="s">Package context defines the Context type.</p>
  </li>
  <li><p class="s">A malformed entry with no heading link must be skipped.</p></li>
</ul>
</body></html>`

func TestParseMojeekResults(t *testing.T) {
	results := parseMojeekResults(mojeekSERPFixture, 10)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(results), results)
	}
	if results[0].URL != "https://go.dev/blog/context" {
		t.Errorf("URL = %q", results[0].URL)
	}
	if results[0].Title != "Go Concurrency Patterns: Context" {
		t.Errorf("Title = %q", results[0].Title)
	}
	if !strings.Contains(results[0].Snippet, "own goroutine") {
		t.Errorf("Snippet = %q", results[0].Snippet)
	}
}

func TestParseMojeekResults_MaxAndGarbage(t *testing.T) {
	if got := parseMojeekResults(mojeekSERPFixture, 1); len(got) != 1 {
		t.Errorf("maxResults=1 must clip, got %d", len(got))
	}
	if got := parseMojeekResults("<html><body>nothing here</body></html>", 5); len(got) != 0 {
		t.Errorf("page without results-standard must yield none, got %d", len(got))
	}
}

func TestFetchSearchHTML_ErrorPaths(t *testing.T) {
	// Non-200 surfaces as an error so the chain falls through (a Mojeek
	// 403 interstitial must not read as "zero results, stop here").
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	if _, err := fetchSearchHTML(t.Context(), srv.URL+"/search?q=", "golang"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("403 must surface as an error, got %v", err)
	}

	// Metadata endpoint refused by the SSRF guard before any request.
	if _, err := fetchSearchHTML(t.Context(), "http://169.254.169.254/?q=", "x"); err == nil {
		t.Error("metadata endpoint must be refused")
	}
}

func TestSearchBraveAndMojeek_EndToEnd(t *testing.T) {
	// Serve the fixtures over HTTP and point the parsers at them through
	// the full provider functions, swapping the endpoints via the shared
	// fetch path (the providers differ only in endpoint + parser).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if strings.Contains(r.URL.RawQuery, "brave") {
			_, _ = w.Write([]byte(braveSERPFixture))
			return
		}
		_, _ = w.Write([]byte(mojeekSERPFixture))
	}))
	defer srv.Close()

	body, err := fetchSearchHTML(t.Context(), srv.URL+"/search?q=", "brave golang")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := parseBraveResults(body, 5); len(got) != 2 {
		t.Errorf("brave end-to-end: got %d results", len(got))
	}

	body, err = fetchSearchHTML(t.Context(), srv.URL+"/search?q=", "mojeek golang")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := parseMojeekResults(body, 5); len(got) != 2 {
		t.Errorf("mojeek end-to-end: got %d results", len(got))
	}
}
