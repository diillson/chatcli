/*
 * ChatCLI - Coder turn-UI mini readline
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"context"
	"fmt"
	"io"
)

// InputEvent is what the mini-readline emits to the input loop's
// consumer (the agent's stdin queue). Replacing the channel<-string
// pattern with a richer event type lets the consumer distinguish a
// plain submission from a Ctrl+C cancel or an EOF — each of which
// has different semantics for the agent loop (continue / abort the
// current turn / shut down).
type InputEvent struct {
	Kind InputEventKind
	Line string
}

// InputEventKind enumerates the events the input loop can produce.
// Keep the set small and exhaustively documented; downstream code
// in the agent loop will need to switch on every variant.
type InputEventKind int

const (
	// InputSubmit fires when the user presses Enter on a non-empty
	// buffer. The Line field carries the trimmed text.
	InputSubmit InputEventKind = iota

	// InputCancel fires on Ctrl+C. The agent loop translates this
	// to a request to abort the current LLM turn (the same effect
	// as the existing Ctrl+C in chat mode), but without killing
	// the whole session.
	InputCancel

	// InputEOF fires on Ctrl+D with an empty buffer (matches the
	// shell convention). The agent loop treats it as "user is done
	// for this session" and returns from /coder.
	InputEOF
)

// inputPainter is the abstraction the loop uses to repaint the input
// row after every buffer change. The TurnUI implements it; tests
// supply a mock that records the redraws so we can assert on the
// echo behavior without a real terminal.
type inputPainter interface {
	// paintInput moves the cursor to the input row, clears it,
	// writes the prompt and the buffer's current contents. Called
	// after every state change in the line buffer.
	paintInput(buf *LineBuffer) error
}

// ReadLineConfig bundles the dependencies the input loop needs.
// Pulling them into a struct makes the loop self-contained and lets
// tests inject mock readers, writers, and painters without touching
// globals.
type ReadLineConfig struct {
	// Reader is the source of raw bytes. In production this is the
	// raw-mode stdin; in tests, any io.Reader works.
	Reader io.Reader

	// Painter repaints the input row on buffer changes. Required.
	Painter inputPainter

	// OnSubmit is invoked when the user presses Enter with a non-
	// empty buffer. The line is delivered already trimmed.
	OnSubmit func(line string)

	// OnCancel is invoked on Ctrl+C. The loop continues; it is up
	// to the agent to translate cancel into "abort the LLM turn".
	OnCancel func()
}

// RunReadLine drives the read-decode-paint loop until ctx is canceled
// or the reader returns EOF. It blocks on the calling goroutine — the
// agent layer is expected to run it in its own goroutine alongside
// the turn execution.
//
// The loop is structured as: read into a 256-byte buffer, drain it
// rune by rune via DecodeOne, act on each Key, repaint when the
// buffer changes. The 256-byte read size is a balance — large enough
// to capture pasted text in one read on most terminals, small enough
// to keep latency low for single-key responsiveness.
//
// Errors from the reader are surfaced via the returned error so the
// caller can decide between "EOF means clean shutdown" and "I/O error
// means restore terminal then bubble up". An EOF inside a non-empty
// buffer is preserved (the buffer is not auto-submitted) to match
// readline semantics — the user pressed Ctrl+D to bail, they did NOT
// silently confirm whatever was in the field.
func RunReadLine(ctx context.Context, cfg ReadLineConfig) error {
	if cfg.Reader == nil || cfg.Painter == nil {
		return fmt.Errorf("turnui: ReadLineConfig requires Reader and Painter")
	}

	buf := NewLineBuffer()
	if err := cfg.Painter.paintInput(buf); err != nil {
		return fmt.Errorf("turnui: initial input paint: %w", err)
	}

	// readBuf accumulates raw bytes; carry holds the unprocessed
	// tail between reads so a multi-byte rune split across two
	// reads is not lost. The tail is rare in practice — most TTYs
	// deliver complete characters per read — but the cost of
	// supporting it is two lines and a copy.
	readBuf := make([]byte, 256)
	var carry []byte

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		n, err := cfg.Reader.Read(readBuf)
		if n == 0 && err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		stream := append(carry, readBuf[:n]...)
		carry = carry[:0]
		for len(stream) > 0 {
			key, consumed := DecodeOne(stream)
			if consumed == 0 {
				// Incomplete sequence — stash the remainder for
				// the next read instead of looping forever.
				carry = append(carry, stream...)
				break
			}
			stream = stream[consumed:]

			submit, exitLoop, repaint := applyKey(key, buf)
			if repaint {
				if err := cfg.Painter.paintInput(buf); err != nil {
					return fmt.Errorf("turnui: paint after key: %w", err)
				}
			}
			if submit && cfg.OnSubmit != nil {
				line := buf.Trim()
				buf.Reset()
				if err := cfg.Painter.paintInput(buf); err != nil {
					return fmt.Errorf("turnui: paint after submit: %w", err)
				}
				if line != "" {
					cfg.OnSubmit(line)
				}
			}
			if exitLoop == exitCancel {
				if cfg.OnCancel != nil {
					cfg.OnCancel()
				}
				buf.Reset()
				if err := cfg.Painter.paintInput(buf); err != nil {
					return fmt.Errorf("turnui: paint after cancel: %w", err)
				}
			}
			if exitLoop == exitEOF {
				return nil
			}
		}
	}
}

// exitKind is the internal verdict applyKey hands back to the loop.
// Distinguishing cancel from EOF up front keeps the switch in the
// loop body tidy and the test surface narrow.
type exitKind int

const (
	exitNone exitKind = iota
	exitCancel
	exitEOF
)

// applyKey mutates the buffer based on one Key event and reports
// what the loop should do next. Pure function over (Key, buffer); no
// IO, no terminal writes — those happen in the loop. Pulled out so
// the per-key behavior table has its own focused unit tests.
func applyKey(key Key, buf *LineBuffer) (submit bool, exit exitKind, repaint bool) {
	switch key.Kind {
	case KeyChar:
		buf.Append(key.Rune)
		return false, exitNone, true
	case KeyBackspace:
		return false, exitNone, buf.Backspace()
	case KeyEnter:
		return true, exitNone, false
	case KeyCtrlC:
		return false, exitCancel, false
	case KeyCtrlD:
		// EOT submits or exits depending on whether the buffer is
		// empty — same convention as bash and readline. A non-empty
		// buffer keeps the line and lets the user keep typing.
		if buf.VisibleWidth() == 0 {
			return false, exitEOF, false
		}
		return false, exitNone, false
	case KeyCtrlU:
		return false, exitNone, buf.KillLine()
	case KeyCtrlW:
		return false, exitNone, buf.KillWord()
	default:
		return false, exitNone, false
	}
}
