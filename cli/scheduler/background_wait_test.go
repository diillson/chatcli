/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/i18n"
)

// newWaitTestScheduler starts a scheduler with the fake condition and
// action registered and fast polling.
func newWaitTestScheduler(t *testing.T) (*Scheduler, *fakeEval) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.DefaultPollInterval = 20 * time.Millisecond
	cfg.DefaultWaitTimeout = 30 * time.Second
	cfg.AuditEnabled = false
	cfg.SnapshotInterval = 0
	cfg.WALGCInterval = 0
	cfg.DaemonAutoConnect = false
	s, err := New(cfg, NewNoopBridge(), SchedulerDeps{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fe := &fakeEval{}
	_ = s.Conditions().Register(fe)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DrainAndShutdown(2 * time.Second) })
	return s, fe
}

func decodeOut(t *testing.T, raw string) ToolOutput {
	t.Helper()
	var out ToolOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return out
}

func waitForStatus(t *testing.T, s *Scheduler, id JobID, want JobStatus) *Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if j, err := s.Query(id); err == nil && j.Status == want {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	j, _ := s.Query(id)
	t.Fatalf("job %s never reached %s (now %+v)", id, want, j)
	return nil
}

func TestParseLooseDuration(t *testing.T) {
	good := map[string]time.Duration{
		"30s": 30 * time.Second, "5m": 5 * time.Minute, "1h30m": 90 * time.Minute,
		"10 min": 10 * time.Minute, "2 hours": 2 * time.Hour, "1d": 24 * time.Hour,
		"1h 30m": 90 * time.Minute, "2 horas e 30 minutos": 150 * time.Minute,
		"45 Seconds": 45 * time.Second, "1.5h": 90 * time.Minute, "1w": 7 * 24 * time.Hour,
		"500ms": 500 * time.Millisecond, "0s": 0,
	}
	for in, want := range good {
		got, err := ParseLooseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseLooseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "30", "10x", "soon", "-5m", "5 parsecs"} {
		if d, err := ParseLooseDuration(in); err == nil {
			t.Errorf("ParseLooseDuration(%q) = %v, want an error", in, d)
		}
	}
}

func TestScheduleDSLAcceptsLooseDurations(t *testing.T) {
	sch, err := ParseScheduleDSL("in 5 min")
	if err != nil || sch.Kind != ScheduleRelative || sch.Relative != 5*time.Minute {
		t.Fatalf("in 5 min: %+v %v", sch, err)
	}
	sch, err = ParseScheduleDSL("every 2 hours")
	if err != nil || sch.Kind != ScheduleInterval || sch.Interval != 2*time.Hour {
		t.Fatalf("every 2 hours: %+v %v", sch, err)
	}
}

// A wait that is healthy — polling a condition not yet met, or one whose
// probe errors because the target is still starting — must not be listed
// as overdue or failed.
func TestWaitingJobIsNotListedAsOverdueOrFailed(t *testing.T) {
	now := time.Now()
	waiting := JobSummary{
		Status: StatusWaiting, Type: "on_condition+wait",
		NextFireAt: now.Add(-3 * time.Minute), LastOutcome: OutcomeFailed,
		WaitPolls: 12, WaitDeadline: now.Add(9 * time.Minute),
	}
	next, last := nextFireLabel(waiting, now), lastLabel(waiting)
	if next != i18n.T("scheduler.render.polling_left", "9m0s") {
		t.Errorf("next fire of a waiting job = %q", next)
	}
	if last == string(OutcomeFailed) || !strings.Contains(last, "×12") {
		t.Errorf("last of a waiting job whose probe errored = %q", last)
	}
	waiting.LastOutcome = OutcomeSkipped
	if got := lastLabel(waiting); got == string(OutcomeSkipped) {
		t.Errorf("last of a waiting job not yet met = %q", got)
	}

	cases := []struct {
		name string
		sum  JobSummary
		want string
	}{
		{"finished", JobSummary{Status: StatusCompleted, NextFireAt: now.Add(-time.Hour)}, "—"},
		{"paused", JobSummary{Status: StatusPaused, NextFireAt: now.Add(-time.Hour)}, "—"},
		{"queued late", JobSummary{Status: StatusPending, NextFireAt: now.Add(-time.Minute)}, i18n.T("scheduler.render.overdue")},
		{"queued just due", JobSummary{Status: StatusPending, NextFireAt: now.Add(-time.Second)}, i18n.T("scheduler.render.due_now")},
		{"queued ahead", JobSummary{Status: StatusPending, NextFireAt: now.Add(90 * time.Second)}, i18n.T("scheduler.render.in", "1m30s")},
		{"running", JobSummary{Status: StatusRunning, NextFireAt: now.Add(-time.Minute)}, i18n.T("scheduler.render.running_now")},
		{"blocked", JobSummary{Status: StatusBlocked, NextFireAt: now.Add(-time.Minute)}, i18n.T("scheduler.render.after_deps")},
	}
	for _, c := range cases {
		if got := nextFireLabel(c.sum, now); got != c.want {
			t.Errorf("%s: next fire = %q, want %q", c.name, got, c.want)
		}
	}
	// An action's own outcome is shown as is.
	if got := lastLabel(JobSummary{Status: StatusFailed, Type: "relative", LastOutcome: OutcomeFailed}); got != "failed" {
		t.Errorf("last of a failed action = %q", got)
	}
}

