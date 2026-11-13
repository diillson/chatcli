/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/cli/workspace"
	"go.uber.org/zap"
)

// squadOutputs runs each line and checks what it printed has the wanted text.
func squadOutputs(t *testing.T, run func(line string), cases map[string]string) {
	t.Helper()
	for line, want := range cases {
		if out := captureStdout(t, func() { run(line) }); !strings.Contains(out, want) {
			t.Errorf("%q: output %q lacks %q", line, out, want)
		}
	}
}

func TestSquadArgs_TokensAndFlags(t *testing.T) {
	spec := map[string]squadFlagKind{"--text": squadFlagText, "--word": squadFlagWord, "--yes": squadFlagBool}
	parse := func(line string) (squadArgs, error) {
		return parseSquadTail("/cmd verb "+line, spec)
	}

	got, err := parse(`card-1 don't stop --text add tests and docs --word one -y`)
	if err != nil || got.positional(0) != "card-1 don't stop" || got.positional(1) != "don't stop" || got.positional(9) != "" {
		t.Fatalf("positionals: %+v %v", got.pos, err)
	}
	if got.values["--text"] != "add tests and docs" || got.values["--word"] != "one" || !got.set["--yes"] {
		t.Fatalf("flags: %+v %+v", got.values, got.set)
	}
	// A double-quoted argument is data even when it looks like a flag; a
	// lone quoted value loses its quotes, a longer one is kept as typed.
	got, err = parse(`"--not a flag" --text say "--hello  there" now --word "two words"`)
	if err != nil || got.positional(0) != "--not a flag" || got.values["--text"] != `say "--hello  there" now` || got.values["--word"] != "two words" {
		t.Fatalf("quoting: %+v %v", got, err)
	}
	// Apostrophes never quote, wherever they sit; neither does a double
	// quote inside or at the end of a bare word, nor a trailing backslash.
	got, err = parse(`'tis it's 5" wide\ for users' --yes`)
	if err != nil || got.positional(0) != `'tis it's 5" wide\ for users'` || !got.set["--yes"] {
		t.Fatalf("plain punctuation: %+v %v", got, err)
	}
	// "-y" is a word wherever --yes is not a flag.
	if got, err = parseSquadTail("/cmd verb accept -y here", nil); err != nil || got.positional(0) != "accept -y here" {
		t.Fatalf("-y as a word: %+v %v", got, err)
	}
	if got, err = parseSquadTail("/cmd", nil); err != nil || len(got.pos) != 0 {
		t.Fatalf("no tail: %+v %v", got, err)
	}
	for _, bad := range []string{
		"x --bogus", "x --word", "x --word --yes", "x --text", "x --word=one", `x --word ""`,
		`x "left open --word one`, `x "glued"text`, "x --word one two", "x --word one --yes two",
	} {
		if _, err := parse(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
	// A bare word after a flag that takes no value is still a positional.
	if got, err = parse("--yes card-1"); err != nil || got.positional(0) != "card-1" {
		t.Fatalf("positional after a bool flag: %+v %v", got, err)
	}
}

func TestBoardCommand_CreateWithFields(t *testing.T) {
	_, store, _ := withSquadFixtures(t)
	cli := &ChatCLI{}

	out := captureStdout(t, func() {
		cli.handleBoardCommand("/board create Fix login --description add tests and docs --assignee coder --column doing")
	})
	card, err := store.Get("card-1")
	if err != nil || card.Title != "Fix login" || card.Description != "add tests and docs" || card.Assignee != "coder" || card.Column != "doing" {
		t.Fatalf("create with fields: %+v %v", card, err)
	}
	if !strings.Contains(out, "card-1") || !strings.Contains(out, "doing") {
		t.Fatalf("confirmation must name the card and its column: %q", out)
	}
	cli.handleBoardCommand(`/board create "Handle --verbose and -y flags"`)
	if card, _ = store.Get("card-2"); card.Title != "Handle --verbose and -y flags" || card.Column != "backlog" {
		t.Fatalf("quoted title: %+v", card)
	}

	// None of these may create a card: each is reported instead.
	squadOutputs(t, cli.handleBoardCommand, map[string]string{
		"/board create Thing --bogus x":        "--bogus",
		"/board create Thing --assignee":       "--assignee",
		"/board create Thing --column nowhere": "nowhere",
		"/board create --assignee coder":       "/board create",
	})
	if cards, _ := store.List(""); len(cards) != 2 {
		t.Fatalf("a rejected create still made a card: %+v", cards)
	}
}

func TestBoardCommand_EditUnassignLink(t *testing.T) {
	_, store, _ := withSquadFixtures(t)
	cli := &ChatCLI{}
	cli.handleBoardCommand("/board create Old title --assignee coder")

	cli.handleBoardCommand("/board edit card-1 New title")
	cli.handleBoardCommand("/board edit card-1 --description must pass CI")
	if card, _ := store.Get("card-1"); card.Title != "New title" || card.Description != "must pass CI" {
		t.Fatalf("edit: %+v", card)
	}
	cli.handleBoardCommand("/board edit card-1 --title Third --description both at once")
	cli.handleBoardCommand("/board unassign card-1")
	cli.handleBoardCommand("/board link card-1 run-7")
	cli.handleBoardCommand("/board link card-1 --run run-8 --job job-3")
	want, _ := store.Get("card-1")
	if want.Title != "Third" || want.Description != "both at once" || want.Assignee != "" ||
		strings.Join(want.RunIDs, ",") != "run-7,run-8" || strings.Join(want.JobIDs, ",") != "job-3" {
		t.Fatalf("edit/unassign/link: %+v", want)
	}

	// Refused: each says why and leaves the card as it was.
	squadOutputs(t, cli.handleBoardCommand, map[string]string{
		"/board edit":                    "/board edit <id>",
		"/board edit card-1":             "/board edit <id>",
		"/board edit card-1 --nope x":    "--nope",
		"/board edit card-99 Ghost":      "card-99",
		"/board unassign":                "Usage: /board",
		"/board unassign card-1 extra":   "Usage: /board",
		"/board unassign card-1 --force": "--force",
		"/board unassign card-99":        "card-99",
		"/board link":                    "/board link <id>",
		"/board link card-1":             "/board link <id>",
		"/board link card-1 --run":       "--run",
		"/board link card-1 run-1 run-2": "/board link <id>",
		"/board link card-99 run-1":      "card-99",
	})
	if got, _ := store.Get("card-1"); got.Title != want.Title || len(got.RunIDs) != 2 || len(got.JobIDs) != 1 {
		t.Fatalf("a refused command changed the card: %+v", got)
	}
}

func TestBoardCommand_DeleteNeedsYes(t *testing.T) {
	_, store, _ := withSquadFixtures(t)
	cli := &ChatCLI{}
	cli.handleBoardCommand("/board create Disposable card")
	cli.handleBoardCommand("/board create Second card")
	cli.handleBoardCommand("/board create Survivor")

	out := captureStdout(t, func() { cli.handleBoardCommand("/board delete card-1") })
	if _, err := store.Get("card-1"); err != nil {
		t.Fatalf("delete without --yes removed the card: %v", err)
	}
	if !strings.Contains(out, "Disposable card") || !strings.Contains(out, "/board delete card-1 --yes") {
		t.Fatalf("preview must show the card and how to confirm: %q", out)
	}
	squadOutputs(t, cli.handleBoardCommand, map[string]string{
		"/board delete card-1 --yes":        "card-1",
		"/board rm card-2 -y":               "card-2",
		"/board delete":                     "/board delete <id>",
		"/board delete card-3 --force":      "--force",
		"/board delete card-3 card-4 --yes": "/board delete <id>",
		"/board delete card-99 --yes":       "card-99",
	})
	if cards, _ := store.List(""); len(cards) != 1 || cards[0].ID != "card-3" {
		t.Fatalf("expected only card-3 to survive: %+v", cards)
	}
}

// A person's board edit must refresh the knowledge graph exactly as a tool
// edit does; reads and refused commands must not.
func TestBoardCommand_MutationsMarkGraphDirty(t *testing.T) {
	withSquadFixtures(t)
	cli := &ChatCLI{memoryStore: workspace.NewMemoryStore(t.TempDir(), zap.NewNop())}
	var dirty atomic.Int32
	cli.memoryStore.Manager().OnGraphDirty(func() { dirty.Add(1) })

	steps := []struct {
		line   string
		mutate bool
	}{
		{"/board create First card", true},
		{"/board list", false},
		{"/board edit card-1 Renamed", true},
		{"/board assign card-1 coder", true},
		{"/board unassign card-1", true},
		{"/board note card-1 looks good", true},
		{"/board link card-1 run-1", true},
		{"/board move card-1 review", true},
		{"/board move card-1 nowhere", false},
		{"/board move card-99 done", false},
		{"/board delete card-1", false},
		{"/board delete card-1 --yes", true},
		{"/board archive", true},
	}
	for _, step := range steps {
		before := dirty.Load()
		cli.handleBoardCommand(step.line)
		if got := dirty.Load() - before; (got == 1) != step.mutate || got > 1 {
			t.Errorf("%q: graph marked dirty %d time(s), mutation=%v", step.line, got, step.mutate)
		}
	}
}

func TestSplitMailSend(t *testing.T) {
	onBoard := func(id string) bool { return id == "card-2" }
	cases := []struct {
		line, card, text string
		wantErr          bool
	}{
		{line: "coder fix the build", text: "fix the build"},
		{line: "coder --card card-2 fix the build", card: "card-2", text: "fix the build"},
		{line: "coder --card card-9 fix the build", card: "card-9", text: "fix the build"},
		{line: "coder fix the build --card card-2", card: "card-2", text: "fix the build"},
		// At the end, --card is a flag only for a card that exists.
		{line: "coder remember to pass --card card-3", text: "remember to pass --card card-3"},
		{line: "coder the flag is --card", text: "the flag is --card"},
		// Prose keeps its own dashes and quotes, open ones included.
		{line: "coder push with --force and -y when ready", text: "push with --force and -y when ready"},
		{line: `coder he said "wait and don't stop`, text: `he said "wait and don't stop`},
		{line: "coder --card card-2", card: "card-2"},
		{line: "coder"},
		{line: "coder --crad card-2 fix it", wantErr: true},
		{line: "coder --card", wantErr: true},
		{line: "coder --card --card", wantErr: true},
		{line: "--card card-2 coder hello", wantErr: true},
		{line: "-coder hello", wantErr: true},
	}
	for _, c := range cases {
		to, card, text, err := splitMailSend(strings.Fields(c.line), onBoard)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err=%v", c.line, err)
		} else if !c.wantErr && (to != "coder" || card != c.card || text != c.text) {
			t.Errorf("%q: got to=%q card=%q text=%q", c.line, to, card, text)
		}
	}
	if to, _, text, err := splitMailSend(nil, onBoard); to != "" || text != "" || err != nil {
		t.Errorf("empty tail: %q %q %v", to, text, err)
	}
}

func TestMailCommand_SendCardPeekHistory(t *testing.T) {
	_, _, bus := withSquadFixtures(t)
	cli := &ChatCLI{}

	out := captureStdout(t, func() { cli.handleMailCommand("/mail send coder --card card-2 rerun the flaky test") })
	msgs := bus.Peek("coder")
	if len(msgs) != 1 || msgs[0].CardID != "card-2" || msgs[0].Text != "rerun the flaky test" || msgs[0].From != "user" {
		t.Fatalf("send with card: %+v", msgs)
	}
	if !strings.Contains(out, "card-2") {
		t.Fatalf("confirmation must name the card: %q", out)
	}
	// A mistyped flag or an empty text must mail nothing.
	squadOutputs(t, cli.handleMailCommand, map[string]string{
		"/mail send coder --crad card-2 hello": "--crad",
		"/mail send coder --card card-2":       "/mail send <agent>",
		"/mail send coder --card":              "--card",
	})
	if n := bus.Pending()["coder"]; n != 1 {
		t.Fatalf("a rejected send was mailed: %d pending", n)
	}
	cli.handleMailCommand("/mail send reviewer " + strings.Repeat("detail ", 30) + "END")

	// peek shows the whole text and leaves the queue alone.
	out = captureStdout(t, func() { cli.handleMailCommand("/mail peek reviewer") })
	if !strings.Contains(out, "END") || strings.Contains(out, "rerun the flaky") {
		t.Fatalf("peek reviewer: %q", out)
	}
	out = captureStdout(t, func() { cli.handleMailCommand("/mail peek") })
	if !strings.Contains(out, "END") || !strings.Contains(out, "rerun the flaky test") || !strings.Contains(out, "[card-2]") {
		t.Fatalf("peek all: %q", out)
	}
	if pending := bus.Pending(); pending["coder"] != 1 || pending["reviewer"] != 1 {
		t.Fatalf("peek drained the queue: %+v", pending)
	}
	squadOutputs(t, cli.handleMailCommand, map[string]string{
		"/mail peek tester": "tester",
		"/mail peek a b":    "Usage: /mail",
		"/mail peek --all":  "--all",
		"/mail history":     "rerun the flaky",
	})

	// history limit: newest first.
	out = captureStdout(t, func() { cli.handleMailCommand("/mail history 1") })
	if !strings.Contains(out, "reviewer") || strings.Contains(out, "rerun the flaky") {
		t.Errorf("history 1: expected only the newest message, got %q", out)
	}
	for _, line := range []string{"/mail history zero", "/mail history 0", "/mail list 1 2", "/mail history --max 3"} {
		out = captureStdout(t, func() { cli.handleMailCommand(line) })
		if strings.Contains(out, "reviewer") || strings.TrimSpace(out) == "" {
			t.Errorf("%q: must report the problem and list nothing, got %q", line, out)
		}
	}

	bus.Drain("coder")
	bus.Drain("reviewer")
	if out = captureStdout(t, func() { cli.handleMailCommand("/mail peek") }); strings.TrimSpace(out) == "" {
		t.Fatal("peek with nothing queued printed nothing")
	}
}

func TestLiveMailAdapter_Overview(t *testing.T) {
	_, _, bus := withSquadFixtures(t)
	a := newLiveMailAdapter(bus)

	if out, err := a.Overview(); err != nil || !strings.Contains(out, "no pending mail") {
		t.Fatalf("empty overview: %q %v", out, err)
	}
	for _, to := range []string{"reviewer", "coder", "coder"} {
		if _, err := bus.Send("orchestrator", to, "", "ping"); err != nil {
			t.Fatal(err)
		}
	}
	out, err := a.Overview()
	if err != nil || !strings.Contains(out, "coder: 2\nreviewer: 1\n") || !strings.Contains(out, "2 inbox(es)") {
		t.Fatalf("overview: %q %v", out, err)
	}
	if pending := bus.Pending(); pending["coder"] != 2 {
		t.Fatalf("overview drained the queue: %+v", pending)
	}
}

// squadRun starts a live worker run in the fixture registry.
func squadRun(t *testing.T, reg *runs.Registry) *runs.Run {
	t.Helper()
	_, run := reg.Begin(context.Background(), runs.Info{Kind: runs.KindWorker, Agent: "coder", Task: "long task"})
	t.Cleanup(func() { run.End(nil) })
	return run
}

// agentsWithin runs an /agents line and fails, instead of hanging the
// suite, when the command holds longer than it may.
func agentsWithin(ctx context.Context, t *testing.T, cli *ChatCLI, line string) string {
	t.Helper()
	return captureStdout(t, func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			cli.handleAgentsCommand(ctx, line)
		}()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Errorf("%q still holding after 30s", line)
		}
	})
}

