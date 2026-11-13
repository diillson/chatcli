/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

const (
	detachedAskTool = "@coder"
	detachedAskArgs = `{"cmd":"exec","args":{"cmd":"curl https://example.invalid"}}`
)

// detachedVerdict starts a tool-started background run whose worker calls
// CheckAndPrompt once over the given reader, and returns that verdict. It
// fails if the call is still waiting long before any worker timeout.
func detachedVerdict(t *testing.T, lines chan string, gone chan struct{}) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	withStdinInteractive(t, true)
	pa, err := newWorkerPolicyAdapter(zap.NewNop())
	if err != nil || pa.pm.Check(detachedAskTool, detachedAskArgs) != coder.ActionAsk {
		t.Fatalf("the test needs an ask verdict (%v)", err)
	}
	pa.liveStdin = func() (<-chan string, <-chan struct{}) { return lines, gone }
	got := make(chan string, 1)
	var once sync.Once
	a := newTestTaskGraphAdapter(t)
	release := make(chan struct{})
	close(release)
	a.dispatcherFn = func() (taskGraphDispatcher, error) {
		return gatedDispatcher{release: release, policy: func(ctx context.Context) {
			once.Do(func() {
				allowed, reason := pa.CheckAndPrompt(ctx, detachedAskTool, detachedAskArgs)
				got <- fmt.Sprint(allowed, " ", reason)
			})
		}}, nil
	}
	plugins.SetTaskGraphAdapter(a)
	t.Cleanup(func() {
		plugins.CancelTaskGraphBackgroundRun()
		waitTaskGraphBackground(t)
		plugins.SetTaskGraphAdapter(nil)
	})
	plan := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(plan, []byte(testPlanJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.NewBuiltinTaskGraphPlugin().Execute(context.Background(), []string{"run", "--background", "--file", plan}); err != nil {
		t.Fatalf("run did not start: %v", err)
	}
	select {
	case v := <-got:
		return v
	case <-time.After(20 * time.Second):
		t.Fatal("the prompt kept waiting on a reader that is gone")
		return ""
	}
}

// The prompt of a detached run is issued while the turn reads stdin; the
// turn then ends before anyone answers. The call must come back as the
// unattended denial when the reader is torn down, not at the worker timeout.
func TestDetachedPromptEndsWhenTheReaderGoesAway(t *testing.T) {
	lines, gone := make(chan string, 1), make(chan struct{})
	// The prompt drains type-ahead as it opens and hands it to the salvage
	// sink: that call is the proof the prompt is up with a live reader, and
	// it is where the turn ends.
	lines <- "typed before the prompt"
	var teardown sync.Once
	coder.SetSecurityPromptSalvage(func([]string) { teardown.Do(func() { close(gone) }) })
	defer coder.SetSecurityPromptSalvage(nil)
	if v, want := detachedVerdict(t, lines, gone), "false "+i18n.T("agent.policy.unattended_denied", detachedAskTool); v != want {
		t.Fatalf("verdict %q, want %q", v, want)
	}
	if !chanClosed(gone) {
		t.Fatal("the prompt returned before the reader was torn down")
	}
}

// A call that queued behind another prompt while the turn ended never draws
// its own prompt: the reader is already gone when its turn to ask comes.
func TestDetachedPromptIsNotDrawnForAReaderAlreadyGone(t *testing.T) {
	lines, gone := make(chan string, 1), make(chan struct{})
	lines <- "y"
	close(gone)
	if v, want := detachedVerdict(t, lines, gone), "false "+i18n.T("agent.policy.unattended_denied", detachedAskTool); v != want {
		t.Fatalf("verdict %q, want %q", v, want)
	}
	if len(lines) != 1 {
		t.Fatal("the line left for the next reader was consumed")
	}
}

// With no teardown signal the prompt behaves as before, and an answer that
// arrives while the reader leaves is still the user's answer.
func TestPromptWhileReaderLivesKeepsARealAnswer(t *testing.T) {
	ask := func(context.Context) coder.SecurityDecision { return coder.DecisionRunOnce }
	gone := make(chan struct{})
	close(gone)
	for _, signal := range []<-chan struct{}{nil, gone} {
		if d, abandoned := promptWhileReaderLives(context.Background(), signal, ask); d != coder.DecisionRunOnce || abandoned {
			t.Fatalf("an answer must stand: %v abandoned=%v", d, abandoned)
		}
	}
}

// The agent loop hands out the lines and the teardown signal of one reader,
// and nothing when no reader runs.
func TestLiveStdinLinesPairsLinesWithTeardown(t *testing.T) {
	a := &AgentMode{}
	if lines, gone := a.liveStdinLines(); lines != nil || gone != nil {
		t.Fatal("no reader must yield no channels")
	}
	a.stdinLines, a.stdinDone = make(chan string, 1), make(chan struct{})
	lines, gone := a.liveStdinLines()
	if lines == nil || chanClosed(gone) {
		t.Fatal("a running reader must yield its lines and an open teardown signal")
	}
	a.stdinMu.Lock()
	a.teardownStdinReaderLocked()
	a.stdinMu.Unlock()
	if lines, _ := a.liveStdinLines(); !chanClosed(gone) || lines != nil {
		t.Fatal("teardown must close the signal handed out earlier and leave no live reader")
	}
}
