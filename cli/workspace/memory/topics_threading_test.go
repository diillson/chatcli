/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestParseTopics(t *testing.T) {
	// New "name: summary" line format.
	got := parseTopics("Go generics: chose type params over reflection\nmemory: added confidence scoring")
	if got["Go generics"] != "chose type params over reflection" {
		t.Errorf("summary not parsed: %q", got["Go generics"])
	}
	if got["memory"] != "added confidence scoring" {
		t.Errorf("second topic summary wrong: %q", got["memory"])
	}

	// Legacy comma-separated names (no summaries).
	legacy := parseTopics("Go, Bubble Tea, memory systems")
	if len(legacy) != 3 || legacy["Bubble Tea"] != "" {
		t.Errorf("legacy CSV not parsed: %+v", legacy)
	}

	// NOTHING_NEW and blanks are ignored.
	if n := parseTopics("NOTHING_NEW\n\n"); len(n) != 0 {
		t.Errorf("NOTHING_NEW should yield no topics, got %+v", n)
	}
}

func TestRecordWithSummaryRolls(t *testing.T) {
	tt := NewTopicTracker(t.TempDir(), zap.NewNop())

	tt.RecordWithSummary(map[string]string{"auth": "uses OAuth2 PKCE"})
	tt.RecordWithSummary(map[string]string{"auth": "switched to device flow"})

	all := tt.GetAll()
	if len(all) != 1 {
		t.Fatalf("expected one topic, got %d", len(all))
	}
	got := all[0]
	if got.Mentions != 2 {
		t.Errorf("mentions should accumulate, got %d", got.Mentions)
	}
	if got.Summary != "switched to device flow" {
		t.Errorf("latest summary should win, got %q", got.Summary)
	}

	// An empty summary must not erase the existing thread.
	tt.RecordWithSummary(map[string]string{"auth": ""})
	if s := tt.GetAll()[0].Summary; s != "switched to device flow" {
		t.Errorf("empty summary erased the thread: %q", s)
	}
}

func TestRecordWithSummaryCaps(t *testing.T) {
	tt := NewTopicTracker(t.TempDir(), zap.NewNop())
	long := strings.Repeat("x", topicSummaryCap+200)
	tt.RecordWithSummary(map[string]string{"topic": long})
	if got := len(tt.GetAll()[0].Summary); got > topicSummaryCap {
		t.Errorf("summary not capped: len %d > %d", got, topicSummaryCap)
	}
}

func TestFormatForPromptIncludesSummary(t *testing.T) {
	tt := NewTopicTracker(t.TempDir(), zap.NewNop())
	tt.RecordWithSummary(map[string]string{"caching": "added a prompt-cache-friendly index card"})
	out := tt.FormatForPrompt(5)
	if !strings.Contains(out, "caching") || !strings.Contains(out, "index card") {
		t.Fatalf("formatted topics missing the summary:\n%s", out)
	}
}
