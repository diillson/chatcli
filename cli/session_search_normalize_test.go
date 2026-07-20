/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"os"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/diillson/chatcli/models"
)

func TestNormalizeForSearch(t *testing.T) {
	cases := map[string]string{
		"Autenticação OAuth": "autenticacao oauth",
		"SESSÃO É ÚTIL":      "sessao e util",
		"ASCII only":         "ascii only",
		"ção-ñ-ü":            "cao-n-u",
		"日本語 unchanged":      "日本語 unchanged",
	}
	for in, want := range cases {
		if got := normalizeForSearch(in); got != want {
			t.Errorf("normalizeForSearch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSignificantSearchTerms(t *testing.T) {
	// Pure recall framing collapses to nothing — the caller then ranks by
	// BM25 + recency instead of AND-filtering.
	if sig := significantSearchTerms([]string{"o", "que", "discutimos", "ontem"}); sig != nil {
		t.Errorf("all-stopword query should yield nil, got %v", sig)
	}
	// Content-bearing terms survive; framing and grammar drop out.
	got := significantSearchTerms([]string{"o", "que", "discutimos", "sobre", "spinner", "do", "coder"})
	if len(got) != 2 || got[0] != "spinner" || got[1] != "coder" {
		t.Errorf("expected [spinner coder], got %v", got)
	}
}

// TestSearchSessions_NaturalLanguagePTQuery reproduces the reported failure:
// a natural Portuguese recall question against a session that discussed the
// topic. Under the old raw AND filter, framing words like "discutimos"
// disqualified the session and search returned nothing.
func TestSearchSessions_NaturalLanguagePTQuery(t *testing.T) {
	sm := newTestSessionManager(t)
	if err := sm.SaveSessionV2("coder-work", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "user", Content: "O spinner do coder está mostrando o modelo errado"},
			{Role: "assistant", Content: "Corrigi o turnTimer para usar o client resolvido do turno."},
		},
	}); err != nil {
		t.Fatal(err)
	}

	hits, err := sm.SearchSessions("o que discutimos sobre o spinner do coder?", 3)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Session != "coder-work" {
		t.Fatalf("natural PT query should match the session, got %+v", hits)
	}
}

// TestSearchSessions_AccentInsensitive: query without accents must match
// accented session text and vice versa.
func TestSearchSessions_AccentInsensitive(t *testing.T) {
	sm := newTestSessionManager(t)
	if err := sm.SaveSessionV2("auth", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "assistant", Content: "A autenticação OAuth usa PKCE com loopback."},
		},
	}); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{"autenticacao", "AUTENTICAÇÃO pkce"} {
		hits, err := sm.SearchSessions(q, 3)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		if len(hits) != 1 || hits[0].Session != "auth" {
			t.Errorf("query %q should match accented content, got %+v", q, hits)
		}
	}
}

