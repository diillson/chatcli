/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// docsSiteServer spins up a tiny linked doc site. Page A (/) links to B
// (/b), to an asset (/style.css), to a fragment (#top) and to an external
// host. Page B links back to A (dedup) and to a deeper page C (/c).
func docsSiteServer(t *testing.T, externalHost string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><head><title>Page A</title></head><body>
<h1>Welcome A</h1><p>Alpha content body for the corpus.</p>
<a href="/b">to B</a>
<a href="/style.css">styles</a>
<a href="#top">fragment</a>
<a href="%s/external">external host</a>
</body></html>`, externalHost)
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Page B</title></head><body>
<h1>Welcome B</h1><p>Bravo content body for the corpus.</p>
<a href="/">back to A</a>
<a href="/c">to C</a>
</body></html>`)
	})
	mux.HandleFunc("/c", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Page C</title></head><body>
<h1>Welcome C</h1><p>Charlie content body for the corpus.</p>
</body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCrawlDocsFlatten_StaysSameHostAndDedups(t *testing.T) {
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<html><head><title>External</title></head><body>nope</body></html>`)
	}))
	t.Cleanup(external.Close)

	srv := docsSiteServer(t, external.URL)

	cfg, err := parseDocsFlattenArgs([]string{
		fmt.Sprintf(`{"url":%q,"format":"jsonl"}`, srv.URL),
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var log []string
	emit := func(s string) { log = append(log, s) }

	chunks, pages, capped, err := crawlDocsFlatten(context.Background(), cfg, emit)
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}

	// A + B + C are same-host within depth 2; external host must be excluded.
	if pages != 3 {
		t.Fatalf("expected 3 pages crawled (A,B,C), got %d (log: %v)", pages, log)
	}
	if capped {
		t.Fatalf("crawl should not be capped at defaults, got capped=true")
	}

	sources := map[string]bool{}
	titles := map[string]bool{}
	for _, c := range chunks {
		sources[c.Source] = true
		titles[c.Title] = true
		if c.Source == "" {
			t.Fatalf("chunk has empty Source: %+v", c)
		}
		if c.RepoURL != srv.URL {
			t.Fatalf("chunk RepoURL = %q, want seed %q", c.RepoURL, srv.URL)
		}
	}
	for _, want := range []string{srv.URL + "/b", srv.URL + "/c"} {
		if !sources[want] {
			t.Fatalf("expected source %q among crawled pages, got %v", want, sources)
		}
	}
	for _, badHost := range []string{external.URL + "/external"} {
		if sources[badHost] {
			t.Fatalf("crawl leaked to external host %q", badHost)
		}
	}
	for _, want := range []string{"Page A", "Page B", "Page C"} {
		if !titles[want] {
			t.Fatalf("expected title %q, got %v", want, titles)
		}
	}
}

func TestCrawlDocsFlatten_RespectsMaxPages(t *testing.T) {
	srv := docsSiteServer(t, "http://example.invalid")

	cfg, err := parseDocsFlattenArgs([]string{
		fmt.Sprintf(`{"url":%q,"maxPages":1,"format":"jsonl"}`, srv.URL),
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.MaxPages != 1 {
		t.Fatalf("MaxPages = %d, want 1", cfg.MaxPages)
	}

	chunks, pages, capped, err := crawlDocsFlatten(context.Background(), cfg, func(string) {})
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	if pages != 1 {
		t.Fatalf("expected exactly 1 page with maxPages=1, got %d", pages)
	}
	if !capped {
		t.Fatalf("expected capped=true when maxPages cuts the walk short")
	}
	if len(chunks) == 0 {
		t.Fatalf("expected at least one chunk from the seed page")
	}
}

func TestCrawlDocsFlatten_MaxDepthZeroSeedOnly(t *testing.T) {
	srv := docsSiteServer(t, "http://example.invalid")

	cfg, err := parseDocsFlattenArgs([]string{
		fmt.Sprintf(`{"url":%q,"maxDepth":0,"format":"jsonl"}`, srv.URL),
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	chunks, pages, capped, err := crawlDocsFlatten(context.Background(), cfg, func(string) {})
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	if pages != 1 {
		t.Fatalf("maxDepth=0 should crawl only the seed, got %d pages", pages)
	}
	if !capped {
		t.Fatalf("expected capped=true: links existed below the depth horizon")
	}
	if len(chunks) == 0 {
		t.Fatalf("expected chunks from the seed page")
	}
}

func TestDocsFlattenURL_EndToEndJSONL(t *testing.T) {
	srv := docsSiteServer(t, "http://example.invalid")

	plug := NewBuiltinDocsFlattenPlugin()
	args := fmt.Sprintf(`{"url":%q,"format":"jsonl"}`, srv.URL)
	out, err := plug.Execute(context.Background(), []string{args})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	// jsonl with no output → rendered corpus returned inline. Each line must
	// be a valid docsFlattenChunk with Source and Title set.
	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var c docsFlattenChunk
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("line is not valid JSONL chunk: %v\nline: %s", err, line)
		}
		if c.Source == "" {
			t.Fatalf("chunk missing source: %s", line)
		}
		if c.Title == "" {
			t.Fatalf("chunk missing title: %s", line)
		}
		lines++
	}
	if lines == 0 {
		t.Fatalf("expected at least one JSONL chunk, got none.\noutput: %s", out)
	}
}

func TestParseDocsFlattenArgs_SourcesMutuallyExclusive(t *testing.T) {
	if _, err := parseDocsFlattenArgs([]string{`{"url":"https://x.example/","root":"./docs"}`}); err == nil {
		t.Fatalf("expected error when both url and root are set")
	}
	if _, err := parseDocsFlattenArgs([]string{`{"url":"ftp://x.example/"}`}); err == nil {
		t.Fatalf("expected error for non-http(s) url scheme")
	}
	cfg, err := parseDocsFlattenArgs([]string{`{"url":"https://docs.example.com/","maxPages":7,"maxDepth":3,"sameHost":false}`})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.MaxPages != 7 || cfg.MaxDepth != 3 || cfg.SameHost {
		t.Fatalf("flags not parsed: %+v", cfg)
	}
}

func TestNormalizeDocsFlattenURL(t *testing.T) {
	cases := map[string]string{
		"https://H.example.com/a/":     "https://h.example.com/a",
		"https://h.example.com/a#frag": "https://h.example.com/a",
		"https://h.example.com/":       "https://h.example.com/",
		"https://h.example.com/a?x=1":  "https://h.example.com/a?x=1",
	}
	for in, want := range cases {
		if got := normalizeDocsFlattenURL(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveDocsFlattenLink_SkipsAssetsAndSchemes(t *testing.T) {
	base := "https://docs.example.com/guide/"
	skip := []string{"#top", "mailto:a@b.c", "javascript:void(0)", "/logo.png", "/app.js", "style.css"}
	for _, href := range skip {
		if got := resolveDocsFlattenLink(base, href); got != "" {
			t.Errorf("expected %q to be skipped, got %q", href, got)
		}
	}
	if got := resolveDocsFlattenLink(base, "../intro"); got != "https://docs.example.com/intro" {
		t.Errorf("relative resolve = %q", got)
	}
}
