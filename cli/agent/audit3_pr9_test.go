/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package agent

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestLevel1Recovery_DoesNotTouchThePackageBudgets(t *testing.T) {
	turn, per := DefaultTurnBudgetChars, DefaultPerResultMaxChars
	history := []models.Message{
		{Role: "assistant", Content: "run", ToolCalls: []models.ToolCall{{ID: "c1", Name: "read"}}},
		{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("x", per+100)},
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cr := NewContextRecovery(DefaultContextRecoveryConfig(), zap.NewNop())
			out, ok := cr.RecoverContextOverflow(append([]models.Message(nil), history...))
			if !ok || len(out) == 0 {
				t.Error("recovery must run")
			}
			// Another session reading the defaults concurrently must see
			// them unchanged (the race detector flags a mutation).
			if DefaultTurnBudgetChars != turn || DefaultPerResultMaxChars != per {
				t.Error("package budgets mutated by a session's recovery")
			}
		}()
	}
	wg.Wait()
	if DefaultTurnBudgetChars != turn || DefaultPerResultMaxChars != per {
		t.Fatal("package budgets changed")
	}
	// Explicit budgets tighten the result without the globals.
	out, rep := EnforceToolResultBudgetWith(history, 1000, 500, zap.NewNop())
	if rep == nil || len(out[1].Content) >= per {
		t.Fatalf("explicit per-result budget must apply: %d chars", len(out[1].Content))
	}
	if _, rep := EnforceToolResultBudgetWith(history, 0, 0, zap.NewNop()); rep == nil {
		t.Fatal("zero budgets fall back to the defaults")
	}
}

func TestIsContextTooLongError_DelegatesToTheSharedClassifier(t *testing.T) {
	if !IsContextTooLongError(errors.New("input token count (99) exceeds the maximum number of tokens allowed")) {
		t.Fatal("gemini phrasing must classify")
	}
	if IsContextTooLongError(errors.New("rate limit exceeded")) {
		t.Fatal("rate limit is not overflow")
	}
}
