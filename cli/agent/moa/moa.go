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
func Run(ctx context.Context, prompt string, refs []Ref, factory Factory, aggregator Ref) (string, []RefResult, error) {
	if len(refs) == 0 {
		return "", nil, fmt.Errorf("no reference models configured")
	}

	results := make([]RefResult, len(refs))
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		go func(i int, ref Ref) {
			defer wg.Done()
			res := RefResult{Ref: ref}
			c, err := factory(ref.Provider, ref.Model)
			if err != nil {
				res.Err = err
				results[i] = res
				return
			}
			out, err := c.SendPrompt(ctx, prompt, nil, 0)
			res.Output, res.Err = out, err
			results[i] = res
		}(i, ref)
	}
	wg.Wait()

	if countOK(results) == 0 {
		return "", results, fmt.Errorf("all %d reference models failed", len(refs))
	}

	aggClient, err := factory(aggregator.Provider, aggregator.Model)
	if err != nil {
		return "", results, fmt.Errorf("aggregator unavailable: %w", err)
	}
	final, err := aggClient.SendPrompt(ctx, BuildAggregationPrompt(prompt, results), nil, 0)
	if err != nil {
		return "", results, fmt.Errorf("aggregation failed: %w", err)
	}
	return final, results, nil
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
