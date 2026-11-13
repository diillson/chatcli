/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// saveParkAged writes a snapshot created age ago, optionally tied to a job.
func saveParkAged(t *testing.T, age time.Duration, jobID string) string {
	t.Helper()
	snap := &park.Snapshot{Token: park.NewToken(), CreatedAt: time.Now().Add(-age), SchedulerJobID: jobID,
		Park: park.Request{Mode: park.ModeDelay, Delay: time.Hour}}
	if err := snap.Save(); err != nil {
		t.Fatal(err)
	}
	return snap.Token
}

// enqueueParkJob creates a far-future job, cancelled when terminal is set.
func enqueueParkJob(t *testing.T, cli *ChatCLI, terminal bool) string {
	t.Helper()
	j := scheduler.NewJob("park-test-"+park.NewToken()[:8], scheduler.Owner{Kind: scheduler.OwnerUser, ID: "test"},
		scheduler.Schedule{Kind: scheduler.ScheduleRelative, Relative: 10 * time.Hour},
		scheduler.Action{Type: scheduler.ActionShell, Payload: map[string]any{"command": "echo x"}})
	j.DangerousConfirmed = true
	created, err := cli.scheduler.Enqueue(context.Background(), j)
	if err == nil && terminal {
		err = cli.scheduler.Cancel(created.ID, "test", j.Owner)
	}
	if err != nil {
		t.Fatalf("job: %v", err)
	}
	return string(created.ID)
}

func parkOnDisk(token string) bool {
	_, err := park.Load(token)
	return err == nil
}

func sortedJoin(in ...string) string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return strings.Join(out, ",")
}

// parkFleet is one park of every kind the sweep must tell apart.
type parkFleet struct {
	cli                                   *ChatCLI
	waiting, queued, inTurn, remote       string // live
	finished, orphan, vanished, youngDead string // dead
}

func newParkFleet(t *testing.T) parkFleet {
	t.Helper()
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := newCLIWithScheduler(t)
	cli.logger = zap.NewNop()
	const old = 48 * time.Hour
	f := parkFleet{cli: cli}
	f.waiting = saveParkAged(t, old, enqueueParkJob(t, cli, false))
	f.finished = saveParkAged(t, old, enqueueParkJob(t, cli, true))
	f.orphan = saveParkAged(t, old, "")
	f.vanished = saveParkAged(t, old, "job-that-never-existed")
	f.youngDead = saveParkAged(t, time.Minute, "")
	// Its job fired (the scheduler calls it finished) and the resume is
	// queued: the snapshot is what that resume will load.
	f.queued = saveParkAged(t, old, enqueueParkJob(t, cli, true))
	cli.pendingResumeQueue = []string{f.queued}
	// A turn of an unattended surface is blocked on this one.
	f.inTurn = saveParkAged(t, old, "")
	cli.registerParkWaiter(f.inTurn)
	// Another live process owns this one; its job is in that process.
	f.remote = saveParkAged(t, old, "job-in-another-process")
	setRemoteParkProvider(&remoteParkProvider{owner: func(token string) (remoteParkOwner, bool) {
		return remoteParkOwner{Instance: "other"}, token == f.remote
	}})
	t.Cleanup(func() { setRemoteParkProvider(nil) })
	return f
}

func (f parkFleet) live() []string { return []string{f.waiting, f.queued, f.inTurn, f.remote} }

// The sweep removes what nobody waits on and never a park an agent can
// still be woken through — including the two the scheduler alone would
// call dead: a queued resume and a park another process owns. An age only
// narrows it; it never makes a waiting park removable.
func TestSweepDeadParksNeverRemovesAWaitingPark(t *testing.T) {
	f := newParkFleet(t)
	rep := f.cli.sweepDeadParks(24 * time.Hour)
	if sortedJoin(rep.removed...) != sortedJoin(f.finished, f.orphan, f.vanished) || !parkOnDisk(f.youngDead) {
		t.Fatalf("with an age only the old dead parks go: %v (young on disk: %v)", rep.removed, parkOnDisk(f.youngDead))
	}
	if sortedJoin(rep.live...) != sortedJoin(f.live()...) {
		t.Fatalf("old waiting parks must be reported, not removed: %v", rep.live)
	}
	rep = f.cli.sweepDeadParks(0)
	if sortedJoin(rep.removed...) != f.youngDead || sortedJoin(rep.live...) != sortedJoin(f.live()...) {
		t.Fatalf("without an age: removed %v live %v", rep.removed, rep.live)
	}
	for _, token := range f.live() {
		if !parkOnDisk(token) {
			t.Errorf("waiting park %s was deleted", parkShort(token))
		}
	}
	if parkOnDisk(f.finished) || parkOnDisk(f.orphan) || parkOnDisk(f.vanished) || parkOnDisk(f.youngDead) {
		t.Error("a dead park is still on disk")
	}
	// The remote owner dies without cleaning up: its heartbeat goes stale,
	// the hub stops reporting it alive, and its park is prunable.
	setRemoteParkProvider(&remoteParkProvider{owner: func(string) (remoteParkOwner, bool) { return remoteParkOwner{}, false }})
	if rep = f.cli.sweepDeadParks(0); sortedJoin(rep.removed...) != f.remote || parkOnDisk(f.remote) {
		t.Fatalf("a dead owner's park must become prunable: %v", rep.removed)
	}
}

