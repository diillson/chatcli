/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestContextEstimate_CountsAllFourCategories(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop(), Provider: "CLAUDEAI", Model: "claude-sonnet-5"}
	c.history = []models.Message{
		{Role: "system", Content: strings.Repeat("s", 4000)},
		{Role: "user", Content: strings.Repeat("u", 2000)},
		{Role: "assistant", Content: strings.Repeat("a", 2000)},
	}
	c.toolDefsChars = 8000
	c.UserMaxTokens = 1000
	e := c.contextEstimate()
	if e.PrefixChars != 4000 || e.HistoryChars != 4000 || e.ToolDefChars != 8000 || e.ReserveTokens <= 0 {
		t.Fatalf("estimate = %+v", e)
	}
	if e.TotalTokens() != e.PrefixTokens()+e.HistoryTokens()+e.ToolDefTokens()+e.ReserveTokens {
		t.Fatal("total must sum the four categories")
	}
	if pct, ok := e.Pct(); !ok || pct <= 0 {
		t.Fatalf("pct = %v %v", pct, ok)
	}
	// The compactor measures the history slice (system included), so the
	// reserve is tool definitions + answer only; in chat the prefix lives
	// outside the slice and is reserved too.
	if inHist := e.ReservedChars(); inHist != 8000 {
		t.Fatalf("reserved (prefix in history) = %d", inHist)
	}
	if answerReserveTokens(128000, 200000) != 50000 || answerReserveTokens(1000, 200000) != 1000 || answerReserveTokens(0, 200000) != 0 {
		t.Fatal("answer reserve is max_tokens capped at the window share")
	}
	c.history = c.history[1:]
	c.promptBreakdowns.record("chat", []promptSection{{Name: "core", Chars: 3000}})
	e2 := c.contextEstimate()
	if e2.PrefixChars != 3000 || e2.ReservedChars() != 3000+8000 {
		t.Fatalf("chat reserve must include the prefix: %+v → %d", e2, e2.ReservedChars())
	}
	// The footer and /context status read the same numbers.
	if pct, ok := c.projectedContextPct(e2.Window); !ok || pct <= 0 {
		t.Fatalf("footer pct = %v %v", pct, ok)
	}
}

func TestRetrievedBudgetFor_ScalesWithTheWindow(t *testing.T) {
	if got := retrievedBudgetFor(1_000_000, 4); got != ctxmgr.DefaultRetrievedBudgetChars {
		t.Fatalf("large window caps at the default: %d", got)
	}
	if got := retrievedBudgetFor(32_000, 4); got != int(32_000*4*retrievedShare) {
		t.Fatalf("32K window scales: %d", got)
	}
	if got := retrievedBudgetFor(4_000, 4); got != retrievedFloorChars {
		t.Fatalf("tiny window keeps the floor: %d", got)
	}
	if retrievedBudgetFor(0, 4) != 0 {
		t.Fatal("no window → no override")
	}
}