func TestAgentsCommand_WaitReportsWithoutHolding(t *testing.T) {
	reg, _, _ := withSquadFixtures(t)
	cli := &ChatCLI{}
	id := squadRun(t, reg).ID()
	bg := context.Background()

	// Default: state plus how to follow, with the run still going and the
	// context never cancelled — returning at all is the assertion.
	out := agentsWithin(bg, t, cli, "/agents wait "+id)
	if !strings.Contains(out, id) || !strings.Contains(out, "/agents wait "+id+" --block") || !strings.Contains(out, "/agents transcript "+id) {
		t.Fatalf("wait must show the run and how to follow it: %q", out)
	}

	// Surfaces with nothing to hold decline --block instead of stalling.
	for want, set := range map[string][]func(bool){
		"--block is not available on this surface": {func(v bool) { cli.unattended = v }, rpcCaptureActive.Store},
		"while a turn is running":                  {cli.isExecuting.Store},
	} {
		for _, flip := range set {
			flip(true)
			out := agentsWithin(bg, t, cli, "/agents wait "+id+" --block --timeout 10m")
			flip(false)
			if !strings.Contains(out, want) || !strings.Contains(out, id) {
				t.Errorf("--block must be declined with %q: %q", want, out)
			}
		}
	}

	for line, want := range map[string]string{
		"/agents wait":                                   "/agents wait <id>",
		"/agents wait " + id + " extra":                  "/agents wait <id>",
		"/agents wait " + id + " --forever":              "--forever",
		"/agents wait " + id + " --timeout 5m":           "--block",
		"/agents wait " + id + " --block --timeout soon": "soon",
		"/agents wait run-999 --block":                   "run-999",
	} {
		if out := agentsWithin(bg, t, cli, line); !strings.Contains(out, want) {
			t.Errorf("%q: output %q lacks %q", line, out, want)
		}
	}
	if info, _ := reg.Get(id); info.Status != runs.StatusRunning {
		t.Fatalf("waiting changed the run: %+v", info)
	}
}

