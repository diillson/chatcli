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
	"strconv"

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
		base := map[string]string{"provider": rec.Provider, "model": rec.Model}
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
	out = append(out, telemetry.Metric{Name: "chatcli.session.cost", Unit: "USD", Points: []telemetry.Point{{Value: snap.TotalCostUSD, Attrs: map[string]string{"session": snap.SessionID}}}})
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
