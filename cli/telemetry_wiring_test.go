/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestTelemetryMetrics_RenderTheCostTracker(t *testing.T) {
	cli := &ChatCLI{costTracker: NewCostTracker()}
	cli.costTracker.RecordRealUsage("openai", "gpt-5.6-terra", &models.UsageInfo{PromptTokens: 1000, CompletionTokens: 100, CacheReadInputTokens: 200, TotalTokens: 1100, IsReal: true})
	cli.costTracker.RecordCompaction(CompactReport{Level: 3})
	names := map[string]int{}
	for _, m := range cli.telemetryMetrics() {
		names[m.Name] = len(m.Points)
	}
	if names["chatcli.llm.tokens"] != 3 || names["chatcli.llm.cost"] != 1 || names["chatcli.context.compactions"] != 2 || names["chatcli.session.cost"] != 1 {
		t.Fatalf("metrics = %v", names)
	}
	if (&ChatCLI{}).telemetryMetrics() != nil {
		t.Fatal("no tracker → nothing")
	}
	// No endpoint: init is a no-op; surface/tenant/shutdown never panic.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "")
	cli.initTelemetry(context.Background(), "repl")
	if cli.otlp != nil {
		t.Fatal("exporter must stay off without an endpoint")
	}
	cli.SetAuditSurface("gateway")
	cli.telemetryTenant("acme")
	cli.shutdownTelemetry(context.Background())
}
