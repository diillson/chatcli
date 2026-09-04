/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
)

func TestProjectedContextParts_SplitsUsedFromReserve(t *testing.T) {
	cli := newTenantTestCLI(t)
	cli.Provider, cli.Model = "CLAUDEAI", "claude-sonnet-5"
	window := 1000
	cli.history = []models.Message{{Role: "user", Content: strings.Repeat("x", 3000)}, {Role: "assistant", Content: strings.Repeat("y", 1000)}}
	cli.promptBreakdowns.recordDegraded("chat", nil, []promptSection{{Name: "mode", Chars: 2000}})
	used, reserve, ok := cli.projectedContextParts(window)
	if !ok {
		t.Fatal("projection must be available")
	}
	// (3000+1000+2000 chars) / 4 = 1500 tokens on a 1000 window = 150% used;
	// the reserve rides apart (max_tokens capped at 25% of the window).
	if used < 149 || used > 151 {
		t.Fatalf("used = %.1f, want ≈150", used)
	}
	wantReserve := float64(answerReserveTokens(cli.getMaxTokensForCurrentLLM(), window)) / float64(window) * 100
	if reserve < wantReserve-0.5 || reserve > wantReserve+0.5 {
		t.Fatalf("reserve = %.1f, want ≈%.1f", reserve, wantReserve)
	}
	if total, _ := cli.projectedContextPct(window); total < used+reserve-1 || total > used+reserve+1 {
		t.Fatalf("total = %.1f must be used + reserve", total)
	}
	line := i18n.T("chat.envelope.context_pct_reserve", roundPct(used), roundPct(reserve))
	if !strings.Contains(line, "(+") || !strings.Contains(line, "150") {
		t.Fatalf("footer line = %q", line)
	}
}
