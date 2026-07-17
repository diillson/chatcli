/*
 * ChatCLI - Ranked session search + paginated session read tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestSearchSessions_TermsAcrossMessagesInOneSession(t *testing.T) {
	sm := newTestSessionManager(t)
	if err := sm.SaveSessionV2("scattered", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "user", Content: "Let's design the oauth login flow."},
			{Role: "assistant", Content: "The refresh token rotates every hour."},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// "oauth" and "refresh" live in DIFFERENT messages of the same session —
	// the old per-message AND returned nothing for this.
	hits, err := sm.SearchSessions("oauth refresh", 3)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Session != "scattered" {
		t.Fatalf("terms across messages of one session must match, got %+v", hits)
	}
	if hits[0].Score <= 0 {
		t.Errorf("hit must carry a positive BM25 score, got %f", hits[0].Score)
	}
}

func TestSearchSessions_RanksDenseSessionFirst(t *testing.T) {
	sm := newTestSessionManager(t)
	if err := sm.SaveSessionV2("passing-mention", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "user", Content: "Unrelated work, though the scheduler came up once."},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := sm.SaveSessionV2("deep-dive", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "user", Content: "Redesign the scheduler preflight policy."},
			{Role: "assistant", Content: "The scheduler daemon re-arms the scheduler interval in place."},
		},
	}); err != nil {
		t.Fatal(err)
	}

	hits, err := sm.SearchSessions("scheduler", 3)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("both sessions qualify, got %+v", hits)
	}
	if hits[0].Session != "deep-dive" {
		t.Errorf("the term-dense session must rank first, got %q", hits[0].Session)
	}
}

func TestGetSessionMessages_Pagination(t *testing.T) {
	sm := newTestSessionManager(t)
	var msgs []models.Message
	for i := 0; i < 7; i++ {
		msgs = append(msgs, models.Message{Role: "user", Content: "message " + string(rune('a'+i))})
	}
	if err := sm.SaveSessionV2("long", &SessionData{Version: 2, ChatHistory: msgs}); err != nil {
		t.Fatal(err)
	}

	page, total, err := sm.GetSessionMessages("long", 0, 3)
	if err != nil || total != 7 || len(page) != 3 || page[0].Content != "message a" {
		t.Fatalf("first page wrong: %v total=%d page=%+v", err, total, page)
	}
	page, total, err = sm.GetSessionMessages("long", 6, 3)
	if err != nil || total != 7 || len(page) != 1 || page[0].Content != "message g" {
		t.Fatalf("tail page wrong: %v total=%d page=%+v", err, total, page)
	}
	if page, total, err = sm.GetSessionMessages("long", 99, 3); err != nil || total != 7 || len(page) != 0 {
		t.Fatalf("past-the-end must be empty, not error: %v total=%d page=%+v", err, total, page)
	}
	if _, _, err = sm.GetSessionMessages("missing", 0, 3); err == nil {
		t.Error("missing session must error")
	}
}
