/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package workers

import (
	"context"
	"fmt"
	"sync"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// UsageRecorder receives the summed token usage of one worker run so the
// session cost tracker can account for subagent spend — historically the
// largest untracked slice of an agent-mode session.
type UsageRecorder func(provider, model string, usage *models.UsageInfo)

// usageRecorderBox wraps the recorder func so Dispatcher can hold it via a
// comparable pointer field.
type usageRecorderBox struct{ fn UsageRecorder }

// SetUsageRecorder wires the callback that receives each worker's summed
// usage, attributed to the provider+model that actually served the worker.
// Nil disables recording (the default).
func (d *Dispatcher) SetUsageRecorder(fn UsageRecorder) {
	if fn == nil {
		d.usageRecorder = nil
		return
	}
	d.usageRecorder = &usageRecorderBox{fn: fn}
}

// usageTally accumulates the usage of every LLM call a worker makes.
type usageTally struct {
	mu    sync.Mutex
	total models.UsageInfo
	calls int
}

func (t *usageTally) add(u *models.UsageInfo) {
	if u == nil {
		return
	}
	t.mu.Lock()
	t.total.Merge(u)
	t.calls++
	t.mu.Unlock()
}

// take returns the accumulated usage, or nil when no call completed.
func (t *usageTally) take() *models.UsageInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.calls == 0 {
		return nil
	}
	cp := t.total
	return &cp
}

// tallyClient decorates an LLM client so every SendPrompt /
// SendPromptWithTools round-trip lands in the tally — real API usage when
// the inner client reports it, a character estimate otherwise. It preserves
// the tool-use capability of the inner client (SupportsNativeTools answers
// for it), so RunWorkerReAct routes exactly as it would undecorated.
type tallyClient struct {
	inner client.LLMClient
	tally *usageTally
}

func wrapWithUsageTally(inner client.LLMClient, tally *usageTally) client.LLMClient {
	return &tallyClient{inner: inner, tally: tally}
}

func (tc *tallyClient) GetModelName() string { return tc.inner.GetModelName() }

func (tc *tallyClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	resp, err := tc.inner.SendPrompt(ctx, prompt, history, maxTokens)
	if err == nil {
		tc.tally.add(client.GetUsageOrEstimate(tc.inner, promptChars(prompt, history), len(resp)))
	}
	return resp, err
}

// SendPromptWithTools delegates to the inner client's native tool path.
// Only reachable when SupportsNativeTools() returned true.
func (tc *tallyClient) SendPromptWithTools(ctx context.Context, prompt string, history []models.Message, tools []models.ToolDefinition, maxTokens int) (*models.LLMResponse, error) {
	tac, ok := client.AsToolAware(tc.inner)
	if !ok {
		return nil, fmt.Errorf("worker LLM client does not support native tools")
	}
	resp, err := tac.SendPromptWithTools(ctx, prompt, history, tools, maxTokens)
	if err == nil {
		if resp != nil && resp.Usage != nil {
			tc.tally.add(resp.Usage)
		} else {
			outChars := 0
			if resp != nil {
				outChars = len(resp.Content)
			}
			tc.tally.add(client.GetUsageOrEstimate(tc.inner, promptChars(prompt, history), outChars))
		}
	}
	return resp, err
}

// SupportsNativeTools answers for the inner client so the decorated client
// keeps its exact routing behavior.
func (tc *tallyClient) SupportsNativeTools() bool {
	if tac, ok := client.AsToolAware(tc.inner); ok {
		return tac.SupportsNativeTools()
	}
	return false
}

// LastUsage forwards the inner client's real usage so nested consumers of
// the decorated client still see it.
func (tc *tallyClient) LastUsage() *models.UsageInfo {
	if uac, ok := client.AsUsageAware(tc.inner); ok {
		return uac.LastUsage()
	}
	return nil
}

// LastStopReason forwards the inner client's stop reason (the ReAct loop
// uses it to detect max_tokens truncation).
func (tc *tallyClient) LastStopReason() string {
	if src, ok := client.AsStopReasonAware(tc.inner); ok {
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
