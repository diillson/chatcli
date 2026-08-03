/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent/mail"
	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/cli/board"
)

// withSquadFixtures points the /agents, /board and /mail command layers at
// isolated stores for one test.
func withSquadFixtures(t *testing.T) (*runs.Registry, *board.Store, *mail.Registry) {
	t.Helper()
	reg := runs.NewRegistry(20)
	store := board.NewStore(filepath.Join(t.TempDir(), "board.json"))
	bus := mail.NewRegistry(20)

	prevRuns, prevBoard, prevMail := agentRunsRegistry, squadBoard, squadMailBus
	agentRunsRegistry = func() *runs.Registry { return reg }
	squadBoard = func() *board.Store { return store }
	squadMailBus = func() *mail.Registry { return bus }
	t.Cleanup(func() {
		agentRunsRegistry, squadBoard, squadMailBus = prevRuns, prevBoard, prevMail
	})
	return reg, store, bus
}

func TestHandleAgentsCommandFlows(t *testing.T) {
	reg, _, _ := withSquadFixtures(t)
	cli := &ChatCLI{}

	// Empty registry: list renders the empty state.
	cli.handleAgentsCommand("/agents")

	ctx, orch := reg.Begin(context.Background(), runs.Info{Kind: runs.KindOrchestrator, Agent: "coder", Task: "goal", Origin: "repl"})
	_, worker := reg.Begin(ctx, runs.Info{Kind: runs.KindWorker, Agent: "reviewer", Task: "review the diff"})
	worker.SetTurn(3, 30)
	worker.SetAction("git-diff")

	cli.handleAgentsCommand("/agents list")
	cli.handleAgentsCommand("/agents show " + worker.ID())
	cli.handleAgentsCommand("/agents show run-999") // not found path
	cli.handleAgentsCommand("/agents show")         // missing ID path
	cli.handleAgentsCommand("/agents help")
	cli.handleAgentsCommand("/agents bogus")

	// Cancel a live run, then observe the finished paths.
	cli.handleAgentsCommand("/agents cancel " + worker.ID())
	worker.End(context.Canceled)
	cli.handleAgentsCommand("/agents cancel " + worker.ID()) // already finished
	cli.handleAgentsCommand("/agents cancel run-999")        // unknown
	cli.handleAgentsCommand("/agents cancel")                // missing ID

	failed := func() *runs.Run {
		_, r := reg.Begin(ctx, runs.Info{Kind: runs.KindSubagent, Agent: "subagent"})
		return r
	}()
	failed.End(errors.New("boom"))
	orch.End(nil)
	cli.handleAgentsCommand("/agents") // recent history rendering (all statuses)
}

func TestHandleBoardCommandFlows(t *testing.T) {
	_, store, _ := withSquadFixtures(t)
	cli := &ChatCLI{}

	cli.handleBoardCommand("/board")        // empty board
	cli.handleBoardCommand("/board create") // missing title
	cli.handleBoardCommand("/board create Fix login bug")
	card, err := store.Get("card-1")
	if err != nil || card.Title != "Fix login bug" {
		t.Fatalf("create via command failed: %+v %v", card, err)
	}

	cli.handleBoardCommand("/board assign card-1 reviewer")
	cli.handleBoardCommand("/board assign card-1") // usage
	cli.handleBoardCommand("/board note card-1 needs QA")
	cli.handleBoardCommand("/board note card-1") // usage
	cli.handleBoardCommand("/board move card-1 review")
	cli.handleBoardCommand("/board move card-1 nowhere") // invalid column
	cli.handleBoardCommand("/board move card-1")         // usage
	cli.handleBoardCommand("/board move card-99 done")   // unknown card
	cli.handleBoardCommand("/board list review")
	cli.handleBoardCommand("/board list nowhere") // invalid filter
	cli.handleBoardCommand("/board show card-1")
	cli.handleBoardCommand("/board show card-99") // not found
	cli.handleBoardCommand("/board show")         // missing ID
	cli.handleBoardCommand("/board archive bogus-duration")
	cli.handleBoardCommand("/board move card-1 done")
	cli.handleBoardCommand("/board archive")
	if _, err := store.Get("card-1"); err == nil {
		t.Fatal("archive via command did not remove the done card")
	}
	cli.handleBoardCommand("/board help")
	cli.handleBoardCommand("/board bogus")

	got, err := store.List("")
	if err != nil || len(got) != 0 {
		t.Fatalf("expected empty board after archive: %v %v", got, err)
	}
}

