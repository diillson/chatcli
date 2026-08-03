/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * agent_typeahead — live preview of the line being typed while the agent
 * loop owns the terminal.
 *
 * With the TTY in cbreak mode (see stdin_cbreak_unix.go) the centralized
 * reader receives bytes as they are typed; the residual partial line is
 * published here and the spinner / dispatch panel renders it as a `❯ …▌`
 * input line, so the user finally SEES what they are typing mid-run
 * instead of the kernel echo being eaten by the repaint.
 */
package cli

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/diillson/chatcli/cli/metrics"
	"github.com/diillson/chatcli/i18n"
)

// setTypeaheadPreview publishes the current partial input line (what has
// been typed since the last Enter). Called from the reader goroutine.
func (a *AgentMode) setTypeaheadPreview(s string) {
	a.typeaheadMu.Lock()
	a.typeaheadLine = s
	a.typeaheadMu.Unlock()
}

// typeaheadPreviewSnapshot returns the current partial input line.
func (a *AgentMode) typeaheadPreviewSnapshot() string {
	a.typeaheadMu.Lock()
	defer a.typeaheadMu.Unlock()
	return a.typeaheadLine
}

// typeaheadPreviewTailWidth bounds how much of the in-flight line the
// display shows; when longer, the TAIL is shown (the user cares about the
// characters around the cursor, not the start of a long instruction).
const typeaheadPreviewTailWidth = 70

// formatTypeaheadPreviewLine renders the input line appended under the
// dispatch panel. Empty preview renders nothing. Control bytes (escape
// sequences from arrow keys, etc.) are stripped from the DISPLAY only —
// the submitted line is untouched, same contract as before.
func formatTypeaheadPreviewLine(preview string) string {
	text := sanitizeTypeaheadPreview(preview)
	if text == "" {
		return ""
	}
	if n := utf8.RuneCountInString(text); n > typeaheadPreviewTailWidth {
		runes := []rune(text)
		text = "…" + string(runes[n-typeaheadPreviewTailWidth:])
	}
	// \033[K clears repaint leftovers when the line shrinks (backspace).
	return "  " + ColorCyan + "❯ " + text + "▌" + ColorReset + "\033[K"
}

// formatTypeaheadPreviewBelow renders the input line UNDER the single-line
// turn spinner (same placement as the dispatch panel's preview, which
// users found clearer than an inline suffix competing with the spinner
// for horizontal space).
//
// The suffix paints the line below and returns the cursor to the spinner
// line, so the spinner's plain `\r` repaint contract is preserved:
//
//	\n<preview>\033[A\r   — down, paint, back up
//
// hadPreview threads the previous tick's state: when typing stops (Enter
// or backspace-to-empty) one final `\n\033[K\033[A\r` wipes the stale
// line, and after that the dance stops entirely — a quiet spinner never
// touches the row below it (which would scroll the screen when the
// spinner sits on the terminal's bottom row).
func formatTypeaheadPreviewBelow(preview string, hadPreview bool) (string, bool) {
	if line := formatTypeaheadPreviewLine(preview); line != "" {
		return "\n" + line + "\033[A\r", true
	}
	if hadPreview {
		return "\n\033[K\033[A\r", false
	}
	return "", false
}

// sanitizeTypeaheadPreview drops control bytes AND whole escape sequences
// (arrow keys arrive as CSI like "\x1b[A" — stripping only the ESC would
// leave a stray "[A" in the preview) so stray input never corrupts the
// panel rendering. Display-only: the submitted line is untouched.
func sanitizeTypeaheadPreview(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == 0x1b {
			if i+1 < len(runes) && runes[i+1] == '[' {
				// CSI: skip parameters until the final byte (@ through ~).
				i++
				for i+1 < len(runes) {
					i++
					if runes[i] >= 0x40 && runes[i] <= 0x7e {
						break
					}
				}
			} else if i+1 < len(runes) {
				i++ // two-byte escape (ESC + one char)
			}
			continue
		}
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	return strings.TrimLeft(b.String(), " ")
}

// spinnerPreviewWipe returns the sequence that erases a type-ahead line
// still painted below the spinner when the turn ends mid-typing ("" when
// nothing was painted).
func spinnerPreviewWipe(hadPreview bool) string {
	if hadPreview {
		return "\n\033[K\033[A\r"
	}
	return ""
}

// buildTurnSpinnerFrame composes one repaint of the single-line turn
// spinner: status line (with the queued type-ahead indicator) plus the
// live input line below when the user is typing. Extracted from the
// ticker closure so the frame logic is directly testable — the ticker
// itself only runs against a real TTY.
func (a *AgentMode) buildTurnSpinnerFrame(d time.Duration, modelName string, hadPreview bool) (string, bool) {
	msg := "Processando..."
	a.cli.messageQueueMu.Lock()
	queued := len(a.cli.messageQueue)
	a.cli.messageQueueMu.Unlock()
	if a.stdinLines != nil {
		queued += len(a.stdinLines)
	}
	if queued > 0 {
		msg = "Processando... " + i18n.T("agent.queue.indicator", queued)
	}
	// Live type-ahead: what the user is typing renders on its own line
	// BELOW the spinner (same placement as the dispatch panel), cursor
	// returned to the spinner line each tick.
	suffix, had := formatTypeaheadPreviewBelow(a.typeaheadPreviewSnapshot(), hadPreview)
	return metrics.FormatTimerStatus(d, modelName, msg) + "\033[K" + suffix, had
}
