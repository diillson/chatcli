/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTaskGraphAdapter scripts a run that streams lines until its context
// is canceled (or until release is closed), so the background path can be
// observed mid-flight.
type fakeTaskGraphAdapter struct {
	mu       sync.Mutex
	release  chan struct{}
	resolved []string
	ranIDs   []string
	canceled int
}

func (f *fakeTaskGraphAdapter) Plan(string) (string, error) { return "plan accepted: run tg-plan", nil }
func (f *fakeTaskGraphAdapter) Run(ctx context.Context, runID, graphJSON string, onOutput func(string)) (string, error) {
	f.mu.Lock()
	f.ranIDs = append(f.ranIDs, runID+"|"+graphJSON)
	f.mu.Unlock()
	onOutput("T1 started")
	onOutput("T1 done\n")
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-f.release:
		return "final report: all green", nil
	}
}
func (f *fakeTaskGraphAdapter) Status(runID string) (string, error) {
	if runID == "missing" {
		return "", errors.New("no such run")
	}
	return "status of " + runID, nil
}
func (f *fakeTaskGraphAdapter) Show(runID, taskID string) (string, error) {
	return "show " + runID + "/" + taskID, nil
}
func (f *fakeTaskGraphAdapter) Retry(context.Context, string, string, func(string)) (string, error) {
	return "retried", nil
}
func (f *fakeTaskGraphAdapter) Cancel() (string, error) {
	f.mu.Lock()
	f.canceled++
	f.mu.Unlock()
	return "engine canceled", nil
}
func (f *fakeTaskGraphAdapter) List() (string, error) { return "runs", nil }

// fakeTaskGraphResolver adds the optional run-id resolution capability.
type fakeTaskGraphResolver struct{ *fakeTaskGraphAdapter }

func (f fakeTaskGraphResolver) ResolveRunID(runID, graphJSON string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolved = append(f.resolved, runID+"|"+graphJSON)
	if runID != "" {
		return runID, nil
	}
	return "tg-bg-1", nil
}

