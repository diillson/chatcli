/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
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
		w.Write([]byte(`["turing",["Alan Turing","Turing machine"],["",""],["",""]]`))
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
		w.Write([]byte(`{"query":{"pages":{"123":{"title":"Alan Turing","extract":"A British mathematician."}}}}`))
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
		w.Write([]byte(`["zzz",[],[],[]]`))
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
