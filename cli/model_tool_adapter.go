/*
 * ChatCLI - @model tool adapter
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements plugins.ModelRoutingAdapter over the live session: lists the
 * configured providers/models enriched with routing metadata (pricing tier,
 * cost per 1M tokens, context window, capabilities), applies/clears the
 * agent-loop route override the AI picks via "@model use", and runs
 * "@model delegate" one-shots on another model without touching the main
 * loop's history or prompt cache.
 *
 * Resolution reuses the same pipeline as skill `model:` frontmatter
 * (client.ResolveModelRouting), so qualified "PROVIDER:model" handles are
 * deterministic and bare names fall back to catalog/family heuristics.
 * Supplied to plugins.SetModelRoutingAdapter at startup.
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/utils"
)

// modelListTimeout bounds the live model listing across providers, mirroring
// the RPC surface: providers that answer in time contribute API models, slow
// ones degrade to the static catalog for this call.
const modelListTimeout = 10 * time.Second

// modelListPerProviderCap bounds the unfiltered listing so a 15-provider
// setup doesn't flood the model's context. The truncation is announced and
// the provider filter lifts it.
const modelListPerProviderCap = 25

// isModelToolEnabled gates the @model tool registration. Default on; set
// CHATCLI_AGENT_MODEL_TOOL=false to keep the AI from routing models itself
// (cost-governance kill switch).
func isModelToolEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(utils.GetEnvOrDefault("CHATCLI_AGENT_MODEL_TOOL", "true")))
	return v != "false" && v != "0" && v != "off"
}

// --- ChatCLI route-override state -------------------------------------------

// setAgentRouteOverride records the AI-chosen "PROVIDER:model" handle. It is
// honored per turn by AgentMode.clientAndCtxForTurn and never mutates the
// session's own Client/Provider/Model.
func (cli *ChatCLI) setAgentRouteOverride(handle string) {
	cli.agentRouteOverrideMu.Lock()
	cli.agentRouteOverride = handle
	cli.agentRouteOverrideMu.Unlock()
}

// agentRouteOverrideHandle returns the active override ("" = none).
func (cli *ChatCLI) agentRouteOverrideHandle() string {
	cli.agentRouteOverrideMu.RLock()
	defer cli.agentRouteOverrideMu.RUnlock()
	return cli.agentRouteOverride
}

// clearAgentRouteOverride removes the override (task end, reset, new Run).
func (cli *ChatCLI) clearAgentRouteOverride() {
	cli.setAgentRouteOverride("")
}

// --- adapter -----------------------------------------------------------------

// modelRoutingAdapter is the concrete plugins.ModelRoutingAdapter.
type modelRoutingAdapter struct{ cli *ChatCLI }

func (a *modelRoutingAdapter) ready() error {
	if a.cli == nil || a.cli.manager == nil {
		return errors.New(i18n.T("model.tool.unavailable"))
	}
	return nil
}

// List renders providers and models with routing metadata. providerFilter
// ("" = all) narrows to one provider and lifts the per-provider cap.
func (a *modelRoutingAdapter) List(ctx context.Context, providerFilter string) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	cli := a.cli

	names := cli.manager.GetAvailableProviders()
	if providerFilter != "" {
		var kept []string
		for _, n := range names {
			if strings.EqualFold(n, providerFilter) {
				kept = append(kept, n)
			}
		}
		if len(kept) == 0 {
			return "", fmt.Errorf("%s", i18n.T("model.tool.list.unknown_provider", providerFilter, strings.Join(names, ", ")))
		}
		names = kept
	}
	sort.Strings(names)

	type providerModels struct {
		models []client.ModelInfo
		source string
	}
	listed := make([]providerModels, len(names))
	listCtx, cancel := context.WithTimeout(ctx, modelListTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			models, err := cli.manager.ListModelsForProvider(listCtx, name)
			if err == nil && len(models) > 0 {
				source := "catalog"
				for _, m := range models {
					if m.Source != client.ModelSourceCatalog {
						source = "api+catalog"
						break
					}
				}
				listed[i] = providerModels{models: models, source: source}
				return
			}
			var fallback []client.ModelInfo
			for _, meta := range catalog.ListByProvider(name) {
				fallback = append(fallback, client.ModelInfo{ID: meta.ID, Source: client.ModelSourceCatalog})
			}
			listed[i] = providerModels{models: fallback, source: "catalog"}
		}(i, name)
	}
	wg.Wait()

	activeHandle := cli.Provider + ":" + cli.Model
	override := cli.agentRouteOverrideHandle()
	overrideLabel := override
	if overrideLabel == "" {
		overrideLabel = i18n.T("model.tool.none")
	}

	var b strings.Builder
	b.WriteString(i18n.T("model.tool.list.header", activeHandle, overrideLabel))
	b.WriteString("\n")
	b.WriteString(i18n.T("model.tool.list.hint"))
	b.WriteString("\n")

	perProviderCap := modelListPerProviderCap
	if providerFilter != "" {
		perProviderCap = 0 // filtered listing is never truncated
	}
	for i, name := range names {
		entry := listed[i]
		b.WriteString(fmt.Sprintf("\n%s (%d, %s)\n", name, len(entry.models), entry.source))
		shown := 0
		for _, m := range entry.models {
			if perProviderCap > 0 && shown >= perProviderCap {
				b.WriteString("  " + i18n.T("model.tool.list.truncated", len(entry.models)-shown, name) + "\n")
				break
			}
			b.WriteString(renderModelRoutingLine(name, m.ID, activeHandle, override))
			shown++
		}
	}
	return b.String(), nil
}

// renderModelRoutingLine formats one model entry with the metadata the AI
// needs to route: qualified handle, price-derived tier, cost per 1M tokens,
// context window and catalog capabilities (when known).
func renderModelRoutingLine(provider, id, activeHandle, override string) string {
	handle := provider + ":" + id
	parts := []string{"  " + handle}

	tier, in, out := modelRoutingTier(provider, id)
	parts = append(parts, "tier="+tier)
	if in > 0 || out > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f/$%.2f per 1M", in, out))
	}
	if meta, ok := catalog.Resolve(provider, id); ok {
		if meta.ContextWindow > 0 {
			parts = append(parts, "ctx="+formatTokenCount64(int64(meta.ContextWindow)))
		}
		if len(meta.Capabilities) > 0 {
			parts = append(parts, "caps="+strings.Join(meta.Capabilities, ","))
		}
	}
	switch handle {
	case override:
		parts = append(parts, "« "+i18n.T("model.tool.list.routed"))
	case activeHandle:
		parts = append(parts, "« "+i18n.T("model.tool.list.active"))
	}
	return strings.Join(parts, "  ") + "\n"
}

// modelRoutingTier derives a routing tier from the cost tables so the AI can
// reason over a label instead of raw prices. "unmetered" covers local
// backends (Ollama), flat subscriptions (Devin) and unknown pricing.
func modelRoutingTier(provider, model string) (tier string, in, out float64) {
	in, out = getModelPricing(provider, model)
	if in == 0 && out == 0 {
		return "unmetered", 0, 0
	}
	// Blend input and output prices; output dominates agent turns far less
	// than input (history >> completion), hence the 1:5 weighting.
	score := in + out/5
	switch {
	case score <= 2.5:
		return "fast-cheap", in, out
	case score <= 8:
		return "balanced", in, out
	default:
		return "frontier", in, out
	}
}

// Use routes the rest of the task to handle, sticky until Reset or task end.
func (a *modelRoutingAdapter) Use(ctx context.Context, handle string) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	cli := a.cli
	cli.ensureModelCacheWarm(ctx)

	resolution := cli.resolveSkillClient(handle)
	if !resolution.Changed {
		if resolution.UserMessage != "" {
			// Actionable, model-facing: names the provider/model and why it
			// was refused, so the AI can pick another handle or ask the user.
			return "", errors.New(resolution.UserMessage)
		}
		// The handle is the session's own model — an override would be a
		// no-op, so clear any previous one instead.
		cli.clearAgentRouteOverride()
		return i18n.T("model.tool.already_active", resolution.Provider+":"+resolution.Model), nil
	}

	qualified := resolution.Provider + ":" + resolution.Model
	cli.setAgentRouteOverride(qualified)

	msg := i18n.T("model.tool.switched", qualified)
	if resolution.CrossProvider {
		msg += "\n" + i18n.T("model.tool.cross_provider_note", resolution.Provider)
	}
	if _, known := catalog.Resolve(resolution.Provider, resolution.Model); known &&
		!catalog.HasCapability(resolution.Provider, resolution.Model, "tools") {
		msg += "\n" + i18n.T("model.tool.no_tools_note", qualified)
	}
	return msg, nil
}

// Reset clears the route override.
func (a *modelRoutingAdapter) Reset() (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	cli := a.cli
	if cli.agentRouteOverrideHandle() == "" {
		return i18n.T("model.tool.reset.none", cli.Provider+":"+cli.Model), nil
	}
	cli.clearAgentRouteOverride()
	return i18n.T("model.tool.reset.done", cli.Provider+":"+cli.Model), nil
}

// Status reports the session model and the active override, if any.
func (a *modelRoutingAdapter) Status() (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	cli := a.cli
	session := cli.Provider + ":" + cli.Model
	if override := cli.agentRouteOverrideHandle(); override != "" {
		return i18n.T("model.tool.status.override", session, override), nil
	}
	return i18n.T("model.tool.status.no_override", session), nil
}

// Delegate one-shots prompt on handle's model and returns the answer. The
// main loop, its history and its provider prompt cache stay untouched — the
// delegated model sees ONLY the prompt.
func (a *modelRoutingAdapter) Delegate(ctx context.Context, handle, prompt string, maxTokens int) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	cli := a.cli
	cli.ensureModelCacheWarm(ctx)

	resolution := cli.resolveSkillClient(handle)
	if !resolution.Changed && resolution.UserMessage != "" {
		return "", errors.New(resolution.UserMessage)
	}
	if maxTokens < 0 {
		maxTokens = 0
	}

	if resolution.Client == nil {
		return "", errors.New(i18n.T("model.tool.delegate.failed", resolution.Provider+":"+resolution.Model))
	}
	answer, err := resolution.Client.SendPrompt(ctx, prompt, nil, maxTokens)
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("model.tool.delegate.failed", resolution.Provider+":"+resolution.Model), err)
	}
	if strings.TrimSpace(answer) == "" {
		// An empty completion must surface as a failure, not as a header
		// with nothing under it — the calling model would otherwise take
		// it as a legitimate (blank) answer.
		return "", fmt.Errorf("%s: %s", i18n.T("model.tool.delegate.failed", resolution.Provider+":"+resolution.Model),
			i18n.T("model.tool.delegate.empty"))
	}

	if cli.costTracker != nil {
		usage := client.GetUsageOrEstimate(resolution.Client, len(prompt), len(answer))
		cli.costTracker.RecordRealUsage(resolution.Provider, resolution.Model, usage)
	}

	return i18n.T("model.tool.delegate.header", resolution.Provider+":"+resolution.Model) + "\n\n" + answer, nil
}
