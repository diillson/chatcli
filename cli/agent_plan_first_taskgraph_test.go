/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// TestQueryInvokesTaskGraph: an explicit task-graph request must defer
// auto plan-first to the @taskgraph orchestrator (executor ≠ reviewer),
// instead of the PlanRunner executing the work outside the graph.
func TestQueryInvokesTaskGraph(t *testing.T) {
	invokes := []string{
		"Use o @taskgraph para construir o CLI",
		"execute com o TASKGRAPH em paralelo",
		"rode isso como um task graph com review",
	}
	for _, q := range invokes {
		if !queryInvokesTaskGraph(q) {
			t.Fatalf("must defer to task graph: %q", q)
		}
	}
	plain := []string{
		"implemente OAuth com testes e me entregue revisado",
		"crie um grafo de chamadas do pacote http",
	}
	for _, q := range plain {
		if queryInvokesTaskGraph(q) {
			t.Fatalf("must NOT defer: %q", q)
		}
	}
}

// TestSteerToTaskGraphAppendsHint verifies the auto-routing hint is folded
// into the existing user turn (single message, no alternation break) and
// carries the @taskgraph steer.
func TestSteerToTaskGraphAppendsHint(t *testing.T) {
	cli := &ChatCLI{}
	cli.history = append(cli.history, models.Message{Role: "user", Content: "build the whole feature"})
	a := &AgentMode{cli: cli, logger: zap.NewNop()}
	a.steerToTaskGraph("build the whole feature")

	if len(cli.history) != 1 {
		t.Fatalf("must not add a new turn, got %d messages", len(cli.history))
	}
	last := cli.history[len(cli.history)-1]
	if last.Role != "user" {
		t.Fatalf("last turn must stay user, got %q", last.Role)
	}
	if !strings.Contains(last.Content, "@taskgraph") || !strings.Contains(last.Content, "build the whole feature") {
		t.Fatalf("hint must ride the user turn: %q", last.Content)
	}
}
