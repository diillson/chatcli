/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ChatCLI - moa_adapter.go
 *
 * Implements plugins.MoaAdapter: runs a Mixture-of-Agents query through the
 * live LLM manager. Each member model answers the same prompt in parallel;
 * an aggregator model then synthesizes one best answer from all candidates.
 * Supplied to plugins.SetMoaAdapter at startup.
 */
package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/diillson/chatcli/cli/agent/moa"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/client"
	"go.uber.org/zap"
)

// maxMoaMembers bounds the default fan-out so an unqualified @moa call doesn't
// fire every configured provider at once.
const maxMoaMembers = 4

// moaPluginAdapter is the concrete plugins.MoaAdapter.
type moaPluginAdapter struct {
	cli *ChatCLI
}

// moaMember is one resolved participant.
type moaMember struct {
	label    string // "provider:model" for display
	provider string
	model    string
}

// moaResult is one member's answer (or error).
type moaResult struct {
	label  string
	answer string
	err    error
}

func (a *moaPluginAdapter) log() *zap.Logger {
	if a.cli != nil && a.cli.logger != nil {
		return a.cli.logger
	}
	return zap.NewNop()
}

// parseMember turns "provider" or "provider:model" into a moaMember. The
// provider case is preserved verbatim — GetClient does a case-sensitive lookup
// against the registered provider keys (which are upper-case); canonicalProvider
// reconciles user-typed case at resolve time.
func parseMember(spec string) moaMember {
	s := strings.TrimSpace(spec)
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return moaMember{label: s, provider: strings.TrimSpace(s[:i]), model: strings.TrimSpace(s[i+1:])}
	}
	return moaMember{label: s, provider: s}
}

// canonicalProviderName maps a user-supplied provider name to the actual
// registered key (case-insensitively), so "openai" → "OPENAI". GetClient does a
// case-sensitive map lookup, so this prevents the "all models failed" class of
// bug. Unknown names pass through so GetClient can surface a clear error. Shared
// by the @moa tool and the /moa command.
func (cli *ChatCLI) canonicalProviderName(name string) string {
	if cli.manager == nil {
		return name
	}
	for _, p := range cli.manager.GetAvailableProviders() {
		if strings.EqualFold(p, name) {
			return p
		}
	}
	return name
}

// moaClientFor returns an authenticated client for provider/model. When the
// target matches the session's active provider (and model, if given), it REUSES
// the live session client (cli.Client) — which carries whatever auth the session
// uses: OAuth tokens (preferred over API keys when logged in), client-forwarded
// tokens (server/gateway mode), or an API key. Other targets go through the
// manager, which itself prefers OAuth. Shared by @moa and /moa.
func (cli *ChatCLI) moaClientFor(provider, model string) (client.LLMClient, error) {
	cp := cli.canonicalProviderName(provider)
	if cli.Client != nil &&
		strings.EqualFold(strings.TrimSpace(cp), strings.TrimSpace(cli.Provider)) &&
		(strings.TrimSpace(model) == "" || strings.EqualFold(strings.TrimSpace(model), strings.TrimSpace(cli.Model))) {
		return cli.Client, nil
	}
	return cli.manager.GetClient(cp, model)
}

// clientFor delegates to the shared session-aware resolver.
func (a *moaPluginAdapter) clientFor(provider, model string) (client.LLMClient, error) {
	return a.cli.moaClientFor(provider, model)
}

// canonicalProvider delegates to the shared resolver.
func (a *moaPluginAdapter) canonicalProvider(name string) string {
	return a.cli.canonicalProviderName(name)
}

// resolveMembers builds the participant list. Explicit input wins; otherwise it
// falls back to CHATCLI_MOA_MODELS (so the env controls both @moa and /moa) and
// finally to up to maxMoaMembers configured providers.
func (a *moaPluginAdapter) resolveMembers(specs []string) []moaMember {
	// Default to CHATCLI_MOA_MODELS when the call didn't name members, so the
	// tool honors the same env the /moa command uses.
	if len(specs) == 0 {
		for _, r := range moa.ParseRefs(os.Getenv("CHATCLI_MOA_MODELS")) {
			if r.Model != "" {
				specs = append(specs, r.Provider+":"+r.Model)
			} else {
				specs = append(specs, r.Provider)
			}
		}
	}

	if len(specs) > 0 {
		out := make([]moaMember, 0, len(specs))
		for _, s := range specs {
			if strings.TrimSpace(s) == "" {
				continue
			}
			m := parseMember(s)
			m.provider = a.canonicalProvider(m.provider)
			if m.label == "" {
				m.label = m.provider
			}
			out = append(out, m)
		}
		return out
	}

	providers := a.cli.manager.GetAvailableProviders()
	sort.Strings(providers)
	if len(providers) > maxMoaMembers {
		providers = providers[:maxMoaMembers]
	}
	out := make([]moaMember, 0, len(providers))
	for _, p := range providers {
		out = append(out, moaMember{label: p, provider: p})
	}
	return out
}