// /parked prune and @park prune are one sweep: the command prints its
// counts, the tool names what it left alone and how to end one on purpose.
func TestParkedPruneCommandAndToolShareTheSweep(t *testing.T) {
	f := newParkFleet(t)
	if out := captureStdout(t, f.cli.parkPrune); !strings.Contains(out, "4 removed, 4 still pending") {
		t.Fatalf("/parked prune: %q", out)
	}
	dead, young := saveParkAged(t, 48*time.Hour, ""), saveParkAged(t, time.Minute, "")
	plugins.SetParkCanceller(newLiveParkAdapter(f.cli))
	t.Cleanup(func() { plugins.SetParkCanceller(nil) })
	p := plugins.NewBuiltinParkPlugin()

	// Nothing is swept when the call is not understood.
	for _, argv := range [][]string{
		{"prune", "--older_than", "24"},   // flattened number: no unit
		{"prune", "--older_than"},         // flag without its value
		{"prune", "--older_than", "soon"}, // not an age
		{"prune", "--older_than", "-1h"},  // would put the cutoff in the future
		{"prune", "--token", "abc"},       // not an argument of prune
	} {
		if res, err := p.Execute(context.Background(), argv); err == nil {
			t.Errorf("%v must be an error, got %q", argv, res)
		}
	}
	if !parkOnDisk(dead) || !parkOnDisk(young) {
		t.Fatal("a rejected prune deleted a snapshot")
	}

	// Flattened {"cmd":"prune","args":{"older_than":"24h"}}.
	res, err := p.Execute(context.Background(), []string{"prune", "--older_than", "24h"})
	if err != nil || !strings.HasPrefix(res, "pruned 1 parked snapshot(s)") || !strings.Contains(res, "older than 24h0m0s") {
		t.Fatalf("prune older_than: %q %v", res, err)
	}
	for _, token := range f.live() {
		if !strings.Contains(res, parkShort(token)) || !parkOnDisk(token) {
			t.Errorf("waiting park %s must be kept and named: %q", parkShort(token), res)
		}
	}
	if !strings.Contains(res, "still waiting") || !strings.Contains(res, `"cmd":"discard"`) {
		t.Errorf("the result must say why they were kept and how to end one: %q", res)
	}
	if parkOnDisk(dead) || !parkOnDisk(young) {
		t.Fatalf("old dead gone=%v, young kept=%v", !parkOnDisk(dead), parkOnDisk(young))
	}
	// Flattened {"cmd":"prune"} takes the young one too; whole days parse.
	if res, err = p.Execute(context.Background(), []string{"prune"}); err != nil || !strings.HasPrefix(res, "pruned 1 parked snapshot(s) nobody was waiting on (all snapshots)") {
		t.Fatalf("prune: %q %v", res, err)
	}
	if res, err = p.Execute(context.Background(), []string{"gc", "--older_than", "7d"}); err != nil || !strings.Contains(res, "older than 168h0m0s") {
		t.Fatalf("prune by days: %q %v", res, err)
	}
	if _, err := (*liveParkAdapter)(nil).PrunePark(""); err == nil {
		t.Error("an unbound adapter must refuse")
	}
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	if out := captureStdout(t, f.cli.parkPrune); !strings.Contains(out, i18n.T("park.list.empty")) {
		t.Fatalf("nothing parked: %q", out)
	}
}

