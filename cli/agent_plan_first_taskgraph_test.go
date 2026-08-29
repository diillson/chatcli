/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import "testing"

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
