/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeWikipediaLang(t *testing.T) {
	cases := map[string]string{
		"":      wikipediaDefaultLang,
		"EN":    "en",
		"pt-BR": "pt",
		"pt_BR": "pt",
		"es":    "es",
		"x9!":   wikipediaDefaultLang, // non-alpha → default
	}
	for in, want := range cases {
		if got := normalizeWikipediaLang(in); got != want {
			t.Errorf("normalizeWikipediaLang(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseWikipediaArgs(t *testing.T) {
	t.Run("flat query", func(t *testing.T) {
		cfg, err := parseWikipediaArgs([]string{`{"query":"Alan Turing"}`})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Query != "Alan Turing" || cfg.Lang != "en" {
			t.Fatalf("got %+v", cfg)
		}
	})
	t.Run("read with lang", func(t *testing.T) {
		cfg, err := parseWikipediaArgs([]string{`{"read":"Computação quântica","lang":"pt-BR"}`})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Read != "Computação quântica" || cfg.Lang != "pt" {
			t.Fatalf("got %+v", cfg)
		}
	})
	t.Run("bare argv query", func(t *testing.T) {
		cfg, err := parseWikipediaArgs([]string{"quantum", "computing"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Query != "quantum computing" {
			t.Fatalf("got %+v", cfg)
		}
	})
	t.Run("empty errors", func(t *testing.T) {
		if _, err := parseWikipediaArgs([]string{`{}`}); err == nil {
			t.Fatal("expected error for empty args")
		}
	})
}

func TestWikipediaSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") != "opensearch" {
			t.Errorf("unexpected action %q", r.URL.Query().Get("action"))
		}
		_, _ = w.Write([]byte(`["turing",["Alan Turing","Turing machine"],["",""],["",""]]`))
	}))
	defer server.Close()

	old := wikipediaBaseURL
	wikipediaBaseURL = func(string) string { return server.URL }
	defer func() { wikipediaBaseURL = old }()

	out, err := NewBuiltinWikipediaPlugin().Execute(context.Background(), []string{`{"query":"turing"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Alan Turing") || !strings.Contains(out, "Turing machine") {
		t.Fatalf("missing titles: %q", out)
	}
}

func TestWikipediaRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") != "query" {
			t.Errorf("unexpected action %q", r.URL.Query().Get("action"))
		}
		_, _ = w.Write([]byte(`{"query":{"pages":{"123":{"title":"Alan Turing","extract":"A British mathematician."}}}}`))
	}))
	defer server.Close()

	old := wikipediaBaseURL
	wikipediaBaseURL = func(string) string { return server.URL }
	defer func() { wikipediaBaseURL = old }()

	out, err := NewBuiltinWikipediaPlugin().Execute(context.Background(), []string{`{"read":"Alan Turing"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Alan Turing") || !strings.Contains(out, "British mathematician") {
		t.Fatalf("missing extract: %q", out)
	}
}

func TestWikipediaSearchNoResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`["zzz",[],[],[]]`))
	}))
	defer server.Close()

	old := wikipediaBaseURL
	wikipediaBaseURL = func(string) string { return server.URL }
	defer func() { wikipediaBaseURL = old }()

	out, err := NewBuiltinWikipediaPlugin().Execute(context.Background(), []string{`{"query":"zzz"}`})
	if err != nil {
		t.Fatal(err)
	}
	// i18n is not initialized under `go test`, so T() returns the raw key plus
	// the formatted arg — assert on the key and the echoed term, which are
	// stable regardless of locale loading.
	if !strings.Contains(out, "no_results") || !strings.Contains(out, "zzz") {
		t.Fatalf("expected no-results message, got %q", out)
	}
}

const wikiArticle = "Alan Turing was a mathematician.\n\n\n== Early life ==\nBorn in London.\n\n\n=== Family ===\nHis father worked abroad.\n\n\n== Career ==\nHe worked at Bletchley Park.\n\n\n== See also ==\n"

func wikiExtractServer(t *testing.T) func() {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("exintro") != "" {
			t.Errorf("section/full reads must not ask for the intro only")
		}
		body, _ := json.Marshal(map[string]interface{}{"query": map[string]interface{}{"pages": map[string]interface{}{"1": map[string]string{"title": "Alan Turing", "extract": wikiArticle}}}})
		_, _ = w.Write(body)
	}))
	old := wikipediaBaseURL
	wikipediaBaseURL = func(string) string { return server.URL }
	return func() { server.Close(); wikipediaBaseURL = old }
}

func TestWikipediaSectionsAndSectionRead(t *testing.T) {
	defer wikiExtractServer(t)()
	p := NewBuiltinWikipediaPlugin()
	out, err := p.Execute(context.Background(), []string{`{"title":"Alan Turing","sections":true}`})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"4 section(s)", "1. Early life", "  2.   Family", "3. Career", `"section":"<heading or index>"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("sections missing %q: %q", want, out)
		}
	}
	out, err = p.Execute(context.Background(), []string{`{"read":"Alan Turing","section":"early LIFE"}`})
	if err != nil || !strings.Contains(out, "Born in London.") || !strings.Contains(out, "### Family") || strings.Contains(out, "Bletchley") {
		t.Fatalf("section by heading (with its children, not its siblings) = %q %v", out, err)
	}
	out, err = p.Execute(context.Background(), []string{`{"read":"Alan Turing","section":"3"}`})
	if err != nil || !strings.Contains(out, "Bletchley Park") || strings.Contains(out, "London") {
		t.Fatalf("section by index = %q %v", out, err)
	}
	if _, err := p.Execute(context.Background(), []string{`{"read":"Alan Turing","section":"9"}`}); err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("index out of range: %v", err)
	}
	if _, err := p.Execute(context.Background(), []string{`{"read":"Alan Turing","section":"nothing"}`}); err == nil || !strings.Contains(err.Error(), "sections:true") {
		t.Fatalf("unknown heading hints at sections: %v", err)
	}
	out, err = p.Execute(context.Background(), []string{`{"read":"Alan Turing","section":"car"}`})
	if err != nil || !strings.Contains(out, "Bletchley") {
		t.Fatalf("unique substring match = %q %v", out, err)
	}
	out, err = p.Execute(context.Background(), []string{`{"read":"Alan Turing","full":true,"max_chars":40}`})
	if err != nil || !strings.Contains(out, "of ") || !strings.Contains(out, "chars shown") || strings.Contains(out, "Bletchley") {
		t.Fatalf("full read capped = %q %v", out, err)
	}
	out, err = p.Execute(context.Background(), []string{"--read", "Alan Turing", "--full", "--max_chars", "100000"})
	if err != nil || !strings.Contains(out, "Bletchley") {
		t.Fatalf("flat full read = %q %v", out, err)
	}
	a, _ := parseWikipediaArgs([]string{`{"read":"x","max_chars":999999}`})
	if a.MaxChars != wikipediaMaxChars {
		t.Fatalf("max_chars clamp = %d", a.MaxChars)
	}
	if lvl, h, ok := parseWikiHeading("=== Family ==="); !ok || lvl != 3 || h != "Family" {
		t.Fatalf("heading parse = %d %q %v", lvl, h, ok)
	}
	if _, _, ok := parseWikiHeading("== =="); ok {
		t.Fatal("empty heading must not parse")
	}
}
