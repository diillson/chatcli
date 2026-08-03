/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Package moa implements Mixture-of-Agents: fan a prompt out to several
 * reference models in parallel, then synthesize their answers with an
 * aggregator model. Based on Wang et al. (arXiv:2406.04692).
 *
 * It is fully provider-agnostic: references are (provider, model) pairs and
 * clients are obtained through a factory, so any of the configured providers
 * can participate as a reference or as the aggregator. The package contains
 * no provider-specific logic and is unit-tested with a fake client.
 */
package moa

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/models"
)

// Ref identifies a participating model.
type Ref struct {
	Provider string
	Model    string
}

func (r Ref) String() string {
	if r.Model == "" {
		return r.Provider
	}
	return r.Provider + ":" + r.Model
}

// Client is the minimal contract MoA needs — satisfied by client.LLMClient.
type Client interface {
	SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error)
}

// Factory resolves a client for a (provider, model) pair.
type Factory func(provider, model string) (Client, error)

// Turn runs one participant's complete turn: a single LLM exchange plus
// whatever bounded, read-only tool rounds the host wires in (knowledge
// retrieval, CCR recall, ...). The package stays provider- and tool-agnostic:
// it only sequences turns, the host decides what a turn is capable of.
type Turn func(ctx context.Context, ref Ref, prompt string, history []models.Message) (string, error)

// factoryTurn adapts a plain client Factory into a Turn with no tool rounds —
// the classic single-shot SendPrompt exchange.
func factoryTurn(factory Factory) Turn {
	return func(ctx context.Context, ref Ref, prompt string, history []models.Message) (string, error) {
		c, err := factory(ref.Provider, ref.Model)
		if err != nil {
			return "", err
		}
		return c.SendPrompt(ctx, prompt, history, 0)
	}
}

// RefResult is one reference model's outcome.
type RefResult struct {
	Ref    Ref
	Output string
	Err    error
}

// OK reports whether the reference produced usable output.
func (r RefResult) OK() bool { return r.Err == nil && strings.TrimSpace(r.Output) != "" }

// ParseRefs parses "openai:gpt-5, claudeai:opus, googleai" into Refs.
// A bare token (no colon) is treated as a provider with the default model.
func ParseRefs(raw string) []Ref {
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == ';' })
	refs := make([]Ref, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		provider, model, found := strings.Cut(part, ":")
		ref := Ref{Provider: strings.TrimSpace(provider)}
		if found {
			ref.Model = strings.TrimSpace(model)
		}
		refs = append(refs, ref)
	}
	return refs
}

// Run queries every reference concurrently, then calls the aggregator to
// synthesize a final answer from the successful responses. It returns an
// error only when nothing usable was produced (no reference succeeded, or the
// aggregator itself failed). Reference errors are otherwise tolerated — MoA
// degrades gracefully to the models that did answer.
// Run synthesizes an answer with no prior conversation context. Preserved for
// API compatibility; delegates to RunWithHistory.
func Run(ctx context.Context, prompt string, refs []Ref, factory Factory, aggregator Ref) (string, []RefResult, error) {
	return RunWithHistory(ctx, prompt, nil, refs, factory, aggregator)
}

// RunWithHistory is Run with the prior conversation, passed to each proposer so
// a follow-up MoA is context-aware.
func RunWithHistory(ctx context.Context, prompt string, history []models.Message, refs []Ref, factory Factory, aggregator Ref) (string, []RefResult, error) {
	return RunSession(ctx, prompt, history, refs, aggregator, factoryTurn(factory))
}

// RunSession is the full MoA entry point: every reference runs its turn
// concurrently over the shared history, then the aggregator's turn
// synthesizes the successful answers. The turn executor carries whatever
// capabilities the host granted (system context, tool rounds), so proposers
// and aggregator behave like the host's regular conversation turns.
func RunSession(ctx context.Context, prompt string, history []models.Message, refs []Ref, aggregator Ref, turn Turn) (string, []RefResult, error) {
	if len(refs) == 0 {
		return "", nil, fmt.Errorf("no reference models configured")
	}
	if turn == nil {
		return "", nil, fmt.Errorf("no turn executor configured")
	}

	results := make([]RefResult, len(refs))
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		go func(i int, ref Ref) {
			defer wg.Done()
			// Each panel member registers as a run so the /agents view shows
			// the MoA fan-out alongside worker dispatches.
			refCtx, liveRun := runs.Default().Begin(ctx, runs.Info{
				Kind:  runs.KindMoA,
				Agent: ref.String(),
				Task:  prompt,
			})
			out, err := turn(refCtx, ref, prompt, history)
			liveRun.End(err)
			results[i] = RefResult{Ref: ref, Output: out, Err: err}
		}(i, ref)
	}
	wg.Wait()

	if countOK(results) == 0 {
		// Surface the first underlying error so the failure is diagnosable
		// (provider not available, 401, etc.) instead of an opaque message.
		for _, r := range results {
			if r.Err != nil {
				return "", results, fmt.Errorf("all %d reference models failed: %s: %w", len(refs), r.Ref.String(), r.Err)
			}
		}
		return "", results, fmt.Errorf("all %d reference models failed", len(refs))
	}

	final, err := turn(ctx, aggregator, BuildAggregationPrompt(prompt, results), historyForAggregation(history, prompt))
	if err != nil {
		return "", results, fmt.Errorf("aggregation failed: %w", err)
	}
	return final, results, nil
}

// historyForAggregation prepares the shared history for the aggregator turn.
// The aggregation prompt embeds the user's request, so a trailing user turn
// that duplicates it is dropped — otherwise two consecutive user messages
// would reach the wire. Everything else (system context, prior conversation)
// is kept so the aggregator synthesizes with the same briefing the proposers
// had.
func historyForAggregation(history []models.Message, prompt string) []models.Message {
	if n := len(history); n > 0 && history[n-1].Role == "user" && history[n-1].Content == prompt {
		return history[:n-1]
	}
	return history
}

func countOK(results []RefResult) int {
	n := 0
	for _, r := range results {
		if r.OK() {
			n++
		}
	}
	return n
}

// BuildAggregationPrompt assembles the synthesizer prompt from the reference
// answers, following the MoA pattern: present the candidate responses and ask
// the aggregator to produce a single best answer.
func BuildAggregationPrompt(userPrompt string, results []RefResult) string {
	var b strings.Builder
	b.WriteString("You are an aggregator. Several models answered the user's request below. ")
	b.WriteString("Synthesize a single, correct, high-quality response. Do not mention the models or that aggregation occurred. ")
	b.WriteString("Resolve contradictions by reasoning about correctness, and keep the best details from each.\n\n")
	b.WriteString("## User request\n")
	b.WriteString(userPrompt)
	b.WriteString("\n\n## Candidate responses\n")
	idx := 1
	for _, r := range results {
		if !r.OK() {
			continue
		}
		fmt.Fprintf(&b, "\n### Candidate %d\n%s\n", idx, strings.TrimSpace(r.Output))
		idx++
	}
	b.WriteString("\n## Your synthesized answer\n")
	return b.String()
}