func TestAgentsCommand_WaitBlock(t *testing.T) {
	reg, _, _ := withSquadFixtures(t)
	cli := &ChatCLI{}
	run := squadRun(t, reg)
	id := run.ID()
	bg := context.Background()

	// Interrupted: the command context ends (what Ctrl+C does in the REPL)
	// and the hold lets go, leaving the run alone.
	ctx, cancel := context.WithTimeout(bg, 50*time.Millisecond)
	defer cancel()
	out := agentsWithin(ctx, t, cli, "/agents wait "+id+" --block")
	if !strings.Contains(out, "keeps running") {
		t.Fatalf("interrupted hold must say the agent keeps running: %q", out)
	}
	if info, _ := reg.Get(id); info.Status != runs.StatusRunning || cli.cmdInterrupt != nil {
		t.Fatalf("interrupting the wait must leave the run going and no registration behind: %+v", info)
	}

	// Bounded: the timeout gives the prompt back with the current state.
	out = agentsWithin(bg, t, cli, "/agents wait "+id+" --block --timeout 300ms")
	if !strings.Contains(out, "300ms") || !strings.Contains(out, "still running") {
		t.Fatalf("timed-out hold: %q", out)
	}

	// Finished while held: the final state is shown. Once finished there is
	// nothing left to hold or to follow.
	go run.End(nil)
	if out = agentsWithin(bg, t, cli, "/agents wait "+id+" --block"); !strings.Contains(out, "completed") {
		t.Fatalf("finished hold: %q", out)
	}
	if out = agentsWithin(bg, t, cli, "/agents wait "+id); !strings.Contains(out, "completed") || strings.Contains(out, "--block") {
		t.Fatalf("wait on a finished run: %q", out)
	}
}

