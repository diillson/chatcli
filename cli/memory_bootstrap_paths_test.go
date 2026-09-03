/*
 * ChatCLI - Coverage for the first-turn memory navigation paths.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/board"
	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/knowledge"
	"go.uber.org/zap"
)

func TestAddBoardNodes_JoinsCardsWithColumnAndAssignee(t *testing.T) {
	store := withTempBoard(t)
	doing, err := store.Create("migrate auth service", "", "coder", board.ColDoing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("write docs", "", "", board.ColBacklog); err != nil {
		t.Fatal(err)
	}

	g := knowledge.New()
	addBoardNodes(g)

	node, ok := g.Node("card:" + doing.ID)
	if !ok {
		t.Fatalf("doing card missing from graph: %+v", g)
	}
	if !strings.Contains(node.Title, "[doing]") || !strings.Contains(node.Title, "migrate auth") {
		t.Errorf("card title must carry column and title, got %q", node.Title)
	}
	if node.Weight != 2.0 {
		t.Errorf("doing cards must outweigh backlog, got %v", node.Weight)
	}
	// Assignee clusters cards by worker via a tag edge.
	if hood := g.Neighborhood("card:"+doing.ID, 1, 10); len(hood) == 0 {
		t.Error("expected the assignee tag edge in the neighborhood")
	}
	// The index card tally now names cards — the boot-visibility signal.
	if card := g.IndexCard(4); !strings.Contains(card, "card 2") {
		t.Errorf("index card must tally board cards, got %q", card)
	}
}

func TestOneShotSystemMessage_InjectsMemoryAndSessionRecall(t *testing.T) {
	cli := newBootstrapCLI(t)
	dir := t.TempDir()
	cli.contextBuilder = workspace.NewContextBuilder(
		workspace.NewBootstrapLoader(dir, dir, zap.NewNop()), cli.memoryStore, dir)
	mgr := cli.memoryStore.Manager()
	if !mgr.Facts.AddFact("The staging deploy runs through the blue pipeline", "architecture", []string{"deploy"}) {
		t.Fatal("seed fact")
	}
	if err := cli.sessionManager.SaveSessionV2("autosave-deploy", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "user", Content: "vamos revisar o deploy da staging"},
			{Role: "assistant", Content: "pipeline azul atualizado"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	sys := cli.oneShotSystemMessage(context.Background(), "o que falamos anteriormente sobre o deploy?")
	if !strings.Contains(sys, ChatModeSystemHint) {
		t.Fatalf("one-shot must carry the chat baseline, got %q", sys)
	}
	if !strings.Contains(sys, "blue pipeline") {
		t.Errorf("one-shot must push the memory retrieval (index promotes to full), got %q", sys)
	}
	if !strings.Contains(sys, "[SESSION RECALL]") || !strings.Contains(sys, "autosave-deploy") {
		t.Errorf("one-shot must surface saved-session pointers, got %q", sys)
	}
}

func TestOneShotSystemMessage_NilBuilderStillHasBaseline(t *testing.T) {
	cli := newBootstrapCLI(t)
	sys := cli.oneShotSystemMessage(context.Background(), "qualquer pergunta")
	if !strings.Contains(sys, ChatModeSystemHint) {
		t.Fatalf("baseline must survive a nil context builder, got %q", sys)
	}
}

func TestFollowUpRecallBlocks_RefiresRecallMidLoop(t *testing.T) {
	cli := newBootstrapCLI(t)
	mgr := cli.memoryStore.Manager()
	if !mgr.Facts.AddFact("Kimi tool_call ids must be paired positionally", "gotcha", []string{"kimi"}) {
		t.Fatal("seed fact")
	}
	if err := cli.sessionManager.SaveSessionV2("autosave-kimi", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "user", Content: "o pairing do kimi quebrou de novo"},
			{Role: "assistant", Content: "corrigido com pairing por ordem dos ids"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	a := &AgentMode{cli: cli}

	block := a.followUpRecallBlocks(context.Background(), "lembra do problema do kimi que resolvemos?")
	if !strings.Contains(block, "[MEMORY AUTO-RECALL]") {
		t.Errorf("mid-loop follow-up must re-rank facts, got %q", block)
	}
	if !strings.Contains(block, "[SESSION RECALL]") {
		t.Errorf("mid-loop follow-up must re-rank sessions, got %q", block)
	}
	if got := a.followUpRecallBlocks(context.Background(), "   "); got != "" {
		t.Errorf("blank follow-up must inject nothing, got %q", got)
	}
}
