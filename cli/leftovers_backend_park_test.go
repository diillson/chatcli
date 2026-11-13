/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"os"
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

// saveParkOwnedBy writes an old snapshot that records its owner process on
// this host, with a job no scheduler here knows.
func saveParkOwnedBy(t *testing.T, pid int) string {
	t.Helper()
	snap := &park.Snapshot{Token: park.NewToken(), CreatedAt: time.Now().Add(-48 * time.Hour),
		SchedulerJobID: "job-in-its-owner", OwnerPID: pid, OwnerHost: parkHostname(),
		Park: park.Request{Mode: park.ModeDelay, Delay: time.Hour}}
	if err := snap.Save(); err != nil {
		t.Fatal(err)
	}
	return snap.Token
}

// Pids the fleet's snapshots record; parkOwnerAlive is stubbed over them.
const (
	fleetLivePID = 2000001
	fleetDeadPID = 2000002
)

// parkFleet is one park of every kind the sweep must tell apart.
type parkFleet struct {
	cli                                         *ChatCLI
	waiting, queued, inTurn, remote, ownerAlive string // an agent can still be woken
	finished, resumed, ownerDead, letGo         string // proven finished
	orphan, vanished, young                     string // nothing proves either
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
	// No job at all, and a job this scheduler never heard of: either may
	// be waiting in a process this one cannot see.
	f.orphan = saveParkAged(t, old, "")
	f.vanished = saveParkAged(t, old, "job-that-never-existed")
	// Its job is finished, but it was parked seconds ago.
	f.young = saveParkAged(t, time.Second, enqueueParkJob(t, cli, true))
	// Its job fired (the scheduler calls it finished) and the resume is
	// queued: the snapshot is what that resume will load.
	f.queued = saveParkAged(t, old, enqueueParkJob(t, cli, true))
	cli.pendingResumeQueue = []string{f.queued}
	// A turn of an unattended surface is blocked on this one.
	f.inTurn = saveParkAged(t, old, "")
	cli.registerParkWaiter(f.inTurn)
	// Another live process heartbeats for this one; its job is there.
	f.remote = saveParkAged(t, old, "job-in-another-process")
	setRemoteParkProvider(&remoteParkProvider{owner: func(token string) (remoteParkOwner, bool) {
		return remoteParkOwner{Instance: "other"}, token == f.remote
	}})
	t.Cleanup(func() { setRemoteParkProvider(nil) })
	// A resume of this one completed; it is kept for forensics.
	f.resumed = saveParkAged(t, old, "job-purged-from-history")
	snap, err := park.Load(f.resumed)
	if err == nil {
		snap.LastResumeAt, snap.ResumeState = time.Now().Add(-time.Hour), park.ResumeDone
		err = snap.Save()
	}
	if err != nil {
		t.Fatal(err)
	}
	// The mirror is silent about these three; the process table is not.
	f.ownerAlive = saveParkOwnedBy(t, fleetLivePID)
	f.ownerDead = saveParkOwnedBy(t, fleetDeadPID)
	f.letGo = saveParkOwnedBy(t, os.Getpid()) // parked here, no longer held
	prev := parkOwnerAlive
	parkOwnerAlive = func(pid int) bool { return pid == fleetLivePID }
	t.Cleanup(func() { parkOwnerAlive = prev })
	return f
}

func (f parkFleet) live() []string {
	return []string{f.waiting, f.queued, f.inTurn, f.remote, f.ownerAlive}
}
func (f parkFleet) dead() []string    { return []string{f.finished, f.resumed, f.ownerDead, f.letGo} }
func (f parkFleet) unknown() []string { return []string{f.orphan, f.vanished, f.young} }

