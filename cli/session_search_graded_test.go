package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

func saveChat(t *testing.T, sm *SessionManager, name string, msgs ...models.Message) {
	t.Helper()
	if err := sm.SaveSessionV2(name, &SessionData{Version: 2, ChatHistory: msgs}); err != nil {
		t.Fatal(err)
	}
}

// A query that names several things finds the session holding all of them,
// and also the sessions that hold part of them, listed after it and labeled:
// the fact that finishes an answer often sits in a session worded differently.
func TestSearchSessions_PartialMatchesFollowFullOnes(t *testing.T) {
	sm := newTestSessionManager(t)
	saveChat(t, sm, "full", models.Message{Role: "user", Content: "I bought a waterproof cover and a detailing spray for the car"})
	saveChat(t, sm, "partial-two", models.Message{Role: "user", Content: "The cover and the spray for the car arrived from Amazon"})
	saveChat(t, sm, "partial-one", models.Message{Role: "user", Content: "My cover letter needs work"})
	saveChat(t, sm, "unrelated", models.Message{Role: "user", Content: "Tell me about volcanoes"})

	hits, err := sm.SearchSessions("waterproof cover detailing spray car", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 || hits[0].Session != "full" || hits[0].Partial {
		t.Fatalf("the full match must come first and not be partial: %+v", hits)
	}
	var seen bool
	for _, h := range hits[1:] {
		if !h.Partial || h.Terms != 5 || h.Covered < 3 {
			t.Errorf("later hits must be partial with at least half the terms: %+v", h)
		}
		if h.Session == "partial-two" {
			seen = true
		}
		if h.Session == "partial-one" || h.Session == "unrelated" {
			t.Errorf("%s holds too few of the terms to be listed", h.Session)
		}
	}
	if !seen {
		t.Errorf("partial-two (3 of 5 terms) must be listed: %+v", hits)
	}
}

// Two-term queries keep strict AND: with so few words one shared term is not a
// signal, and widening would flood every common word.
func TestSearchSessions_TwoTermsStayStrict(t *testing.T) {
	sm := newTestSessionManager(t)
	saveChat(t, sm, "both", models.Message{Role: "user", Content: "kayak rental in Lisbon"})
	saveChat(t, sm, "one", models.Message{Role: "user", Content: "Lisbon weather next week"})
	hits, _ := sm.SearchSessions("kayak lisbon", 3)
	if len(hits) != 1 || hits[0].Session != "both" {
		t.Fatalf("two terms must stay AND once a session holds both: %+v", hits)
	}
}

// Partial matches are bounded however large the history is.
func TestSearchSessions_PartialMatchesAreCapped(t *testing.T) {
	sm := newTestSessionManager(t)
	saveChat(t, sm, "full", models.Message{Role: "user", Content: "alpha bravo charlie delta"})
	for i := 0; i < 12; i++ {
		saveChat(t, sm, fmt.Sprintf("p%02d", i), models.Message{Role: "user", Content: "alpha bravo only"})
	}
	hits, _ := sm.SearchSessions("alpha bravo charlie delta", 3)
	partial := 0
	for _, h := range hits {
		if h.Partial {
			partial++
		}
	}
	if partial != maxPartialSessions || len(hits) != 1+maxPartialSessions {
		t.Fatalf("want %d partial hits after the full one, got %d of %d", maxPartialSessions, partial, len(hits))
	}
}

// A long assistant turn must not crowd out the user's own statement.
func TestSearchSessions_UserSnippetsSurviveLongAssistantTurns(t *testing.T) {
	sm := newTestSessionManager(t)
	msgs := []models.Message{}
	for i := 0; i < 4; i++ {
		msgs = append(msgs, models.Message{Role: "assistant", Content: strings.Repeat("Tomato trellis advice tomato staking tomato care. ", 8)})
	}
	msgs = append(msgs, models.Message{Role: "user", Content: "I have grown tomato plants since February"})
	saveChat(t, sm, "garden", msgs...)
	hits, _ := sm.SearchSessions("tomato", 3)
	if len(hits) != 1 {
		t.Fatalf("hits: %+v", hits)
	}
	var user bool
	for _, sn := range hits[0].Snippets {
		if strings.HasPrefix(sn, "user: ") {
			user = true
		}
	}
	if !user || len(hits[0].Snippets) != 3 {
		t.Fatalf("the user's statement must be among the 3 snippets: %q", hits[0].Snippets)
	}
}
