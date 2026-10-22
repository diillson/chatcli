package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSelectSearchChainFromEnv(t *testing.T) {
	cases := []struct {
		name     string
		override string
		searxng  string
		want     []SearchProvider
	}{
		{"no override, no searxng", "", "", []SearchProvider{ProviderDuckDuckGo, ProviderBrave, ProviderMojeek}},
		{"no override, searxng set → DDG first, SX fallback", "", "https://sx.example", []SearchProvider{ProviderDuckDuckGo, ProviderSearXNG, ProviderBrave, ProviderMojeek}},
		{"auto override = default (DDG first)", "auto", "https://sx.example", []SearchProvider{ProviderDuckDuckGo, ProviderSearXNG, ProviderBrave, ProviderMojeek}},
		{"explicit ddg, no searxng", "duckduckgo", "", []SearchProvider{ProviderDuckDuckGo, ProviderBrave, ProviderMojeek}},
		{"explicit ddg, with searxng (ddg first, sx follows)", "duckduckgo", "https://sx.example", []SearchProvider{ProviderDuckDuckGo, ProviderSearXNG, ProviderBrave, ProviderMojeek}},
		{"explicit searxng, with url (sx first, ddg follows)", "searxng", "https://sx.example", []SearchProvider{ProviderSearXNG, ProviderDuckDuckGo, ProviderBrave, ProviderMojeek}},
		{"explicit searxng, no url → chain without sx", "searxng", "", []SearchProvider{ProviderDuckDuckGo, ProviderBrave, ProviderMojeek}},
		{"unknown override → fallback to default (DDG first)", "bogus", "https://sx.example", []SearchProvider{ProviderDuckDuckGo, ProviderSearXNG, ProviderBrave, ProviderMojeek}},
		{"explicit brave moves to front", "brave", "https://sx.example", []SearchProvider{ProviderBrave, ProviderDuckDuckGo, ProviderSearXNG, ProviderMojeek}},
		{"explicit mojeek moves to front", "mojeek", "", []SearchProvider{ProviderMojeek, ProviderDuckDuckGo, ProviderBrave}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chain := selectSearchChainFromEnv(tc.override, tc.searxng)
			got := make([]SearchProvider, 0, len(chain))
			for _, p := range chain {
				got = append(got, p.name)
			}
			if !equalSliceProviders(got, tc.want) {
				t.Errorf("selectSearchChainFromEnv(%q, %q) = %v, want %v", tc.override, tc.searxng, got, tc.want)
			}
		})
	}
}

func equalSliceProviders(a, b []SearchProvider) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSearchSearxNG_JSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("format"); got != "json" {
			t.Errorf("expected format=json, got %q", got)
		}
		if got := r.URL.Query().Get("q"); got != "go context" {
			t.Errorf("expected q=go context, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(searxngResponse{
			Results: []struct {
				Title   string `json:"title"`
				URL     string `json:"url"`
				Content string `json:"content"`
			}{
				{Title: "Go context", URL: "https://pkg.go.dev/context", Content: "Package context defines..."},
				{Title: "Effective Go", URL: "https://go.dev/doc/effective_go", Content: "Tips"},
			},
		})
	}))
	defer srv.Close()

	results, err := searchSearxNG(context.Background(), "go context", 5, srv.URL)
	if err != nil {
		t.Fatalf("searchSearxNG error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Title != "Go context" || results[0].URL != "https://pkg.go.dev/context" {
		t.Errorf("first result mismatch: %+v", results[0])
	}
}

// SearxNG served HTML because admin forgot to enable json format — we
// should return an actionable error instead of a cryptic decode failure.
func TestSearchSearxNG_HTMLFallbackIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body>Results page</body></html>"))
	}))
	defer srv.Close()

	_, err := searchSearxNG(context.Background(), "q", 5, srv.URL)
	if err == nil {
		t.Fatal("expected error when instance returns HTML instead of JSON")
	}
	if !strings.Contains(err.Error(), "JSON") || !strings.Contains(err.Error(), "settings.yml") {
		t.Errorf("expected actionable error mentioning JSON/settings.yml, got: %v", err)
	}
}

func TestSearchSearxNG_TrailingSlashNormalized(t *testing.T) {
	got := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	_, err := searchSearxNG(context.Background(), "q", 5, srv.URL+"/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/search" {
		t.Errorf("expected /search path, got %q (double-slash trimming failed)", got)
	}
}

// A JSON payload is never searched as text: a missing query is an empty
// query, and the query may come under any of its usual names.
func TestParseWebSearchArgs_JSONShapes(t *testing.T) {
	cases := []struct {
		args  []string
		query string
		max   int
	}{
		{[]string{`{"cmd":"search","args":{"query":"golang context","maxResults":5}}`}, "golang context", 5},
		{[]string{`{"cmd":"search","args":{"q":"golang context","max_results":3}}`}, "golang context", 3},
		{[]string{`{"cmd":"web_search","args":{"term":"chatcli"}}`}, "chatcli", 10},
		{[]string{`{"cmd":"search","query":"flat with cmd","limit":4}`}, "flat with cmd", 4},
		{[]string{`{"query":"native shape","max_results":7}`}, "native shape", 7},
		{[]string{`{"q":"short key"}`}, "short key", 10},
		{[]string{`{"text":"  padded  "}`}, "padded", 10},
		{[]string{`{"cmd":"search","args":{}}`}, "", 10},
		{[]string{`{"unrelated":"x"}`}, "", 10},
		{[]string{"search", "--query", "positional", "--maxResults", "2"}, "positional", 2},
		{[]string{"search", "bare", "words"}, "bare words", 10},
		{[]string{"golang", "context"}, "golang context", 10},
		{nil, "", 10},
	}
	for _, c := range cases {
		q, m := parseWebSearchArgs(c.args)
		if q != c.query || m != c.max {
			t.Errorf("%v: got (%q, %d), want (%q, %d)", c.args, q, m, c.query, c.max)
		}
	}
	_, err := NewBuiltinWebSearchPlugin().Execute(context.Background(), []string{`{"cmd":"search","args":{}}`})
	if err == nil || !strings.Contains(err.Error(), `"query"`) {
		t.Fatalf("a JSON call without a query must name the field, got %v", err)
	}
}
