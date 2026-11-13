/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"sync"
	"testing"

	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/i18n"
)

// A call queues behind another worker's prompt; meanwhile the turn's reader
// is suspended and resumed (an overlay opened and closed). The reader that
// was live when the call arrived is gone, but a reader IS live when its
// turn to ask comes: it must ask that one, not deny as unattended.
func TestQueuedPromptAsksTheReaderLiveWhenItsTurnComes(t *testing.T) {
	oldLines, oldGone := make(chan string, 1), make(chan struct{})
	newLines, newGone := make(chan string, 2), make(chan struct{})
	newLines <- "typed before the prompt"
	coder.SetSecurityPromptSalvage(func([]string) { newLines <- "y" })
	defer coder.SetSecurityPromptSalvage(nil)

	var calls int
	var mu sync.Mutex
	verdict := detachedVerdictOf(t, func(pa *workerPolicyAdapter) {
		pa.mu.Lock() // another worker's prompt is on screen
		pa.liveStdin = func() (<-chan string, <-chan struct{}) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls > 1 {
				return newLines, newGone
			}
			// The call has seen the first reader and is about to queue:
			// suspend that reader, resume a new one, then free the gate.
			go func() {
				close(oldGone)
				pa.mu.Unlock()
			}()
			return oldLines, oldGone
		}
	})
	if verdict != "true " {
		t.Fatalf("verdict %q: a prompt with a live reader must be asked and its answer taken", verdict)
	}
}

// A line typed for a permission prompt that was withdrawn (its reader was
// torn down under it) is not a follow-up instruction: it must not reach the
// model as type-ahead, and it must not vanish — it comes back as the draft
// on the input line, with a notice.
func TestLineForAWithdrawnPromptIsParkedNotSent(t *testing.T) {
	a := &AgentMode{}
	a.stdinLines, a.stdinDone = make(chan string, 2), make(chan struct{})
	closed := a.noteDetachedPrompt()
	a.stdinLines <- "y"

	out := captureStdout(t, func() {
		a.stdinMu.Lock()
		a.teardownStdinReaderLocked()
		a.stdinMu.Unlock()
	})
	closed()
	if got := a.takeSalvagedTypeahead(); len(got) != 0 {
		t.Fatalf("the answer to a withdrawn prompt was queued for the model: %q", got)
	}
	if a.stdinCarry != "y" {
		t.Fatalf("the line must come back as the draft, carry is %q", a.stdinCarry)
	}
	if !strings.Contains(out, i18n.T("leftovers.prompt.withdrawn")) || !strings.Contains(out, "❯ y") {
		t.Fatalf("the user must be told the prompt was withdrawn and what was not sent: %q", out)
	}

	// With no prompt open, lines left at a teardown are type-ahead as before.
	a.stdinLines, a.stdinDone = make(chan string, 2), make(chan struct{})
	a.stdinLines <- "also run the linter"
	a.stdinMu.Lock()
	a.teardownStdinReaderLocked()
	a.stdinMu.Unlock()
	if got := a.takeSalvagedTypeahead(); len(got) != 1 || got[0] != "also run the linter" {
		t.Fatalf("type-ahead must still be salvaged: %q", got)
	}
}