// Run executes the Mixture-of-Agents flow.
func (a *moaPluginAdapter) Run(ctx context.Context, prompt string, memberSpecs []string, aggregatorSpec string) (string, error) {
	if a.cli == nil || a.cli.manager == nil {
		return "", fmt.Errorf("%s", i18n.T("moa.tool.unavailable"))
	}

	members := a.resolveMembers(memberSpecs)
	if len(members) == 0 {
		return "", fmt.Errorf("%s", i18n.T("moa.tool.no_members"))
	}

	// Fan out: each member answers the same prompt in parallel.
	results := make([]moaResult, len(members))
	var wg sync.WaitGroup
	for i, m := range members {
		wg.Add(1)
		go func(i int, m moaMember) {
			defer wg.Done()
			cl, err := a.clientFor(m.provider, m.model)
			if err != nil {
				results[i] = moaResult{label: m.label, err: err}
				return
			}
			ans, err := cl.SendPrompt(ctx, prompt, nil, 0)
			results[i] = moaResult{label: m.label, answer: ans, err: err}
		}(i, m)
	}
	wg.Wait()

	var ok []moaResult
	var firstErr error
	for _, r := range results {
		if r.err != nil {
			a.log().Warn("@moa member failed", zap.String("member", r.label), zap.Error(r.err))
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", r.label, r.err)
			}
			continue
		}
		if strings.TrimSpace(r.answer) != "" {
			ok = append(ok, r)
		}
	}
	if len(ok) == 0 {
		// Surface the underlying provider error so the failure is diagnosable
		// instead of an opaque "all failed".
		if firstErr != nil {
			return "", fmt.Errorf("%s: %w", i18n.T("moa.tool.all_failed"), firstErr)
		}
		return "", fmt.Errorf("%s", i18n.T("moa.tool.all_failed"))
	}

	// A single successful member needs no synthesis.
	if len(ok) == 1 {
		return ok[0].answer, nil
	}

	// Synthesize.
	aggProvider, aggModel := a.resolveAggregator(aggregatorSpec)
	aggClient, err := a.clientFor(aggProvider, aggModel)
	if err != nil {
		// Aggregator unavailable: fall back to the longest candidate rather
		// than failing the whole call.
		a.log().Warn("@moa aggregator unavailable, returning best candidate", zap.Error(err))
		return bestCandidate(ok), nil
	}

	synthPrompt := buildSynthesisPrompt(prompt, ok)
	final, err := aggClient.SendPrompt(ctx, synthPrompt, nil, 0)
	if err != nil || strings.TrimSpace(final) == "" {
		a.log().Warn("@moa synthesis failed, returning best candidate", zap.Error(err))
		return bestCandidate(ok), nil
	}

	labels := make([]string, 0, len(ok))
	for _, r := range ok {
		labels = append(labels, r.label)
	}
	header := i18n.T("moa.tool.synthesized", strings.Join(labels, ", "), aggProvider)
	return header + "\n\n" + final, nil
}

// resolveAggregator picks the synthesizer: explicit spec, else the session's
// current provider/model.
func (a *moaPluginAdapter) resolveAggregator(spec string) (provider, model string) {
	if strings.TrimSpace(spec) != "" {
		m := parseMember(spec)
		return a.canonicalProvider(m.provider), m.model
	}
	return a.cli.Provider, a.cli.Model
}

// List reports the providers available to participate.
func (a *moaPluginAdapter) List(ctx context.Context) (string, error) {
	if a.cli == nil || a.cli.manager == nil {
		return "", fmt.Errorf("%s", i18n.T("moa.tool.unavailable"))
	}
	providers := a.cli.manager.GetAvailableProviders()
	if len(providers) == 0 {
		return i18n.T("moa.tool.list.empty"), nil
	}
	sort.Strings(providers)
	var b strings.Builder
	b.WriteString(i18n.T("moa.tool.list.header"))
	b.WriteByte('\n')
	for _, p := range providers {
		b.WriteString("  • " + p + "\n")
	}
	b.WriteString(i18n.T("moa.tool.list.current", a.cli.Provider, a.cli.Model))
	return strings.TrimRight(b.String(), "\n"), nil
}

// buildSynthesisPrompt assembles the aggregator instruction. English on
// purpose — it's a model-facing instruction, not user-facing text.
func buildSynthesisPrompt(question string, candidates []moaResult) string {
	var b strings.Builder
	b.WriteString("You are an expert aggregator in a Mixture-of-Agents system. ")
	b.WriteString("Several independent models answered the same question. ")
	b.WriteString("Synthesize a single, best response: keep what is correct and well-supported, ")
	b.WriteString("reconcile disagreements (state the most defensible position and why), ")
	b.WriteString("drop hallucinations and errors, and do not mention that multiple models were used. ")
	b.WriteString("Answer in the same language as the question.\n\n")
	b.WriteString("## Question\n")
	b.WriteString(question)
	b.WriteString("\n\n## Candidate answers\n")
	for i, c := range candidates {
		fmt.Fprintf(&b, "\n### Candidate %d (%s)\n%s\n", i+1, c.label, c.answer)
	}
	b.WriteString("\n## Your synthesized answer\n")
	return b.String()
}

// bestCandidate is a cheap fallback heuristic: the longest non-empty answer.
func bestCandidate(results []moaResult) string {
	best := ""
	for _, r := range results {
		if len(r.answer) > len(best) {
			best = r.answer
		}
	}
	return best
}
