/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /config quality renders the seven-pattern quality pipeline state. The
 * pipeline lives in cli/agent/quality and is wired into the dispatcher
 * by initMultiAgent. This file only reads the live snapshot — it does
 * not parse env vars on its own.
 */
package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/i18n"
)

// showConfigQuality renders the seven-pattern pipeline state.
//
// When agentMode hasn't been initialized yet (e.g. before the first agent
// invocation), the section falls back to env-derived defaults so the user
// can still see how the pipeline will be configured.
func (cli *ChatCLI) showConfigQuality() {
	sectionHeader("✨", "cfg.section.quality.title", ColorCyan)
	p := uiPrefix(ColorCyan)

	cfg := quality.LoadFromEnv()
	preCount, postCount := 0, 0
	if cli.agentMode != nil && cli.agentMode.qualityPipeline != nil {
		cfg = cli.agentMode.qualityConfig
		preCount, postCount = cli.agentMode.qualityPipeline.HookCounts()
	}

	// Master switch + summary
	kv(p, "CHATCLI_QUALITY_ENABLED", boolLabel(cfg.Enabled))
	kv(p, i18n.T("cfg.kv.quality.hooks_registered"),
		fmt.Sprintf("pre=%d, post=%d", preCount, postCount))

	// Refine
	fmt.Println(p)
	subheader(p, "cfg.sub.quality.refine")
	kv(p, "CHATCLI_QUALITY_REFINE_ENABLED", boolLabel(cfg.Refine.Enabled))
	kv(p, "CHATCLI_QUALITY_REFINE_MAX_PASSES", fmt.Sprintf("%d", cfg.Refine.MaxPasses))
	kv(p, "CHATCLI_QUALITY_REFINE_MIN_BYTES", fmt.Sprintf("%d", cfg.Refine.MinDraftBytes))
	kv(p, "CHATCLI_QUALITY_REFINE_EPSILON", fmt.Sprintf("%d", cfg.Refine.EpsilonChars))
	kv(p, "CHATCLI_QUALITY_REFINE_EXCLUDE", listLabel(cfg.Refine.ExcludeAgents))

	// Verify
	fmt.Println(p)
	subheader(p, "cfg.sub.quality.verify")
	kv(p, "CHATCLI_QUALITY_VERIFY_ENABLED", boolLabel(cfg.Verify.Enabled))
	kv(p, "CHATCLI_QUALITY_VERIFY_NUM_QUESTIONS", fmt.Sprintf("%d", cfg.Verify.NumQuestions))
	kv(p, "CHATCLI_QUALITY_VERIFY_REWRITE", boolLabel(cfg.Verify.RewriteOnDiscrepancy))
	kv(p, "CHATCLI_QUALITY_VERIFY_EXCLUDE", listLabel(cfg.Verify.ExcludeAgents))

	// Reflexion
	fmt.Println(p)
	subheader(p, "cfg.sub.quality.reflexion")
	kv(p, "CHATCLI_QUALITY_REFLEXION_ENABLED", boolLabel(cfg.Reflexion.Enabled))
	kv(p, "CHATCLI_QUALITY_REFLEXION_ON_ERROR", boolLabel(cfg.Reflexion.OnError))
	kv(p, "CHATCLI_QUALITY_REFLEXION_ON_HALLUCINATION", boolLabel(cfg.Reflexion.OnHallucination))
	kv(p, "CHATCLI_QUALITY_REFLEXION_ON_LOW_QUALITY", boolLabel(cfg.Reflexion.OnLowQuality))
	kv(p, "CHATCLI_QUALITY_REFLEXION_PERSIST", boolLabel(cfg.Reflexion.Persist))

	// PlanFirst
	fmt.Println(p)
	subheader(p, "cfg.sub.quality.plan_first")
	kv(p, "CHATCLI_QUALITY_PLAN_FIRST_MODE", cfg.PlanFirst.Mode)
	kv(p, "CHATCLI_QUALITY_PLAN_FIRST_THRESHOLD", fmt.Sprintf("%d", cfg.PlanFirst.ComplexityThreshold))

	// HyDE
	fmt.Println(p)
	subheader(p, "cfg.sub.quality.hyde")
	kv(p, "CHATCLI_QUALITY_HYDE_ENABLED", boolLabel(cfg.HyDE.Enabled))
	kv(p, "CHATCLI_QUALITY_HYDE_USE_VECTORS", boolLabel(cfg.HyDE.UseVectors))
	provider := cfg.HyDE.EmbedProvider
	if provider == "" {
		provider = i18n.T("cfg.val.none")
	}
	kv(p, "CHATCLI_QUALITY_HYDE_PROVIDER", provider)
	kv(p, "CHATCLI_EMBED_PROVIDER", envOr("CHATCLI_EMBED_PROVIDER"))
	kv(p, "CHATCLI_EMBED_MODEL", envOr("CHATCLI_EMBED_MODEL"))
	kv(p, "CHATCLI_QUALITY_HYDE_NUM_KEYWORDS", fmt.Sprintf("%d", cfg.HyDE.NumKeywords))
	if cli.memoryStore != nil {
		if vi := cli.memoryStore.VectorIndex(); vi != nil {
			kv(p, i18n.T("cfg.kv.quality.vector_provider"), vi.ProviderName())
			kv(p, i18n.T("cfg.kv.quality.vector_count"), fmt.Sprintf("%d", vi.Count()))
		} else {
			kv(p, i18n.T("cfg.kv.quality.vector_state"), i18n.T("cfg.val.not_attached"))
		}
	}

	// Reasoning backbone
	fmt.Println(p)
	subheader(p, "cfg.sub.quality.reasoning")
	kv(p, "CHATCLI_QUALITY_REASONING_MODE", cfg.Reasoning.Mode)
	kv(p, "CHATCLI_QUALITY_REASONING_BUDGET", fmt.Sprintf("%d", cfg.Reasoning.Budget))
	kv(p, "CHATCLI_QUALITY_REASONING_AUTO_AGENTS", listLabel(cfg.Reasoning.AutoAgents))

	sectionEnd(ColorCyan)
}

// boolLabel renders a quality config bool using the standard on/off
// vocabulary already registered for /config sections.
func boolLabel(b bool) string {
	if b {
		return i18n.T("cfg.val.enabled")
	}
	return i18n.T("cfg.val.disabled")
}

// listLabel renders a string slice as "a, b, c" or the localized "(none)"
// placeholder when empty.
func listLabel(items []string) string {
	if len(items) == 0 {
		return i18n.T("cfg.val.none")
	}
	return strings.Join(items, ", ")
}
