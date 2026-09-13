/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Prompt-cache keep-alive: cheaper than the hour-long entry on models
 * whose cache reads cost a small fraction of the input price.
 *
 * A cache read refreshes the entry's timer for free. On Claude Fable 5.1
 * a read is 2.5% of the input price, so re-sending the previous request
 * with max_tokens: 0 every 4.5 minutes while the user is idle keeps the
 * 5-minute entry alive for a fraction of what the hour-long ttl charges
 * (2x the input price on every token written) — unless the pause runs
 * toward an hour, which is where the refreshes stop. On the other Claude
 * models a read is 10% of input and the arithmetic favors the hour, so
 * they keep the evidence-based ttl promotion; on every other provider
 * there is no such request, and the scheduler never arms.
 *
 * The scheduler restarts from every provider-reported usage (the tracker
 * hook): the request itself refreshed the entry, so the next refresh is
 * due one lifetime later, minus a lead. Refreshes are booked like any
 * request, so /cost shows what they cost.
 */
package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	llmclient "github.com/diillson/chatcli/llm/client"
	"go.uber.org/zap"
)

// PromptCacheKeepAliveEnv selects the keep-alive: "auto" (default) arms
// it on models whose cache reads cost at most keepAliveReadShare of the
// input price, "on" arms it on every client that can refresh, "off"
// never does.
const PromptCacheKeepAliveEnv = "CHATCLI_PROMPT_CACHE_KEEPALIVE"

const (
	// keepAliveReadShare is the read/input price ratio at or below which
	// refreshing beats the hour-long ttl (Fable 5.1 reads at 0.025x).
	keepAliveReadShare = 0.05
	// keepAliveLead is how long before the entry expires a refresh goes
	// out; the lifetime is measured from the START of the refreshing
	// request, so the margin covers the request itself.
	keepAliveLead = 30 * time.Second
	// keepAliveMaxRefreshes bounds one idle stretch: twelve refreshes of a
	// 5-minute entry is close to an hour, past which the hour-long ttl
	// would have expired too.
	keepAliveMaxRefreshes = 12
)

// promptCacheKeepAlive is the scheduler state.
type promptCacheKeepAlive struct {
	mu        sync.Mutex
	timer     *time.Timer
	gen       uint64 // bumps on every arm/cancel so a stale timer is a no-op
	refreshes int    // refreshes sent in the current idle stretch
	provider  string
	model     string
	// refreshing is set while a refresh's own usage is being booked, so
	// the tracker hook does not treat it as a user turn.
	refreshing bool
}

// keepAliveMode returns the normalized env value.
func keepAliveMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(PromptCacheKeepAliveEnv))) {
	case "on", "true", "1":
		return "on"
	case "off", "false", "0":
		return "off"
	}
	return "auto"
}

// keepAliveApplies reports whether the active route is kept warm by
// refreshes rather than by the hour-long ttl.
func (cli *ChatCLI) keepAliveApplies(provider, model string) bool {
	if cli == nil || cli.unattended {
		return false
	}
	switch keepAliveMode() {
	case "off":
		return false
	case "on":
		return true
	}
	inputCost, _, known := lookupModelPricing(provider, model)
	if !known || inputCost <= 0 {
		return false
	}
	_, readCost := getCachePricing(provider, model)
	return readCost > 0 && readCost/inputCost <= keepAliveReadShare
}

// noteRealUsageForKeepAlive is the tracker hook: every booked request
// restarts the schedule for the route that made it.
func (cli *ChatCLI) noteRealUsageForKeepAlive(provider, model string) {
	if cli == nil {
		return
	}
	ka := &cli.cacheKeepAlive
	ka.mu.Lock()
	refreshing := ka.refreshing
	ka.mu.Unlock()
	if refreshing {
		return
	}
	applies := cli.keepAliveApplies(provider, model)
	llmclient.SetPromptCacheKeepAlivePreferred(applies)
	if !applies || llmclient.AnthropicCacheTTL() != "5m" {
		cli.cancelPromptCacheKeepAlive()
		return
	}
	cli.armPromptCacheKeepAlive(provider, model, 0)
}

// armPromptCacheKeepAlive schedules the next refresh one lifetime minus
// the lead from now.
func (cli *ChatCLI) armPromptCacheKeepAlive(provider, model string, refreshes int) {
	ka := &cli.cacheKeepAlive
	ka.mu.Lock()
	defer ka.mu.Unlock()
	if ka.timer != nil {
		ka.timer.Stop()
	}
	ka.gen++
	gen := ka.gen
	ka.provider, ka.model, ka.refreshes = provider, model, refreshes
	delay := llmclient.PromptCacheTTLDuration() - keepAliveLead
	if delay < time.Second {
		delay = time.Second
	}
	ka.timer = time.AfterFunc(delay, func() { cli.firePromptCacheKeepAlive(gen) })
}

// cancelPromptCacheKeepAlive stops the schedule: the conversation was
// cleared or the route no longer qualifies.
func (cli *ChatCLI) cancelPromptCacheKeepAlive() {
	if cli == nil {
		return
	}
	ka := &cli.cacheKeepAlive
	ka.mu.Lock()
	defer ka.mu.Unlock()
	if ka.timer != nil {
		ka.timer.Stop()
		ka.timer = nil
	}
	ka.gen++
	ka.refreshes = 0
}

// firePromptCacheKeepAlive sends one refresh when the schedule that armed
// it is still current, books its usage, and re-arms until the idle
// stretch has run its course. Returns whether a refresh was sent.
func (cli *ChatCLI) firePromptCacheKeepAlive(gen uint64) bool {
	ka := &cli.cacheKeepAlive
	ka.mu.Lock()
	if gen != ka.gen {
		ka.mu.Unlock()
		return false
	}
	provider, model, refreshes := ka.provider, ka.model, ka.refreshes
	ka.mu.Unlock()
	if refreshes >= keepAliveMaxRefreshes {
		cli.logger.Debug("prompt cache keep-alive: idle stretch over, letting the entry expire",
			zap.Int("refreshes", refreshes))
		cli.cancelPromptCacheKeepAlive()
		return false
	}
	warmer, ok := cli.getClient().(llmclient.PromptCacheKeepAliver)
	if !ok {
		cli.cancelPromptCacheKeepAlive()
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	usage, err := warmer.KeepPromptCacheWarm(ctx)
	if err != nil {
		if !errors.Is(err, llmclient.ErrPromptCacheKeepAliveUnsupported) {
			cli.logger.Debug("prompt cache keep-alive failed; stopping for this idle stretch", zap.Error(err))
		}
		cli.cancelPromptCacheKeepAlive()
		return false
	}
	ka.mu.Lock()
	ka.refreshing = true
	ka.mu.Unlock()
	if cli.costTracker != nil && usage != nil {
		cli.costTracker.RecordRealUsage(provider, model, usage)
	}
	ka.mu.Lock()
	ka.refreshing = false
	ka.mu.Unlock()
	cli.logger.Debug("prompt cache kept warm",
		zap.String("provider", provider), zap.String("model", model),
		zap.Int("refresh", refreshes+1))
	cli.armPromptCacheKeepAlive(provider, model, refreshes+1)
	return true
}