func TestSummaryProjectsWaitProgress(t *testing.T) {
	s, _ := newWaitTestScheduler(t)
	ta := NewToolAdapter(s)
	owner := Owner{Kind: OwnerUser, ID: "tester"}
	raw, _ := ta.WaitUntil(context.Background(), owner,
		`{"name":"db-up","wait":{"condition":{"type":"fake"}},"async":true,"wait_timeout":"10 min","poll":"20ms"}`)
	out := decodeOut(t, raw)
	if !out.OK {
		t.Fatalf("enqueue: %s", raw)
	}
	waitForStatus(t, s, out.JobID, StatusWaiting)
	var sum JobSummary
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		j, _ := s.Query(out.JobID)
		if sum = j.Summary(); sum.WaitPolls >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sum.WaitPolls < 2 || sum.WaitDetail != "fake" {
		t.Fatalf("wait progress not projected: %+v", sum)
	}
	if left := time.Until(sum.WaitDeadline); left < 9*time.Minute || left > 10*time.Minute {
		t.Fatalf("wait deadline %v is not ~10m away", left)
	}
	j, _ := s.Query(out.JobID)
	show := RenderShow(j)
	for _, want := range []string{
		i18n.T("scheduler.render.wait_budget_value", "20ms", "10m0s"),
		i18n.T("scheduler.render.wait_progress"),
		i18n.T("scheduler.render.wait_last_check"),
	} {
		if !strings.Contains(show, want) {
			t.Errorf("/jobs show lacks %q:\n%s", want, show)
		}
	}
	list := RenderList(s.List(ListFilter{}))
	if strings.Contains(list, i18n.T("scheduler.render.overdue")) || strings.Contains(list, string(OutcomeSkipped)) {
		t.Errorf("listing of a healthy wait:\n%s", list)
	}
}

func TestBuildJobRejectsUnreadableBudget(t *testing.T) {
	s, _ := newWaitTestScheduler(t)
	ta := NewToolAdapter(s)
	owner := Owner{Kind: OwnerUser, ID: "tester"}
	for _, in := range []string{
		`{"wait":{"condition":{"type":"fake"}},"async":true,"wait_timeout":"10x"}`,
		`{"wait":{"condition":{"type":"fake"}},"async":true,"poll":"fast"}`,
		`{"wait":{"condition":{"type":"fake"}},"async":true,"on_timeout":"explode"}`,
		`{"wait":{"condition":{"type":"fake"}},"async":true,"max_polls":-1}`,
	} {
		raw, _ := ta.WaitUntil(context.Background(), owner, in)
		if out := decodeOut(t, raw); out.OK || out.Error == "" {
			t.Errorf("%s was accepted: %s", in, raw)
		}
	}
	if n := len(s.List(ListFilter{IncludeTerminal: true})); n != 0 {
		t.Fatalf("%d jobs were created from rejected inputs", n)
	}
	raw, _ := ta.WaitUntil(context.Background(), owner,
		`{"name":"ok","wait":{"condition":{"type":"fake"}},"async":true,"on_timeout":"fire-anyway","notify":true}`)
	out := decodeOut(t, raw)
	j, err := s.Query(out.JobID)
	if err != nil || j.Wait.OnTimeout != TimeoutFireAnyway || j.Tags["notify"] != "on" {
		t.Fatalf("on_timeout/notify not applied: %+v %v", j, err)
	}
}

