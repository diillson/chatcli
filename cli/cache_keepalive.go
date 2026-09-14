/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Prompt-cache keep-alive: refreshing the provider's cache entry instead
 * of paying to rebuild it.
 *
 * A cache read refreshes the entry's timer for free on every provider
 * with a prompt cache. Re-sending the previous request asking for no
 * output (or the least the wire accepts) shortly before the entry would
 * expire keeps it alive for the price of one read. Two situations make
 * that worth it, and the scheduler serves both with one timer:
 *
 *   - A run whose tool call outlives the entry. A test suite that takes
 *     six minutes expires a 5-minute entry before the next request, and
 *     the whole prefix is rewritten at the write price (or at full price
 *     on the subset caches). A refresh costs the read price of the
 *     prefix, a small fraction of that, on every model and every adapter
 *     that can refresh. The loop marks the run; a timer that fires inside
 *     one is by definition a request gap longer than the lifetime.
 *
 *   - A user idle at the prompt on a model whose reads are cheap enough
 *     to beat the hour-long entry. On Claude Fable 5.1 a read is 2.5% of
 *     the input price, so refreshing every 4.5 minutes for up to an hour
 *     beats paying 2x on every token written for the hour-long ttl. On
 *     the other Claude models a read is 10% of input and the arithmetic
 *     favors the hour, so they keep the evidence-based ttl promotion.
 *
 * The scheduler restarts from every provider-reported usage (the tracker
 * hook): the request itself refreshed the entry, so the next refresh is
 * due one lifetime later, minus a lead. Refreshes are booked like any
 * request, so /cost shows what they cost. Adapters opt in through
 * client.PromptCacheKeepAliver; one that cannot refresh reports so and
 * the schedule stands down without a second attempt.
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

// PromptCacheKeepAliveEnv selects the keep-alive: "auto" (default)
// refreshes during a run's long tool calls on every adapter that can and
// while idle on models whose cache reads cost at most keepAliveReadShare
// of the input price; "on" also refreshes while idle on every adapter
// that can; "off" never refreshes.
const PromptCacheKeepAliveEnv = "CHATCLI_PROMPT_CACHE_KEEPALIVE"

const (
	// keepAliveReadShare is the read/input price ratio at or below which
	// refreshing while idle beats the hour-long ttl (Fable 5.1 reads at
	// 0.025x).
	keepAliveReadShare = 0.05
	// keepAliveLead is how long before the entry expires a refresh goes
	// out; the lifetime is measured from the START of the refreshing
	// request, so the margin covers the request itself.
	keepAliveLead = 30 * time.Second
	// keepAliveMaxRefreshes bounds one idle stretch: twelve refreshes of a
	// 5-minute entry is close to an hour, past which the hour-long ttl
	// would have expired too. A run's tool call is bounded the same way.
	keepAliveMaxRefreshes = 12
)

// keepAliveReason is why a refresh went out, for the log line.
type keepAliveReason string

const (
	keepAliveLongOperation keepAliveReason = "long_operation"
	keepAliveIdle          keepAliveReason = "idle"
)

// promptCacheKeepAlive is the scheduler state.
type promptCacheKeepAlive struct {
	mu        sync.Mutex
	timer     *time.Timer
	gen       uint64 // bumps on every arm/cancel so a stale timer is a no-op
	refreshes int    // refreshes sent in the current stretch
	provider  string
	model     string
	// inRun is true between the start and the end of an agent/coder run:
	// a timer that fires inside a run means a tool call or a long
	// processing step is outliving the cache entry.
	inRun bool
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

// setKeepAliveRun marks the span of an agent/coder run.
func (cli *ChatCLI) setKeepAliveRun(inRun bool) {
	if cli == nil {
		return
	}
	cli.cacheKeepAlive.mu.Lock()
	cli.cacheKeepAlive.inRun = inRun
	cli.cacheKeepAlive.mu.Unlock()
}

// keepAliveIdleApplies reports whether the route is kept warm by
// refreshes while the user is idle, i.e. whether a read is cheap enough
// to beat the hour-long ttl (or the env forces it).
func (cli *ChatCLI) keepAliveIdleApplies(provider, model string) bool {
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

// keepAliveApplies is keepAliveIdleApplies under its historical name.
func (cli *ChatCLI) keepAliveApplies(provider, model string) bool {
	return cli.keepAliveIdleApplies(provider, model)
}

// keepAliveArmed reports whether the scheduler should run at all for a
// booked request: never off, never in unattended mode (one client serves
// every tenant there), and only on the short lifetime — an hour-long
// entry does not need refreshing.
func (cli *ChatCLI) keepAliveArmed() bool {
	if cli == nil || cli.unattended || keepAliveMode() == "off" {
		return false
	}
	return llmclient.AnthropicCacheTTL() != "1h"
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
	llmclient.SetPromptCacheKeepAlivePreferred(cli.keepAliveIdleApplies(provider, model))
	if !cli.keepAliveArmed() {
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

// keepAliveReasonNow decides, at fire time, whether a refresh is worth
// sending: inside a run the entry is outliving a tool call on every
// route; outside one only a cheap-read route (or the env) justifies it.
func (cli *ChatCLI) keepAliveReasonNow(provider, model string, inRun bool) (keepAliveReason, bool) {
	if inRun {
		return keepAliveLongOperation, true
	}
	if cli.keepAliveIdleApplies(provider, model) {
		return keepAliveIdle, true
	}
	return "", false
}

// firePromptCacheKeepAlive sends one refresh when the schedule that armed
// it is still current and a reason holds, books its usage, and re-arms
// until the stretch has run its course. Returns whether a refresh was
// sent.
func (cli *ChatCLI) firePromptCacheKeepAlive(gen uint64) bool {
	ka := &cli.cacheKeepAlive
	ka.mu.Lock()
	if gen != ka.gen {
		ka.mu.Unlock()
		return false
	}
	provider, model, refreshes, inRun := ka.provider, ka.model, ka.refreshes, ka.inRun
	ka.mu.Unlock()
	reason, ok := cli.keepAliveReasonNow(provider, model, inRun)
	if !ok {
		cli.cancelPromptCacheKeepAlive()
		return false
	}
	if refreshes >= keepAliveMaxRefreshes {
		cli.logger.Debug("prompt cache keep-alive: stretch over, letting the entry expire",
			zap.Int("refreshes", refreshes), zap.String("reason", string(reason)))
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
			cli.logger.Debug("prompt cache keep-alive failed; stopping for this stretch", zap.Error(err))
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
		zap.String("reason", string(reason)), zap.Int("refresh", refreshes+1))
	cli.armPromptCacheKeepAlive(provider, model, refreshes+1)
	return true
}