func TestParseParkNoteArgs(t *testing.T) {
	for in, want := range map[string]parkNoteArgs{
		"hello there":               {text: "hello there"},
		"--token ab12 switch to 5m": {token: "ab12", hasToken: true, text: "switch to 5m"},
		"--token=ab12 switch":       {token: "ab12", hasToken: true, text: "switch"},
		"--token ab12":              {token: "ab12", hasToken: true},
		"-- --verbose is the flag":  {text: "--verbose is the flag"},
		"use --token later":         {text: "use --token later"},
		"-n is not a long flag":     {text: "-n is not a long flag"},
		"":                          {},
	} {
		if got, err := parseParkNoteArgs(in); err != nil || got != want {
			t.Errorf("%q: got %+v %v, want %+v", in, got, err, want)
		}
	}
	for in, want := range map[string]string{
		"--token":            i18n.T("execparity.flag_needs_value", "--token"),
		"--token --other x":  i18n.T("execparity.flag_needs_value", "--token"),
		"--token= hi":        i18n.T("execparity.flag_needs_value", "--token"),
		"--tokne ab12 hi":    i18n.T("leftovers.park_note.unknown_flag", "--tokne"),
		"--tokenab12 hi you": i18n.T("leftovers.park_note.unknown_flag", "--tokenab12"),
	} {
		if _, err := parseParkNoteArgs(in); err == nil || err.Error() != want {
			t.Errorf("%q: got %v, want %q", in, err, want)
		}
	}
}

func parkDirectives(t *testing.T, token string) string {
	t.Helper()
	snap, err := park.Load(token)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(snap.PendingUserDirectives, "|")
}

// /park-note --token reaches the named park (by unique prefix, like
// /resume); an unknown or ambiguous token is reported and records nothing;
// the form without a flag still goes to the newest park of the session.
func TestParkNoteWithExplicitToken(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := &ChatCLI{logger: zap.NewNop()}
	older := newWaitingPark(t, cli)
	// Two tokens sharing a first character make a prefix ambiguous.
	newest := newWaitingPark(t, cli)
	for newest[0] != older[0] {
		cli.unregisterActivePark(newest)
		_ = park.Delete(newest)
		newest = newWaitingPark(t, cli)
	}
	foreign := saveParkAged(t, time.Minute, "") // parked by another session
	for foreign[0] == older[0] {
		_ = park.Delete(foreign)
		foreign = saveParkAged(t, time.Minute, "")
	}
	note := func(rest string) string { return captureStdout(t, func() { cli.handleParkNoteCommand(rest) }) }

	if out := note("--token " + older + " go to 5m cycles"); !strings.Contains(out, i18n.T("leftovers.park_note.queued", parkShort(older), parkShort(older))) {
		t.Fatalf("token form: %q", out)
	}
	if out := note("--token=" + foreign[:10] + " from another session"); !strings.Contains(out, parkShort(foreign)) {
		t.Fatalf("prefix of a park this session did not create: %q", out)
	}
	for rest, want := range map[string]string{
		"--token zzzz nobody home":         i18n.T("park.resolve.not_found", "zzzz"),
		"--token " + older[:1] + " which?": "ambiguous",
		"--tokne " + older + " typo":       i18n.T("leftovers.park_note.unknown_flag", "--tokne"),
		"--token":                          i18n.T("execparity.flag_needs_value", "--token"),
		"--token " + older:                 i18n.T("leftovers.park_note.usage"),
	} {
		if out := note(rest); !strings.Contains(out, want) {
			t.Errorf("%q: want %q in %q", rest, want, out)
		}
	}
	// No flag: the newest park of this session, as before.
	if out := note("-- --keep going"); !strings.Contains(out, i18n.T("park.directive.queued", "18:44:09", parkShort(newest))) {
		t.Fatalf("no-flag form: %q", out)
	}
	if a, b, c := parkDirectives(t, older), parkDirectives(t, newest), parkDirectives(t, foreign); a != "go to 5m cycles" || b != "--keep going" || c != "from another session" {
		t.Fatalf("each park must hold exactly its own note: %q %q %q", a, b, c)
	}
	// Completion: the flag first, then the live tokens, nothing in the text.
	for line, want := range map[string]string{
		"/park-note ":                       "--token",
		"/park-note --to":                   "--token",
		"/park-note --token " + foreign[:3]: foreign[:12],
		"/park-note hello ":                 "",
		"/park-note --token abc hello ":     "",
		"/park-note h":                      "",
	} {
		suggs, routed := cli.routeSlashPrefix(line, docWithCursor(line, len(line)))
		if got := strings.Join(suggestTexts(suggs), " "); !routed || got != want {
			t.Errorf("%q: routed=%v got %q, want %q", line, routed, got, want)
		}
	}
}
