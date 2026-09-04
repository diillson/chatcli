/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestTelemetry_SemconvAttrsAndOptInSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := &ChatCLI{logger: zap.NewNop(), costTracker: NewCostTracker()}
	t.Cleanup(c.costTracker.FlushDailySpend)
	c.costTracker.RecordUsage("OPENAI", "gpt-5.6", 1000, 100)
	c.costTracker.RecordEmbeddingUsage("openai", 4000)
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.namespace=x")
	metrics := c.telemetryMetrics()
	var sawSemconv, sawSession, sawEmbedding, sawDaily bool
	for _, m := range metrics {
		for _, p := range m.Points {
			if p.Attrs["gen_ai.provider.name"] == "OPENAI" && p.Attrs["gen_ai.request.model"] == "gpt-5.6" {
				sawSemconv = true
			}
			if _, ok := p.Attrs["chatcli.session.id"]; ok {
				sawSession = true
			}
		}
		if strings.HasPrefix(m.Name, "chatcli.embedding.") {
			sawEmbedding = true
		}
		if m.Name == "chatcli.budget.daily_spent" {
			sawDaily = true
		}
	}
	if !sawSemconv || sawSession || !sawEmbedding || !sawDaily {
		t.Fatalf("semconv=%v session=%v embedding=%v daily=%v", sawSemconv, sawSession, sawEmbedding, sawDaily)
	}
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.namespace=x, chatcli.session=attr")
	for _, m := range c.telemetryMetrics() {
		if m.Name == "chatcli.session.cost" && m.Points[0].Attrs["chatcli.session.id"] == "" {
			t.Fatal("opt-in must attach the session id")
		}
	}
	// Embedding spend accrues to the daily budget too.
	if spent, _ := c.costTracker.DailySpend(); spent <= 0 {
		t.Fatalf("embedding spend must count toward the day: %v", spent)
	}
}

func TestCostExport_CSV(t *testing.T) {
	ct := NewCostTracker()
	ct.RecordUsage("CLAUDEAI", "claude-sonnet-5", 1000, 200)
	out := string(costSnapshotCSV(ct.Snapshot()))
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "session_id,provider,model,requests") {
		t.Fatalf("csv = %q", out)
	}
	if !strings.Contains(lines[1], "CLAUDEAI,claude-sonnet-5,1,1000,200") || !strings.Contains(lines[2], ",total,") {
		t.Fatalf("rows = %q", lines[1:])
	}
}

func TestFinalizeSpend_NilSafeAndPersists(t *testing.T) {
	(*ChatCLI)(nil).settleSpendOnExit(context.Background())
	(&ChatCLI{}).settleSpendOnExit(context.Background())
	t.Setenv("HOME", t.TempDir())
	ct := NewCostTracker()
	ct.RecordUsage("OPENAI", "gpt-5.6", 10, 1)
	c := &ChatCLI{logger: zap.NewNop(), costTracker: ct}
	c.settleSpendOnExit(context.Background())
	if _, err := LoadCostSnapshot(ct.Snapshot().SessionID); err != nil {
		t.Fatalf("snapshot must be persisted on exit: %v", err)
	}
	if m := nextLocalMidnight(); !m.After(time.Now()) || m.Hour() != 0 || m.Minute() != 0 {
		t.Fatalf("next midnight = %v", m)
	}
}
