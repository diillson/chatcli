/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/cli/agent"
)

func TestBatchContainsRecall(t *testing.T) {
	cases := []struct {
		name  string
		calls []agent.ToolCall
		want  bool
	}{
		{"empty", nil, false},
		{"no recall", []agent.ToolCall{{Name: "@search"}, {Name: "@read"}}, false},
		{"recall present", []agent.ToolCall{{Name: "@search"}, {Name: "@recall"}}, true},
		{"bare recall name", []agent.ToolCall{{Name: "recall"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := batchContainsRecall(tc.calls); got != tc.want {
				t.Fatalf("batchContainsRecall = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildBatchFeedbackMessage(t *testing.T) {
	// A batch without @recall yields a plain user message — no verbatim flag,
	// so it stays eligible for normal compaction.
	plain := buildBatchFeedbackMessage("output", []agent.ToolCall{{Name: "@search"}})
	if plain.Role != "user" || plain.Content != "output" {
		t.Fatalf("unexpected message shape: %+v", plain)
	}
	if plain.Meta != nil && plain.Meta.PreserveVerbatim {
		t.Fatal("non-recall batch must not be flagged PreserveVerbatim")
	}

	// A batch that invoked @recall is flagged so the original survives intact.
	recall := buildBatchFeedbackMessage("output", []agent.ToolCall{{Name: "@recall"}})
	if recall.Meta == nil || !recall.Meta.PreserveVerbatim {
		t.Fatal("recall batch must be flagged PreserveVerbatim")
	}
}
