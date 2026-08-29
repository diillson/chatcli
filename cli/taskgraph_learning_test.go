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

	"github.com/diillson/chatcli/cli/taskgraph"
)

func TestFormatTaskGraphDigest(t *testing.T) {
	pass := "PASS"
	g := &taskgraph.Graph{
		Name: "feature-x", Status: taskgraph.StatusDone,
		Tasks: []*taskgraph.Task{
			{ID: "T1", Title: "endpoint", Status: taskgraph.StatusDone, CostUSD: 0.5,
				Attempts: []taskgraph.Attempt{{Verdict: pass, Evidence: "tests green, /foo covered"}}},
			{ID: "T2", Title: "client", Status: taskgraph.StatusFailed,
				Attempts: []taskgraph.Attempt{{FailureReason: "build broke on missing import"}}},
		},
	}
	d := formatTaskGraphDigest(g)
	if len(d) > taskGraphDigestMaxChars {
		t.Fatalf("digest exceeds cap: %d", len(d))
	}
	for _, want := range []string{"feature-x", "T1", "T2", "tests green", "build broke"} {
		if !strings.Contains(d, want) {
			t.Fatalf("digest missing %q:\n%s", want, d)
		}
	}
}

func TestFormatTaskGraphDigestTruncatesManyTasks(t *testing.T) {
	g := &taskgraph.Graph{Name: "big", Status: taskgraph.StatusDone}
	for i := 0; i < 200; i++ {
		g.Tasks = append(g.Tasks, &taskgraph.Task{
			ID: "T", Title: strings.Repeat("x", 50), Status: taskgraph.StatusDone,
		})
	}
	if got := len(formatTaskGraphDigest(g)); got > taskGraphDigestMaxChars {
		t.Fatalf("digest must stay under cap even with many tasks: %d", got)
	}
}

func TestQueueLearningDigestNilMemWorker(t *testing.T) {
	a := &taskGraphAdapter{cli: &ChatCLI{}}                                  // memWorker nil, agentMode nil
	a.queueLearningDigest(context.Background(), &taskgraph.Graph{Name: "x"}) // must not panic
}
