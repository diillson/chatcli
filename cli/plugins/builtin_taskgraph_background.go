/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * @taskgraph run in the background.
 *
 * A graph run can take many minutes; blocking the whole agent turn on it
 * leaves the model unable to do anything else. With background:true the run
 * starts on its own goroutine, under a context owned by the plugin (not the
 * tool call, which ends immediately), and the call returns the run id plus
 * the exact status call to poll. The run's streamed output is captured into
 * a bounded tail buffer that status/show surface, so nothing is streamed to
 * a callback whose turn is long gone. One background run at a time: a
 * second request is refused with the active id; cancel stops it; swapping
 * the adapter (session end) cancels it too.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/pkg/textfold"
)

// TaskGraphRunResolver is the OPTIONAL capability a background run needs:
// persist the graph (or resolve the default run id) and return the concrete
// run id BEFORE execution starts, so the id can be handed back at once.
type TaskGraphRunResolver interface {
	ResolveRunID(runID, graphJSON string) (string, error)
}

// taskGraphBackgroundTailBytes bounds the captured output surfaced by
// status/show: the last 64 KB is plenty to see where a run is.
const taskGraphBackgroundTailBytes = 64 << 10

// taskGraphTail is a bounded, rune-safe tail buffer for streamed output.
type taskGraphTail struct {
	mu    sync.Mutex
	buf   string
	total int
}

// Write appends line, trimming the front past the bound.
func (t *taskGraphTail) Write(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.total += len(line)
	t.buf += line
	if !strings.HasSuffix(line, "\n") {
		t.buf += "\n"
	}
	if len(t.buf) > taskGraphBackgroundTailBytes {
		t.buf = textfold.Tail(t.buf, taskGraphBackgroundTailBytes)
	}
}

// Snapshot returns the retained tail and the total bytes ever written.
func (t *taskGraphTail) Snapshot() (string, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf, t.total
}

// taskGraphBackgroundRun is one detached run and its outcome.
type taskGraphBackgroundRun struct {
	id       string
	cancel   context.CancelFunc
	done     chan struct{}
	started  time.Time
	tail     *taskGraphTail
	mu       sync.Mutex
	finished time.Time
	result   string
	err      error
}

// active reports whether the run is still going.
func (r *taskGraphBackgroundRun) active() bool {
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

// taskGraphBG holds the single background run slot.
var taskGraphBG struct {
	mu  sync.Mutex
	run *taskGraphBackgroundRun
}

// taskGraphWantsBackground reads the background/async flag leniently
// (booleans, "true"/"1" strings, a bare --background from the flattener).
func taskGraphWantsBackground(payload map[string]json.RawMessage) bool {
	return jsonBool(payload, "background", "async", "detach", "detached", "nowait")
}

// taskGraphPollCall renders the exact status call for a run id.
func taskGraphPollCall(id string) string {
	return fmt.Sprintf(`{"cmd":"status","args":{"id":%q}}`, id)
}

// startTaskGraphBackground resolves the run id, starts the run detached and
// returns the model-facing acknowledgment. A second background run while
// one is active is refused with the active id. The run's context keeps the
// call's values but not its cancellation: the tool call returns at once
// and must not take the run down with it.
func startTaskGraphBackground(ctx context.Context, adapter TaskGraphAdapter, runID, graphJSON string) (string, error) {
	resolver, ok := adapter.(TaskGraphRunResolver)
	if !ok {
		return "", errors.New("@taskgraph run: background execution not available in this session (run without background)")
	}
	taskGraphBG.mu.Lock()
	defer taskGraphBG.mu.Unlock()
	if cur := taskGraphBG.run; cur != nil && cur.active() {
		return "", fmt.Errorf("@taskgraph run: background run %s is still active — poll it with %s or stop it with {\"cmd\":\"cancel\"} before starting another",
			cur.id, taskGraphPollCall(cur.id))
	}
	id, err := resolver.ResolveRunID(runID, graphJSON)
	if err != nil {
		return "", err
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	run := &taskGraphBackgroundRun{id: id, cancel: cancel, done: make(chan struct{}), started: time.Now(), tail: &taskGraphTail{}}
	taskGraphBG.run = run
	go func() {
		defer close(run.done)
		defer cancel()
		result, rerr := adapter.Run(runCtx, id, "", run.tail.Write)
		run.mu.Lock()
		run.result, run.err, run.finished = result, rerr, time.Now()
		run.mu.Unlock()
	}()
	return fmt.Sprintf("background run %s started. Poll with %s (show/list work too); stop with {\"cmd\":\"cancel\"}. The final report appears in status once the run finishes.",
		id, taskGraphPollCall(id)), nil
}

// taskGraphBackgroundSummary renders the background run's state and output
// tail when it matches id (any run when id is empty), or "" when there is
// nothing to report.
func taskGraphBackgroundSummary(id string) string {
	taskGraphBG.mu.Lock()
	run := taskGraphBG.run
	taskGraphBG.mu.Unlock()
	if run == nil || (id != "" && id != run.id) {
		return ""
	}
	var b strings.Builder
	if run.active() {
		fmt.Fprintf(&b, "background run %s: running for %s", run.id, time.Since(run.started).Round(time.Second))
	} else {
		run.mu.Lock()
		result, err, finished := run.result, run.err, run.finished
		run.mu.Unlock()
		switch {
		case err != nil:
			fmt.Fprintf(&b, "background run %s: finished with error after %s: %v", run.id, finished.Sub(run.started).Round(time.Second), err)
		default:
			fmt.Fprintf(&b, "background run %s: finished after %s", run.id, finished.Sub(run.started).Round(time.Second))
		}
		if strings.TrimSpace(result) != "" {
			b.WriteString("\n--- final report ---\n")
			b.WriteString(strings.TrimRight(result, "\n"))
		}
	}
	tail, total := run.tail.Snapshot()
	if tail != "" {
		fmt.Fprintf(&b, "\n--- output tail (last %d of %d bytes) ---\n%s", len(tail), total, strings.TrimRight(tail, "\n"))
	}
	return b.String()
}

// cancelTaskGraphBackground stops the active background run, reporting
// whether there was one.
func cancelTaskGraphBackground() (string, bool) {
	taskGraphBG.mu.Lock()
	run := taskGraphBG.run
	taskGraphBG.mu.Unlock()
	if run == nil || !run.active() {
		return "", false
	}
	run.cancel()
	select {
	case <-run.done:
	case <-time.After(5 * time.Second):
	}
	return fmt.Sprintf("background run %s canceled.", run.id), true
}

// withTaskGraphBackground appends the background summary to a status/show
// answer when one applies.
func withTaskGraphBackground(out string, err error, id string) (string, error) {
	summary := taskGraphBackgroundSummary(id)
	if summary == "" {
		return out, err
	}
	if err != nil {
		// The persisted view failed (e.g. the store is mid-write) but the
		// background slot knows the run: answer with it and keep the
		// store's complaint visible.
		return summary + "\n(persisted state unavailable right now: " + err.Error() + ")", nil
	}
	return strings.TrimRight(out, "\n") + "\n\n" + summary, nil
}