func TestHandleMailCommandFlows(t *testing.T) {
	_, _, bus := withSquadFixtures(t)
	cli := &ChatCLI{}

	cli.handleMailCommand("/mail")         // empty history
	cli.handleMailCommand("/mail pending") // empty pending
	cli.handleMailCommand("/mail send")    // usage
	cli.handleMailCommand("/mail send coder prioritize the login fix")

	if pending := bus.Pending(); pending["coder"] != 1 {
		t.Fatalf("send via command did not enqueue: %+v", pending)
	}
	msgs := bus.Peek("coder")
	if len(msgs) != 1 || msgs[0].From != "user" || !strings.Contains(msgs[0].Text, "login fix") {
		t.Fatalf("unexpected message: %+v", msgs)
	}

	cli.handleMailCommand("/mail list")
	cli.handleMailCommand("/mail pending")
	cli.handleMailCommand("/mail help")
	cli.handleMailCommand("/mail bogus")
}

func TestLiveAgentsAdapter(t *testing.T) {
	reg := runs.NewRegistry(20)
	a := &liveAgentsAdapter{reg: reg}

	out, err := a.List()
	if err != nil || !strings.Contains(out, "(none)") {
		t.Fatalf("empty list: %q %v", out, err)
	}

	ctx, orch := reg.Begin(context.Background(), runs.Info{Kind: runs.KindOrchestrator, Agent: "coder", Task: "goal", Origin: "repl"})
	_, worker := reg.Begin(ctx, runs.Info{Kind: runs.KindWorker, Agent: "tester", Task: "run tests", CallID: "agent_1"})
	worker.SetTurn(2, 30)
	worker.SetAction("test ./...")
	worker.AddToolCalls(3)

	out, err = a.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ACTIVE RUNS (2)", "agent=tester", "turn=2/30", `action="test ./..."`, "tools=3", "parent=" + orch.ID()} {
		if !strings.Contains(out, want) {
			t.Fatalf("List missing %q in:\n%s", want, out)
		}
	}

	show, err := a.Show(worker.ID())
	if err != nil || !strings.Contains(show, "current_action=test ./...") {
		t.Fatalf("Show: %q %v", show, err)
	}
	showParent, err := a.Show(orch.ID())
	if err != nil || !strings.Contains(showParent, "live_children=1") {
		t.Fatalf("Show parent: %q %v", showParent, err)
	}
	if _, err := a.Show("run-999"); err == nil {
		t.Fatal("Show unknown must error")
	}

	if msg, err := a.Cancel(worker.ID()); err != nil || !strings.Contains(msg, "cancellation requested") {
		t.Fatalf("Cancel: %q %v", msg, err)
	}
	worker.End(context.Canceled)
	if _, err := a.Cancel(worker.ID()); err == nil {
		t.Fatal("Cancel finished must error")
	}
	if _, err := a.Cancel("run-999"); err == nil {
		t.Fatal("Cancel unknown must error")
	}
	orch.End(errors.New("fail"))
	out, _ = a.List()
	if !strings.Contains(out, "status=failed") || !strings.Contains(out, `error=`) {
		t.Fatalf("finished statuses missing:\n%s", out)
	}
	_ = newLiveAgentsAdapter(nil) // default-registry constructor path
}

