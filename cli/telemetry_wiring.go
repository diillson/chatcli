/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Wires the OTLP metrics exporter to the session's cost tracker: the
 * cumulative token, cache, compaction, embedding and cost counters every
 * surface already keeps become OpenTelemetry sums, labeled by surface and
 * tenant. Configured purely through the OTEL_* environment.
 */
package cli

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/cli/telemetry"
	"go.uber.org/zap"
)

// initTelemetry starts the OTLP exporter when an endpoint is configured.
func (cli *ChatCLI) initTelemetry(ctx context.Context, surface string) {
	if cli == nil || !telemetry.Enabled() {
		return
	}
	exp := telemetry.NewFromEnv(cli.telemetryMetrics, map[string]string{"chatcli.surface": surface})
	if exp == nil {
		return
	}
	if cli.logger != nil {
		logger := cli.logger
		exp.SetLogger(func(format string, args ...interface{}) { logger.Sugar().Debugf(format, args...) })
	}
	cli.otlp = exp
	// The loop outlives any request: bound by process shutdown, not by
	// the constructor's context.
	exp.Start(context.WithoutCancel(ctx))
	if cli.logger != nil {
		endpoint, _, _ := exp.Status()
		cli.logger.Info("OpenTelemetry metrics export enabled", zap.String("endpoint", endpoint))
	}
}

// telemetrySessionAttrOptIn reports whether OTEL_RESOURCE_ATTRIBUTES carries
// chatcli.session=attr — the operator's explicit acceptance of one series
// per session on the session cost metric.
func telemetrySessionAttrOptIn() bool {
	for _, kv := range strings.Split(os.Getenv("OTEL_RESOURCE_ATTRIBUTES"), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if ok && strings.TrimSpace(k) == "chatcli.session" && strings.EqualFold(strings.TrimSpace(v), "attr") {
			return true
		}
	}
	return false
}

