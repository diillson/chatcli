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
// this host, tied to jobID ("" for a job no scheduler here knows).
func saveParkOwnedBy(t *testing.T, pid int, jobID ...string) string {
	t.Helper()
	snap := &park.Snapshot{Token: park.NewToken(), CreatedAt: time.Now().Add(-48 * time.Hour),
		SchedulerJobID: "job-in-its-owner", OwnerPID: pid, OwnerHost: parkHostname(), OwnerPIDNS: parkPIDNamespace(),
		Park: park.Request{Mode: park.ModeDelay, Delay: time.Hour}}
	if len(jobID) > 0 {
		snap.SchedulerJobID = jobID[0]
	}
	if err := snap.Save(); err != nil {
		t.Fatal(err)
	}
	return snap.Token
}

// saveParkResumed writes a snapshot a resume left behind in state, age ago.
func saveParkResumed(t *testing.T, age time.Duration, state, jobID string) string {
	t.Helper()
	token := saveParkAged(t, age, jobID)
	snap, err := park.Load(token)
	if err == nil {
		snap.LastResumeAt, snap.ResumeState = time.Now().Add(-age/2), state
		err = snap.Save()
	}
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// Pids the fleet's snapshots record; parkOwnerAlive is stubbed over them.
// (The sweep against REAL processes is in park_two_process_test.go; this
// table is the map of the rule, one park per row.)
const (
	fleetLivePID = 2000001
	fleetDeadPID = 2000002
)

// parkFleet is one park of every kind the rule tells apart.
type parkFleet struct {
	cli *ChatCLI
	// an agent can still be woken
	waiting, queued, inTurn, remote, ownerAlive, ownerAliveFired string
	// a resume of it completed: the only finished
	resumed, legacyResumed, recentlyResumed string
	// a resume is owed: it failed, or the job fired and the owner died
	failed, ownerDeadFired string
	// nothing proves anything
	fired, orphan, vanished, ownerDead, letGo string
}

func newParkFleet(t *testing.T) parkFleet {
	t.Helper()
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := newCLIWithScheduler(t)
	cli.logger = zap.NewNop()
	const old = 48 * time.Hour
	f := parkFleet{cli: cli}
	f.waiting = saveParkAged(t, old, enqueueParkJob(t, cli, false))
	// Its job fired and the resume is queued here: the snapshot is what
	// that resume will load.
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
	// The mirror is silent about these; the process table is not. The
	// second one's job has FIRED: its owner is alive with the resume
	// queued, which no job state can tell.
	f.ownerAlive = saveParkOwnedBy(t, fleetLivePID)
	f.ownerAliveFired = saveParkOwnedBy(t, fleetLivePID, enqueueParkJob(t, cli, true))

	f.resumed = saveParkResumed(t, old, park.ResumeDone, "job-purged-from-history")
	f.recentlyResumed = saveParkResumed(t, time.Minute, park.ResumeDone, "")
	// Written by a released version: a resume started, no outcome field.
	f.legacyResumed = saveParkResumed(t, old, "", enqueueParkJob(t, cli, true))

	f.failed = saveParkResumed(t, old, park.ResumeRetry, enqueueParkJob(t, cli, true))
	f.ownerDeadFired = saveParkOwnedBy(t, fleetDeadPID, enqueueParkJob(t, cli, true))

	// A finished job alone, with nobody known to own the park: the wait
	// is over, and whether the agent continued is not known.
	f.fired = saveParkAged(t, old, enqueueParkJob(t, cli, true))
	f.orphan = saveParkAged(t, old, "")
	f.vanished = saveParkAged(t, old, "job-that-never-existed")
	f.ownerDead = saveParkOwnedBy(t, fleetDeadPID)
	f.letGo = saveParkOwnedBy(t, os.Getpid()) // parked here, no longer held
	prev := parkOwnerAlive
	parkOwnerAlive = func(pid int) bool { return pid == fleetLivePID }
	t.Cleanup(func() { parkOwnerAlive = prev })
	return f
}

func (f parkFleet) live() []string {
	return []string{f.waiting, f.queued, f.inTurn, f.remote, f.ownerAlive, f.ownerAliveFired}
}
func (f parkFleet) oldFinished() []string { return []string{f.resumed, f.legacyResumed} }
func (f parkFleet) retry() []string       { return []string{f.failed, f.ownerDeadFired} }
func (f parkFleet) unknown() []string {
	return []string{f.fired, f.orphan, f.vanished, f.ownerDead, f.letGo}
}

// The sweep removes a park only when a resume of it completed. A finished
// job, a dead owner, a silent hub — none of them is that. An age only
// narrows the sweep.
func TestSweepDeadParksRemovesOnlyTheProvenFinished(t *testing.T) {
	f := newParkFleet(t)
	rep := f.cli.sweepDeadParks(24 * time.Hour)
	if sortedJoin(rep.removed...) != sortedJoin(f.oldFinished()...) || !parkOnDisk(f.recentlyResumed) {
		t.Fatalf("removed %v, want the two old completed ones", parkShorts(rep.removed))
	}
	if sortedJoin(rep.live...) != sortedJoin(f.live()...) || sortedJoin(rep.retry...) != sortedJoin(f.retry()...) || sortedJoin(rep.unknown...) != sortedJoin(f.unknown()...) {
		t.Fatalf("kept: live %v retry %v unknown %v", parkShorts(rep.live), parkShorts(rep.retry), parkShorts(rep.unknown))
	}
	rep = f.cli.sweepDeadParks(0)
	if sortedJoin(rep.removed...) != f.recentlyResumed {
		t.Fatalf("without an age the recent completed one goes too: %v", parkShorts(rep.removed))
	}
	for _, token := range append(append(f.live(), f.retry()...), f.unknown()...) {
		if !parkOnDisk(token) {
			t.Errorf("park %s was deleted although no resume of it completed", parkShort(token))
		}
	}
	// The remote owner dies without cleaning up and the hub stops
	// reporting it: silence proves nothing, the park stays, as unknown.
	setRemoteParkProvider(nil)
	if rep = f.cli.sweepDeadParks(0); len(rep.removed) != 0 || !parkOnDisk(f.remote) {
		t.Fatalf("a silent mirror is not proof of anything: %v", parkShorts(rep.removed))
	}
	// The owner processes die. Still nothing is finished: the one whose
	// job fired is owed a resume, the other is unknown.
	parkOwnerAlive = func(int) bool { return false }
	rep = f.cli.sweepDeadParks(0)
	if len(rep.removed) != 0 || !strings.Contains(sortedJoin(rep.retry...), f.ownerAliveFired) || !strings.Contains(sortedJoin(rep.unknown...), f.ownerAlive) {
		t.Fatalf("dead owners: removed %v retry %v unknown %v", parkShorts(rep.removed), parkShorts(rep.retry), parkShorts(rep.unknown))
	}
}

// /parked prune and @park prune are one sweep: the command prints its
// counts, the tool names what it left alone, why, and what can end it.
func TestParkedPruneCommandAndToolShareTheSweep(t *testing.T) {
	f := newParkFleet(t)
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
	if !parkOnDisk(f.resumed) || !parkOnDisk(f.recentlyResumed) {
		t.Fatal("a rejected prune deleted a snapshot")
	}

	// Flattened {"cmd":"prune","args":{"older_than":"24h"}}.
	res, err := p.Execute(context.Background(), []string{"prune", "--older_than", "24h"})
	if err != nil || !strings.HasPrefix(res, "pruned 2 parked snapshot(s) proven finished") || !strings.Contains(res, "older than 24h0m0s") {
		t.Fatalf("prune older_than: %q %v", res, err)
	}
	for _, token := range append(append(f.live(), f.retry()...), f.unknown()...) {
		if !strings.Contains(res, parkShort(token)) || !parkOnDisk(token) {
			t.Errorf("park %s must be kept and named: %q", parkShort(token), res)
		}
	}
	for _, want := range []string{"still waiting", `"cmd":"discard"`, "state is unknown here", "/parked gc", "resume failed or was interrupted", "/resume "} {
		if !strings.Contains(res, want) {
			t.Errorf("the result must explain what it kept (%q missing): %q", want, res)
		}
	}
	if !parkOnDisk(f.recentlyResumed) {
		t.Fatal("the age must have spared the recent one")
	}
	// /parked prune takes the recent completed one, and says what it kept.
	out := captureStdout(t, f.cli.parkPrune)
	if !strings.Contains(out, "1 removed, 13 still pending") ||
		!strings.Contains(out, i18n.T("leftovers.parked.prune_unknown", 5, strings.Join(parkShorts(f.unknown()), ", "))) ||
		!strings.Contains(out, i18n.T("leftovers.parked.prune_retry", parkShort(f.failed), parkShort(f.failed))) {
		t.Fatalf("/parked prune: %q", out)
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
	if !strings.Contains(out, "15 removed") || parkOnDisk(f.waiting) || parkOnDisk(f.orphan) || !parkOnDisk(f.recentlyResumed) {
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
