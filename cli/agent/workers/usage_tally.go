/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package workers

import (
	"context"
	"fmt"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// UsageRecorder receives the token usage of ONE worker LLM call so the
// session cost tracker can account for subagent spend — historically the
// largest untracked slice of an agent-mode session. Called per call, not
// per worker run: merging N calls into one UsageInfo would collapse the
// tracker's per-call provider-billed accounting (one call carrying
// usage.cost would mark ALL merged tokens as billed), and per-call
// recording also keeps the tracker live mid-run so the budget gate sees
// spend as it happens instead of only when the worker finishes.
type UsageRecorder func(provider, model string, usage *models.UsageInfo)

// BudgetGate is consulted before every worker LLM call; a non-nil error
// refuses the call (the session budget hard stop). Kept as a callback so
// the workers package stays decoupled from the CLI's cost tracker.
type BudgetGate func() error

// usageRecorderBox wraps the recorder func so Dispatcher can hold it via a
// comparable pointer field.
type usageRecorderBox struct{ fn UsageRecorder }

// budgetGateBox wraps the gate func so Dispatcher stays comparable.
type budgetGateBox struct{ fn BudgetGate }

// SetUsageRecorder wires the callback that receives each worker LLM call's
// usage, attributed to the provider+model that actually served the worker.
// Nil disables recording (the default).
func (d *Dispatcher) SetUsageRecorder(fn UsageRecorder) {
	if fn == nil {
		d.usageRecorder = nil
		return
	}
	d.usageRecorder = &usageRecorderBox{fn: fn}
}

// SetBudgetGate wires the session budget hard stop into every worker LLM
// call. Without it a dispatch wave in flight kept spending after the
// budget was exhausted — up to a full ReAct loop per worker, times the
// parallel wave. Nil disables the gate (the default).
func (d *Dispatcher) SetBudgetGate(fn BudgetGate) {
	if fn == nil {
		d.budgetGate = nil
		return
	}
	d.budgetGate = &budgetGateBox{fn: fn}
}

// recordingClient decorates a worker's LLM client so every SendPrompt /
// SendPromptWithTools round-trip is (a) refused when the budget gate says
// so and (b) recorded immediately — real API usage when the inner client
// reports it, a character estimate otherwise. It preserves the tool-use
// capability of the inner client (SupportsNativeTools answers for it), so
// RunWorkerReAct routes exactly as it would undecorated.
type recordingClient struct {
	inner  client.LLMClient
	record func(*models.UsageInfo) // never nil
	gate   func() error            // may be nil (no budget gate)
}

func wrapWithUsageRecording(inner client.LLMClient, record func(*models.UsageInfo), gate func() error) client.LLMClient {
	return &recordingClient{inner: inner, record: record, gate: gate}
}

func (rc *recordingClient) GetModelName() string { return rc.inner.GetModelName() }

func (rc *recordingClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	if rc.gate != nil {
		if err := rc.gate(); err != nil {
			return "", err
		}
	}
	resp, err := rc.inner.SendPrompt(ctx, prompt, history, maxTokens)
	if err == nil {
		rc.record(client.GetUsageOrEstimate(rc.inner, promptChars(prompt, history), len(resp)))
	}
	return resp, err
}

// SendPromptWithTools delegates to the inner client's native tool path.
// Only reachable when SupportsNativeTools() returned true.
func (rc *recordingClient) SendPromptWithTools(ctx context.Context, prompt string, history []models.Message, tools []models.ToolDefinition, maxTokens int) (*models.LLMResponse, error) {
	tac, ok := client.AsToolAware(rc.inner)
	if !ok {
		return nil, fmt.Errorf("worker LLM client does not support native tools")
	}
	if rc.gate != nil {
		if err := rc.gate(); err != nil {
			return nil, err
		}
	}
	resp, err := tac.SendPromptWithTools(ctx, prompt, history, tools, maxTokens)
	if err == nil {
		if resp != nil && resp.Usage != nil {
			rc.record(resp.Usage)
		} else {
			outChars := 0
			if resp != nil {
				outChars = len(resp.Content)
			}
			rc.record(client.GetUsageOrEstimate(rc.inner, promptChars(prompt, history), outChars))
		}
	}
	return resp, err
}

// SupportsNativeTools answers for the inner client so the decorated client
// keeps its exact routing behavior.
func (rc *recordingClient) SupportsNativeTools() bool {
	if tac, ok := client.AsToolAware(rc.inner); ok {
		return tac.SupportsNativeTools()
	}
	return false
}

// LastUsage forwards the inner client's real usage so nested consumers of
// the decorated client still see it.
func (rc *recordingClient) LastUsage() *models.UsageInfo {
	if uac, ok := client.AsUsageAware(rc.inner); ok {
		return uac.LastUsage()
	}
	return nil
}

// LastStopReason forwards the inner client's stop reason (the ReAct loop
// uses it to detect max_tokens truncation).
func (rc *recordingClient) LastStopReason() string {
	if src, ok := client.AsStopReasonAware(rc.inner); ok {
		return src.LastStopReason()
	}
	return ""
}

// promptChars sizes the outgoing payload for the estimate fallback.
func promptChars(prompt string, history []models.Message) int {
	n := len(prompt)
	for _, m := range history {
		n += len(m.Content)
	}
	return n
}
