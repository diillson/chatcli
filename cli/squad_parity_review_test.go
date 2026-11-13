/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/runs"
)

// Each test here replays a line that used to do something other than what
// was typed, without saying so.
func TestSquadReview_ApostropheNeverSwallowsFlags(t *testing.T) {
	_, store, _ := withSquadFixtures(t)
	cli := &ChatCLI{}
	// The leading apostrophe used to pair with the possessive further on
	// and fold both flags into the title.
	cli.handleBoardCommand("/board create 'tis broken for all --assignee bob --description users' data lost")
	card, err := store.Get("card-1")
	if err != nil || card.Title != "'tis broken for all" || card.Assignee != "bob" || card.Description != "users' data lost" {
		t.Fatalf("apostrophes must stay text and flags must stay flags: %+v %v", card, err)
	}

	// A quote with no single reading is reported, and nothing is created.
	squadOutputs(t, cli.handleBoardCommand, map[string]string{
		`/board create Fix "the open one --assignee bob`: "left open",
		`/board create Fix "glued"words --assignee bob`:  "glued",
		`/board create Fix --assignee ""`:                "--assignee",
	})
	if cards, _ := store.List(""); len(cards) != 1 {
		t.Fatalf("an ambiguous line still created a card: %+v", cards)
	}
	// Verbs whose text is taken as typed are not held to the quoting rules.
	cli.handleBoardCommand(`/board note card-1 they said "wait`)
	if card, _ = store.Get("card-1"); len(card.Notes) != 1 || card.Notes[0].Text != `they said "wait` {
		t.Fatalf("note text must be taken as typed: %+v", card.Notes)
	}
}

func TestSquadReview_BoardRejectsStrayAndDuplicateArguments(t *testing.T) {
	_, store, _ := withSquadFixtures(t)
	cli := &ChatCLI{}
	cli.handleBoardCommand("/board create Original title")

	squadOutputs(t, cli.handleBoardCommand, map[string]string{
		// Words after a one-word value used to rejoin the title.
		"/board create Fix login --assignee bob and alice": "and",
		// The positional title used to be dropped and --title won.
		"/board edit card-1 new title --title flagged": "--title",
		// r2 used to be dropped and the command still said "linked".
		"/board link card-1 --run r1 r2": "r2",
		"/board link card-1 r1 --run r2": "--run",
	})
	cards, _ := store.List("")
	if len(cards) != 1 || cards[0].Title != "Original title" || len(cards[0].RunIDs) != 0 {
		t.Fatalf("a refused command changed the board: %+v", cards)
	}
}

func TestSquadReview_MailRecipientAndTrailingCard(t *testing.T) {
	_, store, bus := withSquadFixtures(t)
	cli := &ChatCLI{}
	if _, err := store.Create("A real card", "", "", ""); err != nil {
		t.Fatal(err)
	}

	// Used to queue a message for a recipient called "--card".
	out := captureStdout(t, func() { cli.handleMailCommand("/mail send --card card-1 coder hello there") })
	if len(bus.Pending()) != 0 || !strings.Contains(out, "--card") || !strings.Contains(out, "Usage: /mail") {
		t.Fatalf("a flag is not a recipient: pending=%+v out=%q", bus.Pending(), out)
	}

	// Used to cut the ending off the text and attach a card that is not there.
	cli.handleMailCommand("/mail send coder remember to pass --card card-3")
	// A trailing --card naming a real card is still the flag.
	cli.handleMailCommand("/mail send coder look at this --card card-1")
	msgs := bus.Peek("coder")
	if len(msgs) != 2 || msgs[0].Text != "remember to pass --card card-3" || msgs[0].CardID != "" {
		t.Fatalf("prose ending in --card must stay whole: %+v", msgs)
	}
	if msgs[1].Text != "look at this" || msgs[1].CardID != "card-1" {
		t.Fatalf("trailing --card with a real card: %+v", msgs[1])
	}
	squadOutputs(t, cli.handleMailCommand, map[string]string{"/mail help": "unambiguous"})
}

func TestSquadReview_HoldEndsOnStaleRunAndIsBounded(t *testing.T) {
	reg, _, _ := withSquadFixtures(t)
	cli := &ChatCLI{}
	const dead = "run-remote-dead"
	setRemoteRunsProvider(&remoteRunsProvider{list: func() []remoteAgentRun {
		return []remoteAgentRun{{Stale: true, Info: runs.Info{
			ID: dead, Kind: runs.KindWorker, Agent: "coder", Status: runs.StatusRunning, Instance: "other", Origin: "gateway",
		}}}
	}})
	t.Cleanup(func() { setRemoteRunsProvider(nil) })

	// The owner died: the hub keeps the run "running" forever, and the hold
	// used to last until Ctrl+C. agentsWithin fails the test if it holds.
	out := agentsWithin(context.Background(), t, cli, "/agents wait "+dead+" --block")
	if !strings.Contains(out, "no longer reports") || !strings.Contains(out, dead) {
		t.Fatalf("a hold on a stale run must end and say why: %q", out)
	}

	// No --timeout still means a bound, and the hold says which.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	out = agentsWithin(ctx, t, cli, "/agents wait "+squadRun(t, reg).ID()+" --block")
	if !strings.Contains(out, "30m0s") || !strings.Contains(out, "keeps running") {
		t.Fatalf("the default bound must be stated: %q", out)
	}

	// The tool's wait is untouched: a stale run still runs out its timeout.
	got, err := newLiveAgentsAdapter(reg).Wait(context.Background(), dead, 300*time.Millisecond)
	if err != nil || !strings.Contains(got, "WAIT TIMED OUT") {
		t.Fatalf("@agents wait changed for a stale run: %q %v", got, err)
	}
}