// TestSearchSessions_AllStopwordQueryRanksRecent: a pure framing question
// returns the recent sessions instead of erroring or matching nothing.
func TestSearchSessions_AllStopwordQueryRanksRecent(t *testing.T) {
	sm := newTestSessionManager(t)
	for _, name := range []string{"old-one", "new-one"} {
		if err := sm.SaveSessionV2(name, &SessionData{
			Version: 2,
			ChatHistory: []models.Message{
				{Role: "user", Content: "conteúdo da sessão " + name},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Age old-one by a month so the recency boost separates the two.
	past := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(sm.getSessionPath("old-one"), past, past); err != nil {
		t.Fatal(err)
	}

	hits, err := sm.SearchSessions("o que a gente fez?", 3)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("all-stopword query should rank all sessions, got %+v", hits)
	}
	if hits[0].Session != "new-one" {
		t.Errorf("recency boost should rank the newer session first, got %+v", hits)
	}
	if hits[0].SavedAt.IsZero() {
		t.Errorf("hits should carry SavedAt, got %+v", hits[0])
	}
}

// TestSearchSessions_CorpusCacheInvalidation: a save after a search must be
// visible to the next search (the directory-signature cache invalidates).
func TestSearchSessions_CorpusCacheInvalidation(t *testing.T) {
	sm := newTestSessionManager(t)
	if err := sm.SaveSessionV2("first", &SessionData{
		Version:     2,
		ChatHistory: []models.Message{{Role: "user", Content: "kubernetes operator drift"}},
	}); err != nil {
		t.Fatal(err)
	}
	if hits, _ := sm.SearchSessions("kubernetes", 3); len(hits) != 1 {
		t.Fatalf("expected first session to match, got %+v", hits)
	}

	if err := sm.SaveSessionV2("second", &SessionData{
		Version:     2,
		ChatHistory: []models.Message{{Role: "user", Content: "kubernetes ingress timeout"}},
	}); err != nil {
		t.Fatal(err)
	}
	if hits, _ := sm.SearchSessions("kubernetes", 3); len(hits) != 2 {
		t.Fatalf("cache must refresh after a new save, got %+v", hits)
	}
}

// TestSnippetAroundUTF8Boundaries: windows sliced out of accented content
// must stay valid UTF-8 (downstream CLIs hard-reject invalid bytes).
func TestSnippetAroundUTF8Boundaries(t *testing.T) {
	content := repeat("ã", 100) + " âncora " + repeat("é", 100)
	norm := normalizeForSearch(content)
	s := snippetAround(content, norm, "ancora")
	if !utf8.ValidString(s) {
		t.Fatalf("snippet must be valid UTF-8, got %q", s)
	}
}

// TestDeriveSessionTitle: saves without an explicit title get one derived
// from the first user message, whitespace-collapsed and rune-capped.
func TestDeriveSessionTitle(t *testing.T) {
	sm := newTestSessionManager(t)
	long := repeat("um assunto bem comprido ", 10)
	if err := sm.SaveSessionV2("titled", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "system", Content: "system prompt"},
			{Role: "user", Content: "  corrigir   o\n delegate do @model  "},
			{Role: "assistant", Content: long},
		},
	}); err != nil {
		t.Fatal(err)
	}
	sd, err := sm.LoadSessionV2("titled")
	if err != nil {
		t.Fatal(err)
	}
	if sd.Title != "corrigir o delegate do @model" {
		t.Errorf("derived title mismatch: %q", sd.Title)
	}

	// Explicit titles win; long derived titles are rune-capped.
	if err := sm.SaveSessionV2("explicit", &SessionData{
		Version:     2,
		Title:       "meu título",
		ChatHistory: []models.Message{{Role: "user", Content: long}},
	}); err != nil {
		t.Fatal(err)
	}
	sd, _ = sm.LoadSessionV2("explicit")
	if sd.Title != "meu título" {
		t.Errorf("explicit title must win, got %q", sd.Title)
	}
	if titles := sm.SessionTitles(); titles["titled"] != "corrigir o delegate do @model" {
		t.Errorf("SessionTitles must expose stored titles, got %v", titles)
	}
}

// TestLatestSessionInfo: newest session by mtime wins and carries its title.
func TestLatestSessionInfo(t *testing.T) {
	sm := newTestSessionManager(t)
	if name, _, _ := sm.LatestSessionInfo(); name != "" {
		t.Fatalf("empty store must yield empty info, got %q", name)
	}
	for _, n := range []string{"older", "newest"} {
		if err := sm.SaveSessionV2(n, &SessionData{
			Version:     2,
			ChatHistory: []models.Message{{Role: "user", Content: "assunto da " + n}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(sm.getSessionPath("older"), past, past); err != nil {
		t.Fatal(err)
	}
	name, saved, title := sm.LatestSessionInfo()
	if name != "newest" || saved.IsZero() || title != "assunto da newest" {
		t.Errorf("LatestSessionInfo = %q/%v/%q", name, saved, title)
	}
}
