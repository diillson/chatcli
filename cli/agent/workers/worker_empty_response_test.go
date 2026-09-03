/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Guards for the "delegate returned nothing" failure class: an empty
 * completion must never be reported as a successful worker result, and the
 * session route (provider/model) must reach workers dispatched after a
 * mid-task switch.
 */
package workers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// truncatedMockClient returns an empty completion and reports a max_tokens
// stop reason, the shape of a thinking-only turn that ran out of budget.
type truncatedMockClient struct{ calls int }

func (m *truncatedMockClient) GetModelName() string { return "mock-truncated" }
func (m *truncatedMockClient) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	m.calls++
	return "", nil
}
func (m *truncatedMockClient) LastStopReason() string { return "max_tokens" }

func TestRunWorkerReAct_EmptyResponseIsNudgedThenSucceeds(t *testing.T) {
	client := &mockLLMClient{responses: []string{"", "final answer after nudge"}}
	config := WorkerReActConfig{MaxTurns: 5, SystemPrompt: "test", AllowedCommands: []string{"read"}, ReadOnly: true}

	result, err := RunWorkerReAct(context.Background(), config, "task", client, nil, NewSkillSet(), nil, zap.NewNop())
	if err != nil {
		t.Fatalf("one empty turn must be recoverable, got error: %v", err)
	}
	if !strings.Contains(result.Output, "final answer after nudge") {
		t.Fatalf("output = %q, want the post-nudge answer", result.Output)
	}
	if client.turn != 2 {
		t.Fatalf("expected exactly 2 LLM calls (empty + nudged), got %d", client.turn)
	}
}

func TestRunWorkerReAct_PersistentlyEmptyResponseFails(t *testing.T) {
	client := &mockLLMClient{responses: []string{"", "", "", ""}}
	config := WorkerReActConfig{MaxTurns: 10, SystemPrompt: "test", AllowedCommands: []string{"read"}, ReadOnly: true}

	result, err := RunWorkerReAct(context.Background(), config, "task", client, nil, NewSkillSet(), nil, zap.NewNop())
	if err == nil {
		t.Fatal("a persistently empty worker must fail, not report success")
	}
	if result == nil || result.Error == nil {
		t.Fatalf("result must carry the error, got %+v", result)
	}
	if !strings.Contains(err.Error(), "empty response") || !strings.Contains(err.Error(), "mock") {
		t.Fatalf("error must name the failure and the model, got: %v", err)
	}
	// Two nudges are tolerated, the third empty turn fails → 3 calls.
	if client.turn != maxEmptyWorkerTurns+1 {
		t.Fatalf("expected %d LLM calls, got %d", maxEmptyWorkerTurns+1, client.turn)
	}
}

func TestRunWorkerReAct_TruncatedEmptyResponseFailsImmediately(t *testing.T) {
	client := &truncatedMockClient{}
	config := WorkerReActConfig{MaxTurns: 5, SystemPrompt: "test", AllowedCommands: []string{"read"}, ReadOnly: true}

	_, err := RunWorkerReAct(context.Background(), config, "task", client, nil, NewSkillSet(), nil, zap.NewNop())
	if err == nil {
		t.Fatal("expected a truncation error")
	}
	if !strings.Contains(err.Error(), "stop_reason=max_tokens") {
		t.Fatalf("error must surface the stop reason, got: %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("a truncated empty turn must not be nudged (retry cannot help), got %d calls", client.calls)
	}
}

// Native mode with zero structured calls must still parse a textual
// <tool_call> block, exactly like the orchestrator loop does.
func TestResolveToolCalls_NativeModeFallsBackToXML(t *testing.T) {
	text := `<tool_call name="@coder" args="read --file main.go" />`
	resolved, parseErrs := resolveToolCalls(true, nil, text, 0, nil)
	if len(parseErrs) != 0 {
		t.Fatalf("unexpected parse errors: %+v", parseErrs)
	}
	if len(resolved) != 1 || resolved[0].Subcmd != "read" {
		t.Fatalf("expected one resolved read call, got %+v", resolved)
	}
	if resolved[0].Native {
		t.Fatal("XML-parsed call must not be flagged native")
	}
}

func TestFormatResults_EmptyOutputIsFlaggedAndCountsAsFailure(t *testing.T) {
	out := FormatResults([]AgentResult{{
		CallID:   "ac-1",
		Agent:    AgentTypePlanner,
		Task:     "plan it",
		Output:   "   \n",
		Duration: 10 * time.Millisecond,
	}})
	if strings.Contains(out, "Status: OK") {
		t.Fatalf("empty worker rendered as OK:\n%s", out)
	}
	if !strings.Contains(out, "Status: EMPTY") {
		t.Fatalf("expected EMPTY status:\n%s", out)
	}
	if !strings.Contains(out, "SQUAD FLOW") {
		t.Fatalf("empty result must trigger the re-dispatch nudge:\n%s", out)
	}

	// A real output still renders OK with no nudge.
	ok := FormatResults([]AgentResult{{CallID: "ac-2", Agent: AgentTypePlanner, Task: "t", Output: "done"}})
	if !strings.Contains(ok, "Status: OK") || strings.Contains(ok, "SQUAD FLOW") {
		t.Fatalf("non-empty result regressed:\n%s", ok)
	}
}

// The orchestrator refreshes the dispatcher's provider/model before every
// wave; workers built afterwards must be created on the refreshed pair.
func TestDispatcher_UpdateProviderModelReachesNextWave(t *testing.T) {
	mgr := &fullMockMgr{hintTrackingManager: &hintTrackingManager{providers: []string{"CLAUDEAI", "GOOGLEAI"}}}
	registry := NewRegistry()
	registry.Register(&mockAgent{agentType: AgentTypePlanner})

	d := NewDispatcher(registry, mgr, DispatcherConfig{
		MaxWorkers: 1, Provider: "CLAUDEAI", Model: "claude-sonnet-5", WorkerTimeout: 5 * time.Second,
	}, zap.NewNop())

	d.UpdateProviderModel("GOOGLEAI", "gemini-2.5-flash")
	if p, m := d.ProviderModel(); p != "GOOGLEAI" || m != "gemini-2.5-flash" {
		t.Fatalf("ProviderModel = %s/%s after update", p, m)
	}

	results := d.Dispatch(context.Background(), []AgentCall{{Agent: AgentTypePlanner, Task: "plan", ID: "c1"}})
	if len(results) != 1 || results[0].Error != nil {
		t.Fatalf("unexpected results: %+v", results)
	}
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if len(mgr.getClientLog) == 0 {
		t.Fatal("dispatcher built no client")
	}
	last := mgr.getClientLog[len(mgr.getClientLog)-1]
	if last.Provider != "GOOGLEAI" || last.Model != "gemini-2.5-flash" {
		t.Fatalf("worker built on %s/%s, want the refreshed GOOGLEAI/gemini-2.5-flash", last.Provider, last.Model)
	}
}