func TestAgentsCommand_Transcript(t *testing.T) {
	reg, _, _ := withSquadFixtures(t)
	cli := &ChatCLI{}
	run := squadRun(t, reg)
	id := run.ID()
	for _, step := range []string{"first step", "second step", "third step"} {
		run.Note(step)
	}
	transcript := func(line string) { cli.handleAgentsCommand(context.Background(), line) }

	out := captureStdout(t, func() { transcript("/agents transcript " + id) })
	if !strings.Contains(out, "first step") || !strings.Contains(out, "third step") || !strings.Contains(out, id) {
		t.Fatalf("transcript: %q", out)
	}
	out = captureStdout(t, func() { transcript("/agents transcript " + id + " --tail 1") })
	if strings.Contains(out, "second step") || !strings.Contains(out, "third step") {
		t.Fatalf("transcript --tail 1: %q", out)
	}
	squadOutputs(t, transcript, map[string]string{
		"/agents transcript":                       "/agents transcript <id>",
		"/agents transcript " + id + " --tail":     "--tail",
		"/agents transcript " + id + " --tail abc": "abc",
		"/agents transcript " + id + " --tail 0":   "--tail",
		"/agents transcript " + id + " --lines 3":  "--lines",
		"/agents transcript run-999":               "run-999",
	})

	squadOutputs(t, transcript, map[string]string{"/agents transcript " + squadRun(t, reg).ID(): "(none)"})
}

