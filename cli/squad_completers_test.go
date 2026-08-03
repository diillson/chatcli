/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/cli/agent/runs"
)

func TestGetAgentsSuggestions(t *testing.T) {
	cli := &ChatCLI{}

	// Root: subcommands offered.
	got := cli.getAgentsSuggestions(docAt("/agents "))
	if len(got) != 4 {
		t.Fatalf("expected 4 root suggestions, got %d: %v", len(got), got)
	}
	// Prefix filter narrows.
	got = cli.getAgentsSuggestions(docAt("/agents ca"))
	if len(got) != 1 || got[0].Text != "cancel" {
		t.Fatalf("prefix filter: %v", got)
	}

	// show/cancel complete live run IDs from the registry.
	_, run := runs.Default().Begin(context.Background(), runs.Info{Kind: runs.KindWorker, Agent: "coder", Task: "t"})
	defer run.End(nil)
	got = cli.getAgentsSuggestions(docAt("/agents cancel "))
	found := false
	for _, s := range got {
		if s.Text == run.ID() {
			found = true
		}
	}
	if !found {
		t.Fatalf("live run ID not suggested: %v", got)
	}
	// Past the ID slot there is nothing to suggest.
	if got := cli.getAgentsSuggestions(docAt("/agents cancel run-1 extra ")); got != nil {
		t.Fatalf("unexpected suggestions: %v", got)
	}
}

func TestGetBoardSuggestions(t *testing.T) {
	_, store, _ := withSquadFixtures(t)
	cli := &ChatCLI{}

	got := cli.getBoardSuggestions(docAt("/board "))
	if len(got) != 8 {
		t.Fatalf("expected 8 root suggestions, got %d: %v", len(got), got)
	}
	got = cli.getBoardSuggestions(docAt("/board mo"))
	if len(got) != 1 || got[0].Text != "move" {
		t.Fatalf("prefix filter: %v", got)
	}

	// Column slot for list.
	got = cli.getBoardSuggestions(docAt("/board list "))
	if len(got) != 5 {
		t.Fatalf("expected 5 columns, got %v", got)
	}

	// Card IDs come from the store; note getBoardSuggestions reads the
	// process default, so exercise it via the fixture-backed indirection
	// used by the command layer only when wired. Here we assert the
	// column slot for move still works with an ID typed.
	if _, err := store.Create("T", "", "", ""); err != nil {
		t.Fatal(err)
	}
	got = cli.getBoardSuggestions(docAt("/board move card-1 "))
	if len(got) != 5 {
		t.Fatalf("expected 5 target columns, got %v", got)
	}
}

func TestGetMailSuggestions(t *testing.T) {
	cli := &ChatCLI{}
	got := cli.getMailSuggestions(docAt("/mail "))
	if len(got) != 4 {
		t.Fatalf("expected 4 root suggestions, got %d: %v", len(got), got)
	}
	got = cli.getMailSuggestions(docAt("/mail send "))
	if len(got) == 0 {
		t.Fatal("expected recipient suggestions")
	}
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.Text] = true
	}
	if !seen["orchestrator"] || !seen["coder"] {
		t.Fatalf("core recipients missing: %v", got)
	}
	if got := cli.getMailSuggestions(docAt("/mail list extra ")); got != nil {
		t.Fatalf("unexpected suggestions: %v", got)
	}
}