func TestLiveBoardAdapter(t *testing.T) {
	store := board.NewStore(filepath.Join(t.TempDir(), "board.json"))
	a := &liveBoardAdapter{store: store}

	if out, err := a.List(""); err != nil || !strings.Contains(out, "BOARD EMPTY") {
		t.Fatalf("empty list: %q %v", out, err)
	}
	out, err := a.Create("Implement /foo", "acceptance criteria", "coder", "doing")
	if err != nil || !strings.Contains(out, "card-1") {
		t.Fatalf("Create: %q %v", out, err)
	}
	if _, err := a.Create("X", "", "", "nowhere"); err == nil {
		t.Fatal("invalid column must error")
	}
	if _, err := a.List("nowhere"); err == nil {
		t.Fatal("invalid filter must error")
	}

	if out, err = a.Move("card-1", "review", "orchestrator"); err != nil || !strings.Contains(out, "[review]") {
		t.Fatalf("Move: %q %v", out, err)
	}
	if _, err := a.Move("card-1", "nowhere", ""); err == nil {
		t.Fatal("invalid target column must error")
	}
	if _, err := a.Assign("card-1", "reviewer"); err != nil {
		t.Fatal(err)
	}
	if out, err = a.Note("card-1", "", "review passed"); err != nil || !strings.Contains(out, "1 notes total") {
		t.Fatalf("Note: %q %v", out, err)
	}
	if out, err = a.Link("card-1", "run-7", "job-3"); err != nil || !strings.Contains(out, "runs=run-7") || !strings.Contains(out, "jobs=job-3") {
		t.Fatalf("Link: %q %v", out, err)
	}
	show, err := a.Show("card-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"assignee=reviewer", "review passed", "runs=run-7", "history:"} {
		if !strings.Contains(show, want) {
			t.Fatalf("Show missing %q:\n%s", want, show)
		}
	}
	list, err := a.List("review")
	if err != nil || !strings.Contains(list, "== REVIEW ==") {
		t.Fatalf("List grouped: %q %v", list, err)
	}
	if _, err := a.Archive("bogus"); err == nil {
		t.Fatal("invalid duration must error")
	}
	if _, err = a.Move("card-1", "done", ""); err != nil {
		t.Fatal(err)
	}
	if out, err = a.Archive(""); err != nil || !strings.Contains(out, "archived 1") {
		t.Fatalf("Archive: %q %v", out, err)
	}
	_ = newLiveBoardAdapter(nil) // default-store constructor path
}

func TestLiveMailAdapter(t *testing.T) {
	bus := mail.NewRegistry(20)
	a := &liveMailAdapter{reg: bus}

	if out, err := a.Inbox(); err != nil || out != "inbox empty" {
		t.Fatalf("empty inbox: %q %v", out, err)
	}
	if out, err := a.History(5); err != nil || out != "no squad mail yet" {
		t.Fatalf("empty history: %q %v", out, err)
	}
	out, err := a.Send("coder", "card-2", "fix the tests")
	if err != nil || !strings.Contains(out, "orchestrator -> coder") {
		t.Fatalf("Send: %q %v", out, err)
	}
	if _, err := a.Send("", "", ""); err == nil {
		t.Fatal("invalid send must error")
	}
	if _, err := bus.Send("reviewer", "orchestrator", "", "verdict: passed"); err != nil {
		t.Fatal(err)
	}
	inbox, err := a.Inbox()
	if err != nil || !strings.Contains(inbox, "[SQUAD MAIL]") || !strings.Contains(inbox, "verdict: passed") {
		t.Fatalf("Inbox: %q %v", inbox, err)
	}
	hist, err := a.History(10)
	if err != nil || !strings.Contains(hist, "orchestrator -> coder") || !strings.Contains(hist, "card=card-2") {
		t.Fatalf("History: %q %v", hist, err)
	}
	_ = newLiveMailAdapter(nil) // default-bus constructor path
}
