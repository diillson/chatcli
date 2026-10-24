/*
 * ChatCLI - Tests for multi-phrasing and by-meaning session search
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/embedding"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
)

func saveSession(t *testing.T, sm *SessionManager, name string, user ...string) {
	t.Helper()
	var hist []models.Message
	for _, u := range user {
		hist = append(hist, models.Message{Role: "user", Content: u}, models.Message{Role: "assistant", Content: "Noted."})
	}
	if err := sm.SaveSessionV2(name, &SessionData{Version: 2, ChatHistory: hist}); err != nil {
		t.Fatal(err)
	}
}

func multiFixture(t *testing.T) (*ChatCLI, *sessionPluginAdapter) {
	t.Helper()
	sm := newTestSessionManager(t)
	saveSession(t, sm, "chat-a-marketing", "In my Marketing Research class project I led the data analysis team.")
	saveSession(t, sm, "chat-b-poster", "I presented a poster on my research about social media influencers at a conference.")
	saveSession(t, sm, "chat-c-solo", "I've been working on a solo project for my Data Mining class.")
	saveSession(t, sm, "chat-d-garden", "My tomatoes finally ripened this week.")
	cli := &ChatCLI{sessionManager: sm, logger: zapNop()}
	return cli, &sessionPluginAdapter{cli: cli}
}

func TestNormalizePhrasings(t *testing.T) {
	got := normalizePhrasings([]string{" Projects I led ", "projects i LED", "", "team lead", "a", "b", "c", "d", "e"})
	want := []string{"Projects I led", "team lead", "a", "b", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizePhrasings = %q, want %q", got, want)
	}
}

func TestFuseSessionHits(t *testing.T) {
	now := time.Now()
	r1 := []SessionSearchHit{{Session: "s1", Score: 3, Snippets: []string{"a1", "a2"}}, {Session: "s2", Score: 1, Partial: true, Covered: 1, Terms: 3, Snippets: []string{"b1"}}}
	r2 := []SessionSearchHit{{Session: "s2", Score: 2, Snippets: []string{"b2", "b1"}}, {Session: "s3", Score: 1, Snippets: []string{"c1"}}}
	fused := fuseSessionHits([]string{"q1", "q2"}, [][]SessionSearchHit{r1, r2}, 3, now)
	if len(fused) != 3 {
		t.Fatalf("fused = %+v", fused)
	}
	var s2 SessionSearchHit
	for _, h := range fused {
		if h.Session == "s2" {
			s2 = h
		}
	}
	if s2.Partial || !reflect.DeepEqual(s2.Via, []string{"q1", "q2"}) {
		t.Fatalf("a full match under any phrasing is a full match; via both: %+v", s2)
	}
	if !reflect.DeepEqual(s2.Snippets, []string{"b1", "b2"}) {
		t.Fatalf("snippets merged round-robin without repeats: %q", s2.Snippets)
	}
	if fused[0].Session != "s2" {
		t.Fatalf("found by two phrasings ranks first (RRF): %+v", fused)
	}
	// One phrasing: fusion is the identity on the order.
	one := fuseSessionHits([]string{"q1"}, [][]SessionSearchHit{r1}, 3, now)
	if one[0].Session != "s1" || one[1].Session != "s2" || !one[1].Partial {
		t.Fatalf("single phrasing reordered: %+v", one)
	}
}

func TestSearchMany_FindsSessionsInTheUsersOwnWords(t *testing.T) {
	_, a := multiFixture(t)
	single, err := a.SearchWindow(context.Background(), "projects I led", 3, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(single, "chat-b-poster") {
		t.Fatal("fixture: the poster session shares no word with the question")
	}
	out, err := a.SearchMany(context.Background(), []string{"projects I led", "presented my research"}, 3, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"chat-a-marketing", "chat-b-poster", "found by: presented my research"} {
		if !strings.Contains(out, want) {
			t.Fatalf("multi-phrasing search missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "chat-d-garden") {
		t.Fatalf("unrelated session listed:\n%s", out)
	}
}

func TestSearchWindow_SinglePhrasingUnchangedWithoutSemanticSignals(t *testing.T) {
	cli, a := multiFixture(t)
	t.Setenv("CHATCLI_QUALITY_HYDE_ENABLED", "")
	withEmbeddingProvider(t, embedding.NewNull())
	got, err := a.SearchWindow(context.Background(), "data analysis team", 3, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// The pre-change rendering: plain search hits, header, no extras.
	hits, _ := cli.sessionManager.searchSessionsAt("data analysis team", 3, 0, time.Time{}, time.Time{}, refNowForTest())
	want := renderSessionHits(searchHeaderForTest("data analysis team")+" "+asOfLabelForTest(), hits, refNowForTest())
	if got != want {
		t.Fatalf("single-phrasing output changed:\n got: %s\nwant: %s", got, want)
	}
	if strings.Contains(got, "related by meaning") || strings.Contains(got, "found by") {
		t.Fatal("semantic annotations without a semantic signal")
	}
}

// conceptProvider embeds text onto a few concept axes, so sentences about
// the same thing are close even when they share no word.
type conceptProvider struct{}

var conceptAxes = [][]string{
	{"led", "lead", "leading", "team", "presented", "poster", "research", "managed", "project", "projects"},
	{"tomato", "tomatoes", "garden", "ripened", "plants"},
	{"cake", "baking", "oven"},
}

func (conceptProvider) Name() string   { return "concept-test" }
func (conceptProvider) Dimension() int { return len(conceptAxes) }
func (conceptProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, len(conceptAxes))
		for _, w := range strings.Fields(strings.ToLower(t)) {
			w = strings.Trim(w, ".,!?'\"")
			for ax, words := range conceptAxes {
				for _, cw := range words {
					if w == cw {
						v[ax]++
					}
				}
			}
		}
		out[i] = v
	}
	return out, nil
}

func TestRelatedSessions_ByMeaningAfterLexicalHits(t *testing.T) {
	_, a := multiFixture(t)
	withEmbeddingProvider(t, conceptProvider{})
	out, err := a.SearchWindow(context.Background(), "which projects did I lead", 3, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "chat-b-poster") || !strings.Contains(out, "related by meaning (similarity") {
		t.Fatalf("by-meaning session missing:\n%s", out)
	}
	if strings.Contains(out, "chat-d-garden") {
		t.Fatalf("unrelated session offered as related:\n%s", out)
	}
	// Related hits follow every lexical hit.
	if strings.Index(out, "related by meaning") < strings.LastIndex(out, "chat-a-marketing") {
		t.Fatalf("a related hit preceded a lexical one:\n%s", out)
	}
}

func TestSessionPlugin_QueriesDispatch(t *testing.T) {
	_, a := multiFixture(t)
	plugins.SetSessionAdapter(a)
	t.Cleanup(func() { plugins.SetSessionAdapter(nil) })
	p := plugins.NewBuiltinSessionPlugin()
	for _, args := range []string{
		`{"cmd":"search","args":{"queries":["projects I led","presented my research"]}}`,
		`{"cmd":"search","args":{"query":"projects I led","alternatives":"presented my research"}}`,
		`{"cmd":"search","args":{"query":"projects I led | presented my research"}}`,
	} {
		out, err := p.Execute(context.Background(), []string{args})
		if err != nil || !strings.Contains(out, "chat-b-poster") {
			t.Fatalf("%s:\n%s %v", args, out, err)
		}
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"search","args":{"queries":[]}}`}); err == nil {
		t.Fatal("empty search accepted")
	}
}

// withEmbeddingProvider installs provider as the session's embedding
// provider for one test and forgets every session vector index.
func withEmbeddingProvider(t *testing.T, p embedding.Provider) {
	t.Helper()
	hydeMu.Lock()
	prevP, prevReady := hydeProvider, hydeProviderReady
	hydeProvider, hydeProviderReady = p, true
	hydeMu.Unlock()
	sessionVectorsMu.Lock()
	sessionVectors = map[string]*sessionVectorIndex{}
	sessionVectorsMu.Unlock()
	t.Cleanup(func() {
		hydeMu.Lock()
		hydeProvider, hydeProviderReady = prevP, prevReady
		hydeMu.Unlock()
		sessionVectorsMu.Lock()
		sessionVectors = map[string]*sessionVectorIndex{}
		sessionVectorsMu.Unlock()
	})
}

func refNowForTest() time.Time { return utils.ReferenceNow() }

func searchHeaderForTest(q string) string { return i18n.T("session.tool.match_header", q) }

func asOfLabelForTest() string {
	return i18n.T("session.tool.as_of", utils.ReferenceNow().Format("2006-01-02"))
}
