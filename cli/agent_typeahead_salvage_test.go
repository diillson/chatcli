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

	"github.com/diillson/chatcli/cli/coder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// A suspend (final-answer prompt, @ask overlay) tears the reader down. Lines
// the user already submitted used to vanish with the channel; they must
// survive into the next type-ahead drain, ahead of anything typed later.
func TestTeardownStdinReader_SalvagesQueuedLines(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{}}
	a.stdinLines = make(chan string, 4)
	a.stdinDone = make(chan struct{})
	a.stdinLines <- "also update the changelog"
	a.stdinLines <- "and run the tests"

	a.stdinMu.Lock()
	a.teardownStdinReaderLocked()
	a.stdinMu.Unlock()

	require.Nil(t, a.stdinLines)
	assert.Equal(t, 2, a.salvagedTypeaheadCount())

	a.stdinLines = make(chan string, 4)
	a.stdinLines <- "typed after the resume"
	assert.Equal(t, "also update the changelog\nand run the tests\ntyped after the resume",
		a.drainStdinToQueue())
	assert.Zero(t, a.salvagedTypeaheadCount(), "the drain consumes the rescue buffer")
}

// The half-typed line (no Enter yet) is carried across a teardown.
func TestTeardownStdinReader_CarriesPartialLine(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{}}
	a.stdinLines = make(chan string, 1)
	a.stdinDone = make(chan struct{})
	a.setTypeaheadPreview("half typed instr")

	a.stdinMu.Lock()
	a.teardownStdinReaderLocked()
	a.stdinMu.Unlock()

	assert.Equal(t, "half typed instr", a.takeStdinCarry())
	assert.Empty(t, a.takeStdinCarry(), "taking the carry clears it")
}

// The security prompt's input guard drains the channel so typed-ahead text
// can never answer the prompt. Full instructions caught by that drain are
// re-queued; answer-shaped lines stay discarded.
func TestSecurityPromptGuard_SalvagesInstructionsNotAnswers(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{}}
	coder.SetSecurityPromptSalvage(a.salvageGuardDrained)
	t.Cleanup(func() { coder.SetSecurityPromptSalvage(nil) })

	ch := make(chan string, 4)
	ch <- "y"
	ch <- "stop touching the migrations folder"
	ch <- "Sim"

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the prompt itself returns immediately; only the guard matters
	done := make(chan struct{})
	go func() {
		defer close(done)
		coder.SetSecurityPromptLogger(zap.NewNop())
		_ = coder.PromptSecurityCheckGuarded(ctx, "@coder", `{"cmd":"exec"}`, ch)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("guarded prompt did not return on a cancelled context")
	}

	a.stdinLines = make(chan string, 1)
	assert.Equal(t, "stop touching the migrations folder", a.drainStdinToQueue())
}

func TestSalvageTypeahead_IsBounded(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{}}
	for i := 0; i < salvagedTypeaheadCap*2; i++ {
		a.salvageTypeahead([]string{"line"})
	}
	assert.Equal(t, salvagedTypeaheadCap, a.salvagedTypeaheadCount())
}

// The spinner's "(N queued)" indicator must count rescued lines too, or the
// queue visibly shrinks to zero while the instruction is still pending.
func TestBuildTurnSpinnerFrame_CountsSalvagedLines(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{}}
	a.salvageTypeahead([]string{"pending instruction"})
	frame, _ := a.buildTurnSpinnerFrame(time.Second, "model", false)
	assert.Contains(t, frame, "1")
}

func TestLabelTypeaheadInstruction(t *testing.T) {
	got := labelTypeaheadInstruction("use tabs")
	assert.True(t, strings.HasPrefix(got, typeaheadInstructionHeader))
	assert.True(t, strings.HasSuffix(got, "\nuse tabs"))
}

// Leftovers at the end of the outermost scope are dropped from the buffer
// (after being shown) so they never leak into a later, unrelated run.
func TestReportUnconsumedTypeahead_ClearsBuffer(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{}}
	a.salvageTypeahead([]string{"never delivered"})
	a.reportUnconsumedTypeahead()
	assert.Zero(t, a.salvagedTypeaheadCount())
}