func TestWaitUntilMaxBlockReleasesTheCallerAndKeepsTheJob(t *testing.T) {
	s, _ := newWaitTestScheduler(t)
	ta := NewToolAdapter(s)
	start := time.Now()
	raw, _ := ta.WaitUntil(context.Background(), Owner{Kind: OwnerUser, ID: "tester"},
		`{"name":"slow","wait":{"condition":{"type":"fake"}},"max_block":"700ms"}`)
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("max_block did not release the caller (%v)", took)
	}
	out := decodeOut(t, raw)
	if out.OK || !strings.Contains(out.Error, "KEEPS RUNNING") {
		t.Fatalf("unexpected reply: %s", raw)
	}
	id, err := s.ResolveRef("slow")
	if err != nil {
		t.Fatal(err)
	}
	if j, _ := s.Query(id); j.Status != StatusWaiting {
		t.Fatalf("job should still be waiting, is %s", j.Status)
	}
}

func TestResolveRefByNameAndPrefix(t *testing.T) {
	s, fe := newWaitTestScheduler(t)
	ta := NewToolAdapter(s)
	owner := Owner{Kind: OwnerUser, ID: "tester"}
	raw, _ := ta.WaitUntil(context.Background(), owner, `{"name":"api-up","wait":{"condition":{"type":"fake"}},"async":true}`)
	id := decodeOut(t, raw).JobID
	for _, ref := range []string{string(id), "api-up", string(id)[:6]} {
		got, err := s.ResolveRef(ref)
		if err != nil || got != id {
			t.Errorf("ResolveRef(%q) = %q, %v", ref, got, err)
		}
	}
	if _, err := s.ResolveRef("nope"); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("unknown ref: %v", err)
	}
	if _, err := MatchJobRef([]JobSummary{{ID: "abcd1"}, {ID: "abcd2"}}, "abcd"); !errors.Is(err, ErrJobAmbiguous) {
		t.Errorf("ambiguous prefix: %v", err)
	}
	// The tool reaches a job by its name.
	raw, _ = ta.QueryJob(context.Background(), owner, `{"id":"api-up"}`)
	if out := decodeOut(t, raw); !out.OK || out.Job == nil || out.Job.ID != id {
		t.Fatalf("query by name: %s", raw)
	}
	raw, _ = ta.CancelJob(context.Background(), owner, `{"id":"api-up"}`)
	if out := decodeOut(t, raw); !out.OK || out.JobID != id {
		t.Fatalf("cancel by name: %s", raw)
	}
	_ = fe
}

