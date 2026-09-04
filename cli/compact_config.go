/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Session-level compaction controls: the /autocompact threshold override
 * and the dedicated summarizer model (CHATCLI_COMPACT_MODEL). compactConfig
 * is the single place every compaction call site builds its config, so the
 * override, the learned token ratio and the summarizer are applied uniformly
 * in chat, agent/coder, one-shot, RPC and /compact.
 */
package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"go.uber.org/zap"
)

// CompactModelEnv names the provider:model that serves Level 2 structured
// summaries. Empty keeps the session client (legacy behavior).
const CompactModelEnv = "CHATCLI_COMPACT_MODEL"

// autoCompactControl is the session-scoped threshold override set by
// /autocompact. Ratio 0 means "use the mode default".
type autoCompactControl struct {
	mu    sync.RWMutex
	ratio float64
}

func (c *autoCompactControl) get() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ratio
}

func (c *autoCompactControl) set(r float64) {
	c.mu.Lock()
	c.ratio = r
	c.mu.Unlock()
}

// parseAutoCompactSetting accepts "60%", "0.6", "150k", "150000" or
// "default"/"reset" and returns the budget ratio for the given window
// (0 = default). Percent and fraction forms are window-independent; token
// forms are converted against the model window.
func parseAutoCompactSetting(arg string, window int) (float64, error) {
	v := strings.ToLower(strings.TrimSpace(arg))
	switch v {
	case "", "default", "reset", "auto":
		return 0, nil
	}
	if strings.HasSuffix(v, "%") {
		p, err := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64)
		if err != nil || p <= 0 || p > 100 {
			return 0, fmt.Errorf("invalid percent %q", arg)
		}
		return p / 100, nil
	}
	if strings.HasSuffix(v, "k") || strings.HasSuffix(v, "m") {
		mult := 1000.0
		if strings.HasSuffix(v, "m") {
			mult = 1_000_000
		}
		n, err := strconv.ParseFloat(v[:len(v)-1], 64)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid token count %q", arg)
		}
		return tokensToRatio(n*mult, window)
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("invalid value %q", arg)
	}
	if f <= 1 {
		return f, nil // fraction of the window
	}
	return tokensToRatio(f, window)
}

func tokensToRatio(tokens float64, window int) (float64, error) {
	if window <= 0 {
		return 0, fmt.Errorf("unknown context window")
	}
	r := tokens / float64(window)
	if r <= 0 {
		return 0, fmt.Errorf("token count below the window minimum")
	}
	if r > 1 {
		r = 1
	}
	return r, nil
}

// handleAutoCompactCommand implements /autocompact [value|default].
func (cli *ChatCLI) handleAutoCompactCommand(_ context.Context, input string) {
	rest := strings.TrimSpace(strings.TrimPrefix(input, "/autocompact"))
	window := catalog.GetContextWindow(cli.Provider, cli.Model)
	if rest == "" {
		cfg := cli.compactConfig(cli.Provider, cli.Model)
		src := i18n.T("autocompact.source_default")
		if cli.autoCompact.get() > 0 {
			src = i18n.T("autocompact.source_override")
		}
		fmt.Println(colorize("  "+i18n.T("autocompact.current",
			fmt.Sprintf("%.0f%%", cfg.BudgetRatio*100),
			formatTokenCount(int64(float64(window)*cfg.BudgetRatio)), src), ColorCyan))
		return
	}
	ratio, err := parseAutoCompactSetting(rest, window)
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("autocompact.invalid", rest), ColorYellow))
		return
	}
	cli.autoCompact.set(ratio)
	if ratio == 0 {
		fmt.Println(colorize("  "+i18n.T("autocompact.reset"), ColorGreen))
		return
	}
	fmt.Println(colorize("  "+i18n.T("autocompact.set",
		fmt.Sprintf("%.0f%%", ratio*100), formatTokenCount(int64(float64(window)*ratio))), ColorGreen))
}

// compactConfig builds the compaction config every call site uses: the
// mode default (DefaultCompactConfig, learned token ratio included), the
// session /autocompact override, and the dedicated summarizer when one is
// configured.
func (cli *ChatCLI) compactConfig(provider, model string) CompactConfig {
	cfg := DefaultCompactConfig(provider, model)
	if cli == nil {
		return cfg
	}
	// The session's (tenant-scoped, persisted) calibrator wins over the
	// process-wide default the constructor consulted.
	if ratio, samples := cli.calibrator().CharsPerToken(provider, model); samples > 0 {
		cfg.CharsPerTokenPrecise = ratio
	}
	if r := cli.autoCompact.get(); r > 0 {
		cfg.BudgetRatio = r
	}
	cfg.SummarizerClient = cli.compactSummarizerClient()
	cfg.ExternalSummarizer = cli.externalSummarizer()
	if cfg.SummarizerClient != nil {
		cfg.SummarizerProvider, cfg.SummarizerModel = cli.compactSummarizerProvider, cli.compactSummarizerModel
	}
	return cfg
}

// compactSummarizerClient resolves CHATCLI_COMPACT_MODEL once per process
// into a client. Resolution failures are logged once and fall back to the
// session client — a misconfigured summarizer must never block compaction.
func (cli *ChatCLI) compactSummarizerClient() client.LLMClient {
	handle := strings.TrimSpace(os.Getenv(CompactModelEnv))
	if handle == "" || cli.manager == nil {
		return nil
	}
	// Cached by the env value: CHATCLI_COMPACT_MODEL is reloadable, so a
	// change (or a transient resolution failure) must not stay pinned for
	// the process the way a sync.Once did.
	cli.compactSummarizerMu.Lock()
	defer cli.compactSummarizerMu.Unlock()
	if cli.compactSummarizerHandle == handle {
		return cli.compactSummarizer
	}
	resolution := cli.resolveSkillClient(handle)
	// Usable means a DIFFERENT client than the session's: a handle that
	// resolves to nothing (unknown model, provider down) falls back to the
	// session client silently, and caching that would pin the fallback.
	if resolution.Client == nil || !resolution.Changed {
		if cli.logger != nil {
			cli.logger.Warn("compact summarizer model not usable, keeping the session client",
				zap.String("handle", handle), zap.String("reason", resolution.UserMessage))
		}
		// Not cached: the next call retries (a transient failure must not
		// pin the session client for the rest of the process).
		cli.compactSummarizer, cli.compactSummarizerHandle = nil, ""
		cli.compactSummarizerProvider, cli.compactSummarizerModel = "", ""
		return nil
	}
	cli.compactSummarizer = resolution.Client
	cli.compactSummarizerHandle = handle
	cli.compactSummarizerProvider, cli.compactSummarizerModel = resolution.Provider, resolution.Model
	return cli.compactSummarizer
}