func withFakeTaskGraph(t *testing.T) *fakeTaskGraphAdapter {
	t.Helper()
	fake := &fakeTaskGraphAdapter{release: make(chan struct{})}
	SetTaskGraphAdapter(fakeTaskGraphResolver{fake})
	t.Cleanup(func() { SetTaskGraphAdapter(nil) })
	return fake
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestTaskGraphBackgroundRunLifecycle(t *testing.T) {
	fake := withFakeTaskGraph(t)
	p := NewBuiltinTaskGraphPlugin()
	graph := `{"name":"x","tasks":[{"id":"T1","prompt":"p"}]}`

	start := time.Now()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"run","args":{"graph":` + graph + `,"background":true}}`})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("background run must return immediately")
	}
	if !strings.Contains(out, "background run tg-bg-1 started") || !strings.Contains(out, `{"cmd":"status","args":{"id":"tg-bg-1"}}`) {
		t.Fatalf("ack must carry the id and the exact poll call: %q", out)
	}
	waitFor(t, "run to start", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.ranIDs) == 1
	})
	fake.mu.Lock()
	ran, resolved := fake.ranIDs[0], fake.resolved[0]
	fake.mu.Unlock()
	if ran != "tg-bg-1|" || !strings.Contains(resolved, `"tasks"`) {
		t.Fatalf("the resolved id must be run without re-planning: ran=%q resolved=%q", ran, resolved)
	}

	// A second background run is refused with the active id.
	if _, err := p.Execute(context.Background(), []string{"run", "--id", "tg-other", "--background"}); err == nil || !strings.Contains(err.Error(), "tg-bg-1 is still active") {
		t.Fatalf("second background run must be refused: %v", err)
	}

	// status surfaces the captured output tail while running.
	waitFor(t, "output capture", func() bool {
		s := taskGraphBackgroundSummary("")
		return strings.Contains(s, "T1 done")
	})
	st, err := p.Execute(context.Background(), []string{`{"cmd":"status","args":{"id":"tg-bg-1"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st, "status of tg-bg-1") || !strings.Contains(st, "running for") || !strings.Contains(st, "T1 started\nT1 done") {
		t.Fatalf("status must carry the persisted view and the live tail: %q", st)
	}
	// Another run's status does not get this run's tail.
	other, _ := p.Execute(context.Background(), []string{`{"cmd":"status","args":{"id":"tg-zzz"}}`})
	if strings.Contains(other, "background run") {
		t.Fatalf("unrelated status must not carry the background summary: %q", other)
	}
	// When the store errors, the background slot still answers.
	_, err = p.Execute(context.Background(), []string{`{"cmd":"status","args":{"id":"missing"}}`})
	if err == nil || !strings.Contains(err.Error(), "no such run") {
		t.Fatalf("unrelated store error must pass through: %v", err)
	}

	// Let the run finish: the final report shows up in status.
	close(fake.release)
	waitFor(t, "run to finish", func() bool { return !strings.Contains(taskGraphBackgroundSummary(""), "running") })
	st, _ = p.Execute(context.Background(), []string{"status"})
	if !strings.Contains(st, "finished after") || !strings.Contains(st, "final report: all green") {
		t.Fatalf("finished run must surface its report: %q", st)
	}
	if _, hadBG := cancelTaskGraphBackground(); hadBG {
		t.Fatal("a finished run is not cancelable")
	}
}

func TestTaskGraphBackgroundCancelAndDetach(t *testing.T) {
	fake := withFakeTaskGraph(t)
	p := NewBuiltinTaskGraphPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"run","args":{"id":"tg-7","async":"true"}}`}); err != nil {
		t.Fatal(err)
	}
	out, err := p.Execute(context.Background(), []string{`{"cmd":"cancel"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "background run tg-7 canceled") || !strings.Contains(out, "engine canceled") {
		t.Fatalf("cancel must stop the goroutine and the engine: %q", out)
	}
	if s := taskGraphBackgroundSummary("tg-7"); !strings.Contains(s, "finished with error") || !strings.Contains(s, "context canceled") {
		t.Fatalf("canceled run must report so: %q", s)
	}
	// After cancel a new background run is accepted again.
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"run","args":{"id":"tg-8","background":"1"}}`}); err != nil {
		t.Fatalf("new run after cancel: %v", err)
	}
	// Detaching the adapter (session end) cancels the run.
	SetTaskGraphAdapter(nil)
	if s := taskGraphBackgroundSummary("tg-8"); strings.Contains(s, "running") {
		t.Fatalf("adapter swap must end the run: %q", s)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.canceled != 1 {
		t.Fatalf("engine Cancel must be called once by the cancel verb, got %d", fake.canceled)
	}
}

func TestTaskGraphBackgroundNeedsResolverAndCaps(t *testing.T) {
	fake := &fakeTaskGraphAdapter{release: make(chan struct{})}
	SetTaskGraphAdapter(fake) // no resolver
	t.Cleanup(func() { SetTaskGraphAdapter(nil) })
	p := NewBuiltinTaskGraphPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"run","args":{"id":"x","background":true}}`}); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("background without a resolver must refuse: %v", err)
	}
	checkCaps(t, "@taskgraph", p, []capsCase{
		{[]string{`{"cmd":"run","args":{"id":"x","background":true}}`}, false, false},
		{[]string{`{"cmd":"status","args":{"id":"x"}}`}, true, true},
	})
	if got := p.DescribeCall([]string{`{"cmd":"run","args":{"id":"x","background":true}}`}); !strings.Contains(got, "describe_run_background") {
		t.Fatalf("describe: %q", got)
	}
	if got := p.DescribeCall([]string{"run", "--id", "x"}); strings.Contains(got, "background") {
		t.Fatalf("foreground run must not describe as background: %q", got)
	}
	if !strings.Contains(p.Usage(), "background") || !strings.Contains(p.Schema(), `"background"`) {
		t.Fatal("Usage/Schema must document background")
	}
}

func TestTaskGraphTailIsBounded(t *testing.T) {
	var tail taskGraphTail
	line := strings.Repeat("x", 1000) + "\n"
	for i := 0; i < 100; i++ {
		tail.Write(line)
	}
	buf, total := tail.Snapshot()
	if total != 100*len(line) || len(buf) > taskGraphBackgroundTailBytes || len(buf) < taskGraphBackgroundTailBytes-len(line) {
		t.Fatalf("tail must keep the last %d bytes: len=%d total=%d", taskGraphBackgroundTailBytes, len(buf), total)
	}
}