// telemetryMetrics renders the cost tracker as cumulative OTLP sums.
func (cli *ChatCLI) telemetryMetrics() []telemetry.Metric {
	ct := cli.costTracker
	if ct == nil {
		return nil
	}
	snap := ct.Snapshot()
	tokens := telemetry.Metric{Name: "chatcli.llm.tokens", Unit: "{token}"}
	cost := telemetry.Metric{Name: "chatcli.llm.cost", Unit: "USD"}
	for _, rec := range snap.ModelUsage {
		if rec == nil {
			continue
		}
		// OpenTelemetry GenAI semantic-convention attribute names.
		base := map[string]string{"gen_ai.provider.name": rec.Provider, "gen_ai.request.model": rec.Model}
		for kind, n := range map[string]int64{
			"input": rec.PromptTokens, "output": rec.CompletionTokens,
			"cache_read": rec.CacheReadTokens, "cache_write": rec.CacheCreationTokens, "reasoning": rec.ReasoningTokens,
		} {
			if n <= 0 {
				continue
			}
			attrs := map[string]string{"kind": kind}
			for k, v := range base {
				attrs[k] = v
			}
			tokens.Points = append(tokens.Points, telemetry.Point{Value: float64(n), Attrs: attrs})
		}
		cost.Points = append(cost.Points, telemetry.Point{Value: rec.TotalCostUSD, Attrs: base})
	}
	out := []telemetry.Metric{tokens, cost}
	if snap.Compactions > 0 {
		out = append(out, telemetry.Metric{Name: "chatcli.context.compactions", Unit: "{compaction}", Points: []telemetry.Point{
			{Value: float64(snap.Compactions - snap.CompactionsLevel3), Attrs: map[string]string{"level": "summary"}},
			{Value: float64(snap.CompactionsLevel3), Attrs: map[string]string{"level": "truncation"}},
		}})
		out = append(out, telemetry.Metric{Name: "chatcli.context.compaction_cost", Unit: "USD", Points: []telemetry.Point{{Value: snap.CompactionCostUSD}}})
	}
	if stats := ct.CacheStats(); stats.Requests > 0 {
		out = append(out, telemetry.Metric{Name: "chatcli.cache.requests", Unit: "{request}", Points: []telemetry.Point{
			{Value: float64(stats.Requests), Attrs: map[string]string{"outcome": "total"}},
			{Value: float64(stats.Misses), Attrs: map[string]string{"outcome": "miss"}},
			{Value: float64(stats.Rebuilds), Attrs: map[string]string{"outcome": "expected_rebuild"}},
		}})
	}
	if snap.CacheResources > 0 {
		out = append(out, telemetry.Metric{Name: "chatcli.cache.storage_cost", Unit: "USD", Points: []telemetry.Point{{Value: snap.CacheStorageCostUSD}}})
	}
	// The session id is unbounded cardinality: attached only when the
	// operator opted in through OTEL_RESOURCE_ATTRIBUTES (chatcli.session=attr).
	sessionAttrs := map[string]string(nil)
	if telemetrySessionAttrOptIn() {
		sessionAttrs = map[string]string{"chatcli.session.id": snap.SessionID}
	}
	out = append(out, telemetry.Metric{Name: "chatcli.session.cost", Unit: "USD", Points: []telemetry.Point{{Value: snap.TotalCostUSD, Attrs: sessionAttrs}}})
	if calls, tokens, cost := ct.EmbeddingStats(); calls > 0 {
		out = append(out, telemetry.Metric{Name: "chatcli.embedding.tokens", Unit: "{token}", Points: []telemetry.Point{{Value: float64(tokens)}}})
		out = append(out, telemetry.Metric{Name: "chatcli.embedding.cost", Unit: "USD", Points: []telemetry.Point{{Value: cost}}})
	}
	if spent, limit := ct.DailySpend(); limit > 0 || spent > 0 {
		out = append(out, telemetry.Metric{Name: "chatcli.budget.daily_spent", Unit: "USD", Points: []telemetry.Point{{Value: spent}}})
		if limit > 0 {
			out = append(out, telemetry.Metric{Name: "chatcli.budget.daily_limit", Unit: "USD", Points: []telemetry.Point{{Value: limit}}})
		}
	}
	return out
}

// telemetrySurface relabels the exporter's surface resource attribute.
func (cli *ChatCLI) telemetrySurface(surface string) {
	if cli == nil || cli.otlp == nil {
		return
	}
	cli.otlp.SetResource("chatcli.surface", surface)
}

// telemetryTenant relabels the exporter's tenant resource attribute.
func (cli *ChatCLI) telemetryTenant(principal string) {
	if cli == nil || cli.otlp == nil {
		return
	}
	cli.otlp.SetResource("chatcli.tenant", principal)
}

// shutdownTelemetry pushes a final snapshot and stops the loop.
func (cli *ChatCLI) shutdownTelemetry(ctx context.Context) {
	if cli == nil || cli.otlp == nil {
		return
	}
	cli.otlp.Stop(ctx)
	cli.otlp = nil
}

// renderTelemetryStatus prints the /config rows of the exporter.
func (cli *ChatCLI) renderTelemetryStatus(p string) {
	kv(p, telemetry.EnvEndpoint, envOr(telemetry.EnvEndpoint))
	kv(p, telemetry.EnvMetricsEndpoint, envOr(telemetry.EnvMetricsEndpoint))
	kv(p, telemetry.EnvServiceName, envOr(telemetry.EnvServiceName))
	kv(p, telemetry.EnvExportInterval, envOr(telemetry.EnvExportInterval))
	if cli != nil && cli.otlp != nil {
		endpoint, pushes, lastErr := cli.otlp.Status()
		state := strconv.Itoa(pushes) + " push(es) → " + endpoint
		if lastErr != "" {
			state += " · " + lastErr
		}
		kv(p, "OTLP", state)
	}
}