// The sweep removes only what is proven finished. It never removes a park
// an agent can still be woken through — including the ones this scheduler
// alone would call dead: a queued resume, a park another process holds —
// nor one it merely knows nothing about. An age only narrows it.
func TestSweepDeadParksRemovesOnlyTheProvenFinished(t *testing.T) {
	f := newParkFleet(t)
	rep := f.cli.sweepDeadParks(24 * time.Hour)
	if sortedJoin(rep.removed...) != sortedJoin(f.dead()...) {
		t.Fatalf("removed %v, want the four proven finished", parkShorts(rep.removed))
	}
	if sortedJoin(rep.live...) != sortedJoin(f.live()...) || sortedJoin(rep.unknown...) != sortedJoin(f.orphan, f.vanished) {
		t.Fatalf("kept: live %v unknown %v (the young park is outside the age)", parkShorts(rep.live), parkShorts(rep.unknown))
	}
	rep = f.cli.sweepDeadParks(0)
	if len(rep.removed) != 0 || sortedJoin(rep.unknown...) != sortedJoin(f.unknown()...) {
		t.Fatalf("without an age: removed %v unknown %v", parkShorts(rep.removed), parkShorts(rep.unknown))
	}
	for _, token := range append(f.live(), f.unknown()...) {
		if !parkOnDisk(token) {
			t.Errorf("park %s was deleted without proof that it is finished", parkShort(token))
		}
	}
	for _, token := range f.dead() {
		if parkOnDisk(token) {
			t.Errorf("finished park %s is still on disk", parkShort(token))
		}
	}
	// The remote owner dies without cleaning up and the hub stops
	// reporting it. That silence alone proves nothing...
	setRemoteParkProvider(nil)
	if rep = f.cli.sweepDeadParks(0); len(rep.removed) != 0 || !parkOnDisk(f.remote) {
		t.Fatalf("a silent mirror is not proof of death: %v", parkShorts(rep.removed))
	}
	// ...but the owner process that is gone from this host is, and so is
	// its job turning terminal once the grace of a young park has passed.
	parkOwnerAlive = func(int) bool { return false }
	if rep = f.cli.sweepDeadParks(0); sortedJoin(rep.removed...) != f.ownerAlive {
		t.Fatalf("a dead owner's park must become prunable: %v", parkShorts(rep.removed))
	}
}

// /parked prune and @park prune are one sweep: the command prints its
// counts, the tool names what it left alone, why, and what can end it.
func TestParkedPruneCommandAndToolShareTheSweep(t *testing.T) {
	f := newParkFleet(t)
	out := captureStdout(t, f.cli.parkPrune)
	if !strings.Contains(out, "4 removed, 8 still pending") || !strings.Contains(out, i18n.T("leftovers.parked.prune_unknown", 3, strings.Join(parkShorts(f.unknown()), ", "))) {
		t.Fatalf("/parked prune: %q", out)
	}
	dead, recent := saveParkOwnedBy(t, fleetDeadPID), saveParkOwnedBy(t, fleetDeadPID)
	snap, err := park.Load(recent)
	if err == nil {
		snap.CreatedAt = time.Now().Add(-time.Hour)
		err = snap.Save()
	}
	if err != nil {
		t.Fatal(err)
	}
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
	if !parkOnDisk(dead) || !parkOnDisk(recent) {
		t.Fatal("a rejected prune deleted a snapshot")
	}

	// Flattened {"cmd":"prune","args":{"older_than":"24h"}}.
	res, err := p.Execute(context.Background(), []string{"prune", "--older_than", "24h"})
	if err != nil || !strings.HasPrefix(res, "pruned 1 parked snapshot(s) proven finished") || !strings.Contains(res, "older than 24h0m0s") {
		t.Fatalf("prune older_than: %q %v", res, err)
	}
	for _, token := range append(f.live(), f.orphan, f.vanished) {
		if !strings.Contains(res, parkShort(token)) || !parkOnDisk(token) {
			t.Errorf("park %s must be kept and named: %q", parkShort(token), res)
		}
	}
	for _, want := range []string{"still waiting", `"cmd":"discard"`, "state is unknown here", "/parked gc"} {
		if !strings.Contains(res, want) {
			t.Errorf("the result must explain what it kept (%q missing): %q", want, res)
		}
	}
	if parkOnDisk(dead) || !parkOnDisk(recent) {
		t.Fatalf("old finished gone=%v, recent kept=%v", !parkOnDisk(dead), parkOnDisk(recent))
	}
	// Flattened {"cmd":"prune"} takes the recent one too; whole days parse.
	if res, err = p.Execute(context.Background(), []string{"prune"}); err != nil || !strings.HasPrefix(res, "pruned 1 parked snapshot(s) proven finished (all snapshots)") {
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

// /parked gc is the human's deliberate override: by age, waiting or not —
// and its help says so.
func TestParkedGCRemovesWaitingParksAndSaysSo(t *testing.T) {
	f := newParkFleet(t)
	out := captureStdout(t, func() { f.cli.handleParkedCommand("/parked gc 24h") })
	if !strings.Contains(out, "11 removed") || parkOnDisk(f.waiting) || parkOnDisk(f.orphan) || !parkOnDisk(f.young) {
		t.Fatalf("/parked gc removes everything older than the age: %q", out)
	}
	help := captureStdout(t, func() { f.cli.handleParkedCommand("/parked help") })
	if !strings.Contains(help, i18n.T("leftovers.parked.usage_gc")) || !strings.Contains(i18n.T("leftovers.parked.usage_gc"), "waiting") {
		t.Fatalf("the help must say gc removes waiting parks: %q", help)
	}
	subs := f.cli.getParkedSuggestions(docWithCursor("/parked g", 9))
	if len(subs) != 1 || subs[0].Description != i18n.T("complete.leftovers.parked.gc") {
		t.Fatalf("completion of gc: %+v", subs)
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