func TestSquadCompleters_NewVerbsAndFlags(t *testing.T) {
	withSquadFixtures(t)
	cli := &ChatCLI{}
	_, run := runs.Default().Begin(context.Background(), runs.Info{Kind: runs.KindWorker, Agent: "coder", Task: "t"})
	defer run.End(nil)
	id := run.ID()

	cases := []struct {
		line  string
		want  []string // must be offered
		never []string // must not be offered; "*" = nothing at all
	}{
		{line: "/agents ", want: []string{"wait", "transcript"}},
		{line: "/agents info ", want: []string{id}},
		{line: "/agents stop ", want: []string{id}},
		{line: "/agents wait ", want: []string{id}},
		{line: "/agents transcript ", want: []string{id}},
		{line: "/agents wait " + id + " ", want: []string{"--block", "--timeout"}},
		{line: "/agents wait " + id + " --block --", want: []string{"--timeout"}, never: []string{"--block"}},
		{line: "/agents transcript " + id + " --", want: []string{"--tail"}},
		{line: "/agents show " + id + " ", never: []string{"*"}},
		{line: "/board ", want: []string{"edit", "unassign", "link", "delete"}},
		{line: "/board create Fix it --", want: []string{"--description", "--assignee", "--column"}},
		{line: "/board create Fix it ", never: []string{"*"}}, // a title in progress is not interrupted
		{line: "/board create Fix it --column ", want: []string{"review"}},
		{line: "/board create Fix it --column re", want: []string{"review"}, never: []string{"done"}},
		{line: "/board edit card-1 ", want: []string{"--title", "--description"}},
		{line: "/board link card-1 --", want: []string{"--run", "--job"}},
		{line: "/board delete card-1 ", want: []string{"--yes"}},
		{line: "/mail ", want: []string{"peek", "history"}},
		{line: "/mail peek ", want: []string{"coder"}},
		{line: "/mail send coder --", want: []string{"--card"}},
		{line: "/mail send coder --card ", never: []string{"--card"}},
	}
	for _, c := range cases {
		suggest := cli.getAgentsSuggestions
		switch {
		case strings.HasPrefix(c.line, "/board"):
			suggest = cli.getBoardSuggestions
		case strings.HasPrefix(c.line, "/mail"):
			suggest = cli.getMailSuggestions
		}
		got := map[string]bool{}
		for _, s := range suggest(docAt(c.line)) {
			got[s.Text] = true
		}
		for _, w := range c.want {
			if !got[w] {
				t.Errorf("%q does not offer %s: %v", c.line, w, got)
			}
		}
		for _, n := range c.never {
			if got[n] || (n == "*" && len(got) > 0) {
				t.Errorf("%q must not offer %s: %v", c.line, n, got)
			}
		}
	}
}
