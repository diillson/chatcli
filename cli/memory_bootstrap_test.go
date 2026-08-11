/*
 * ChatCLI - Persistent-memory bootstrap card tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/board"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/models"
)

// withTempBoard points the read-side board seam at a temp store for the test.
func withTempBoard(t *testing.T) *board.Store {
	t.Helper()
	store := board.NewStore(filepath.Join(t.TempDir(), "board.json"))
	prev := boardStore
	boardStore = func() *board.Store { return store }
	t.Cleanup(func() { boardStore = prev })
	return store
}

func newBootstrapCLI(t *testing.T) *ChatCLI {
	t.Helper()
	cli := newTestCLIWithMemory(t)
	cli.sessionManager = newTestSessionManager(t)
	return cli
}

func TestMemoryBootstrapCard_CountsAndDirectives(t *testing.T) {
	cli := newBootstrapCLI(t)
	mgr := cli.memoryStore.Manager()
	if !mgr.Facts.AddFact("Bedrock max tokens come from the family catalog", "architecture", nil) {
		t.Fatal("seed fact")
	}
	if err := cli.sessionManager.SaveSessionV2("jira-sprint", &SessionData{
		Version: 2,
		Title:   "planning sprint 12",
		ChatHistory: []models.Message{
			{Role: "user", Content: "let's plan the Jira sprint"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	store := withTempBoard(t)
	if _, err := store.Create("fix auth flow", "", "coder", board.ColDoing); err != nil {
		t.Fatal(err)
	}

	chatCard, agentCard := cli.memoryBootstrapCards()

	for name, card := range map[string]string{"chat": chatCard, "agent": agentCard} {
		if !strings.Contains(card, "[PERSISTENT MEMORY") {
			t.Fatalf("%s card missing header: %q", name, card)
		}
		if !strings.Contains(card, "Long-term facts: 1") {
			t.Errorf("%s card missing fact count: %q", name, card)
		}
		if !strings.Contains(card, "Saved conversations: 1") {
			t.Errorf("%s card missing session count: %q", name, card)
		}
		if !strings.Contains(card, `"jira-sprint"`) {
			t.Errorf("%s card missing latest session name: %q", name, card)
		}
		if !strings.Contains(card, `"fix auth flow"`) {
			t.Errorf("%s card missing doing-card title: %q", name, card)
		}
		if !strings.Contains(card, "Never claim you have no memory") {
			t.Errorf("%s card missing the anti-amnesia directive: %q", name, card)
		}
	}

	// Surface-appropriate pull routes: agent teaches its tools; chat must
	// never instruct a tool it does not have.
	if !strings.Contains(agentCard, "@session search") || !strings.Contains(agentCard, "@board list") {
		t.Errorf("agent card missing pull routes: %q", agentCard)
	}
	if strings.Contains(chatCard, "@session") || strings.Contains(chatCard, "@board") {
		t.Errorf("chat card must not reference agent-only tools: %q", chatCard)
	}
	if !strings.Contains(chatCard, "/session attach") {
		t.Errorf("chat card missing the user-side attach route: %q", chatCard)
	}
}

func TestMemoryBootstrapCard_EmptyStoresStaySilent(t *testing.T) {
	cli := newBootstrapCLI(t)
	withTempBoard(t)

	chatCard, agentCard := cli.memoryBootstrapCards()
	if chatCard != "" || agentCard != "" {
		t.Fatalf("fresh install must not render a card, got chat=%q agent=%q", chatCard, agentCard)
	}
}

func TestMemoryBootstrapCard_MemoryOff(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_MODE", "off")
	cli := newBootstrapCLI(t)
	withTempBoard(t)
	mgr := cli.memoryStore.Manager()
	mgr.Facts.AddFact("some fact", "general", nil)

	if chatCard, agentCard := cli.memoryBootstrapCards(); chatCard != "" || agentCard != "" {
		t.Fatalf("memory off must suppress the card, got chat=%q agent=%q", chatCard, agentCard)
	}
}

func TestTurnHints_FirstTurnUsesUserInput(t *testing.T) {
	cli := newBootstrapCLI(t)

	if got := cli.recentHistoryHints(); len(got) != 0 {
		t.Fatalf("empty history must yield no history hints, got %v", got)
	}
	hints := cli.turnHints("kanban stories from the servicenow board")
	if len(hints) == 0 {
		t.Fatal("first-turn hints must derive from the current input")
	}
	joined := strings.Join(hints, " ")
	if !strings.Contains(joined, "kanban") && !strings.Contains(joined, "servicenow") {
		t.Errorf("expected content-bearing hints from the input, got %v", hints)
	}
}

func TestSignificantSearchTerms_ReferentialFramingDrops(t *testing.T) {
	// "o que falamos anteriormente?" is pure framing: with the framing set
	// incomplete, the lone survivor "anteriormente" made BM25 rank the whole
	// store on a framing word and surface months-old sessions.
	norm := normalizeForSearch("o que falamos anteriormente?")
	if terms := significantSearchTerms(strings.Fields(norm)); terms != nil {
		t.Fatalf("pure referential framing must yield no significant terms, got %v", terms)
	}
	// Content-bearing words still survive framing removal.
	norm = normalizeForSearch("configuração anterior do nginx")
	terms := significantSearchTerms(strings.Fields(norm))
	if len(terms) != 2 || terms[0] != "configuracao" || terms[1] != "nginx" {
		t.Fatalf("expected [configuracao nginx], got %v", terms)
	}
}

func TestSessionAutoRecall_ReferentialFirstTurnListsRecent(t *testing.T) {
	cli := newBootstrapCLI(t)
	if err := cli.sessionManager.SaveSessionV2("autosave-yesterday", &SessionData{
		Version: 2,
		Title:   "mcp aws login",
		ChatHistory: []models.Message{
			{Role: "user", Content: "faz login no aws-mcp"},
			{Role: "assistant", Content: "token renovado com sucesso"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// First turn of a fresh session: no history, no hints — only the user's
	// referential question. The block must still fire and point at the
	// saved session.
	block := cli.chatSessionAutoRecallBlock(nil, "o que falamos anteriormente?")
	if !strings.Contains(block, "[SESSION RECALL]") {
		t.Fatalf("referential first-turn question must fire session recall, got %q", block)
	}
	if !strings.Contains(block, "autosave-yesterday") {
		t.Errorf("expected the saved session pointer, got %q", block)
	}
}

func TestExtractKeywordsSeesCurrentInput(t *testing.T) {
	// Guard for the shared hint contract: ExtractKeywords over the current
	// input must produce hints Facts.Search can match — the first-turn
	// auto-recall path depends on it end to end.
	cli := newBootstrapCLI(t)
	mgr := cli.memoryStore.Manager()
	if !mgr.Facts.AddFact("ServiceNow stories are tracked on the sprint board", "project", []string{"servicenow"}) {
		t.Fatal("seed fact")
	}
	hints := memory.ExtractKeywords([]string{"quais tarefas do servicenow estou tocando?"})
	block := cli.memoryAutoRecallBlock(hints)
	if !strings.Contains(block, "[MEMORY AUTO-RECALL]") || !strings.Contains(block, "ServiceNow") {
		t.Fatalf("first-turn hints must reach the fact index, got %q", block)
	}
}