func TestClearJobsDropsOnlyFinishedAndScopesAgents(t *testing.T) {
	s, _ := newWaitTestScheduler(t)
	ta := NewToolAdapter(s)
	user := Owner{Kind: OwnerUser, ID: "tester"}
	agent := Owner{Kind: OwnerAgent, ID: "a1"}
	mk := func(owner Owner, name string, cancel bool) JobID {
		raw, _ := ta.ScheduleJob(context.Background(), owner, `{"name":"`+name+`","when":"+1h","do":"noop"}`)
		id := decodeOut(t, raw).JobID
		if cancel {
			if err := s.Cancel(id, "seed", owner); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	live := mk(user, "live", false)
	userDone := mk(user, "user-done", true)
	agentDone := mk(agent, "agent-done", true)

	if out := decodeOut(t, mustClear(t, ta, agent, `{"filter":{"statuses":["pending"]}}`)); out.OK {
		t.Fatal("clearing a live status was accepted")
	}
	out := decodeOut(t, mustClear(t, ta, agent, `{}`))
	if !out.OK || len(out.Removed) != 1 || out.Removed[0] != agentDone {
		t.Fatalf("agent clear removed %v", out.Removed)
	}
	if _, err := s.Query(userDone); err != nil {
		t.Fatal("an agent cleared a user's job")
	}
	out = decodeOut(t, mustClear(t, ta, user, `{"older_than":"1h"}`))
	if !out.OK || len(out.Removed) != 0 {
		t.Fatalf("older_than 1h removed a fresh job: %v", out.Removed)
	}
	out = decodeOut(t, mustClear(t, ta, user, `{}`))
	if len(out.Removed) != 1 || out.Removed[0] != userDone {
		t.Fatalf("user clear removed %v", out.Removed)
	}
	if _, err := s.Query(live); err != nil {
		t.Fatal("clear removed a live job")
	}
}

func mustClear(t *testing.T, ta *ToolAdapter, owner Owner, in string) string {
	t.Helper()
	raw, err := ta.ClearJobs(context.Background(), owner, in)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The daemon serves /jobs clear: before, a terminal attached to a daemon
// could not clear its history at all.
func TestDaemonPruneOverIPC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the daemon listens on a UNIX socket")
	}
	s, _ := newWaitTestScheduler(t)
	sock := filepath.Join(shortTempDir(t), "s.sock")
	d := NewDaemon(s, sock, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.listener.Close() }()
	c, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	owner := Owner{Kind: OwnerUser, ID: "tester"}
	out, err := c.Enqueue(ctx, owner, ToolInput{Name: "gone", When: "+1h", Do: "noop"})
	if err != nil || !out.OK {
		t.Fatalf("enqueue: %+v %v", out, err)
	}
	if err := c.Cancel(ctx, owner, out.JobID, "seed"); err != nil {
		t.Fatal(err)
	}
	removed, err := c.Prune(ctx, owner, PruneFilter{Statuses: []JobStatus{StatusCancelled}})
	if err != nil || len(removed) != 1 || removed[0] != out.JobID {
		t.Fatalf("prune: %v %v", removed, err)
	}
	if _, err := c.Query(ctx, out.JobID); err == nil {
		t.Fatal("pruned job is still served")
	}
}

// shortTempDir returns a temp dir short enough for a UNIX socket path,
// which t.TempDir's nested name can exceed.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestDescribeCondition(t *testing.T) {
	for dsl, want := range map[string]string{
		"http://localhost:8080/health==200": "http_status http://localhost:8080/health",
		"tcp://db:5432":                     "tcp_reachable db:5432",
		"not file:/tmp/lock":                "not file_exists /tmp/lock",
		"k8s:pod/prod/api:Ready":            "k8s_resource_ready pod/prod/api",
		"and(tcp://a:1, file:/x)":           "all_of(tcp_reachable a:1, file_exists /x)",
	} {
		c, err := ParseConditionDSL(dsl)
		if err != nil {
			t.Fatal(err)
		}
		if got := DescribeCondition(c); got != want {
			t.Errorf("DescribeCondition(%q) = %q, want %q", dsl, got, want)
		}
	}
}

// The filter names the tool documents must reach the filter, and the Go
// names the daemon protocol sends must keep working.
func TestListFilterReadsDocumentedNames(t *testing.T) {
	var f ListFilter
	if err := json.Unmarshal([]byte(`{"include_terminal":true,"name_substr":"api","statuses":["failed"],"tag":"env"}`), &f); err != nil {
		t.Fatal(err)
	}
	if !f.IncludeTerminal || f.NameSubstr != "api" || len(f.Statuses) != 1 || f.Tag != "env" {
		t.Fatalf("documented names: %+v", f)
	}
	owner := Owner{Kind: OwnerUser, ID: "u"}
	wire, _ := json.Marshal(ListFilter{IncludeTerminal: true, NameSubstr: "db", Owner: &owner})
	var back ListFilter
	if err := json.Unmarshal(wire, &back); err != nil {
		t.Fatal(err)
	}
	if !back.IncludeTerminal || back.NameSubstr != "db" || back.Owner == nil || back.Owner.ID != "u" {
		t.Fatalf("protocol round trip: %s -> %+v", wire, back)
	}
	if err := json.Unmarshal([]byte(`{"name":"only"}`), &f); err != nil || f.NameSubstr != "only" || f.IncludeTerminal {
		t.Fatalf("name alias / reset: %+v %v", f, err)
	}
}
