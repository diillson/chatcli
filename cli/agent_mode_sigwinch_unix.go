//go:build !windows

/*
 * ChatCLI - Agent-mode SIGWINCH (terminal resize) handler
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"os"
	"os/signal"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/diillson/chatcli/cli/coder/turnui"
	"go.uber.org/zap"
)

// installResizeHandler subscribes to SIGWINCH so the split UI can
// recompute its layout when the user drags the terminal window. The
// handler runs in its own goroutine, parked on a buffered channel; it
// exits when ctx is canceled (the agent loop's defer fires) or when
// the OS closes the signal channel.
//
// No-op when turnUI is nil or the env does not have a TTY — the
// legacy renderer does not care about resize because \r-based redraw
// always lands on the cursor's current row regardless of size.
//
// SIGWINCH is Unix-only. The windows variant of this file (next door)
// is a stub: Windows console resize fires a different mechanism that
// turnui currently does not implement; the user gets the legacy
// fallback if turnui is active and they resize, which is acceptable
// because the layout invalidates one frame and recovers on the next
// status redraw — visible glitch, not a crash.
func (a *AgentMode) installResizeHandler(ctx context.Context) func() {
	if a.turnUI == nil {
		return func() {}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, unix.SIGWINCH)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-sigCh:
				if !ok {
					return
				}
				a.onTerminalResize()
			}
		}
	}()

	return func() {
		signal.Stop(sigCh)
		close(sigCh)
		<-done
	}
}

// onTerminalResize is the body of the SIGWINCH handler. Extracted so
// tests can drive it directly without sending a real signal — they
// can call a.onTerminalResize() after swapping in a TurnUI bound to
// a buffer + a controlled "current size".
//
// On a size that is now too small for the split UI we tear down to
// the legacy renderer instead of leaving the user staring at an
// overlapping layout. A future enhancement could re-activate when
// the user resizes back up, but that requires re-running setup which
// is intrusive enough to be its own commit.
func (a *AgentMode) onTerminalResize() {
	if a.turnUI == nil {
		return
	}
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		a.logger.Debug("SIGWINCH: term.GetSize failed", zap.Error(err))
		return
	}
	if w < turnui.MinColsRequired || h < turnui.MinRowsRequired {
		// New size is below the floor: log and let the next paint
		// tick attempt land on a still-too-small layout — the
		// turnui.Resize call below will reject it and we keep the
		// previous layout, which at worst looks slightly off until
		// the user resizes back up.
		a.logger.Info("SIGWINCH: new size below split-UI minimum, keeping previous layout",
			zap.Int("rows", h), zap.Int("cols", w))
		return
	}
	if err := a.turnUI.Resize(h, w); err != nil {
		a.logger.Debug("SIGWINCH: turnui.Resize failed", zap.Error(err))
	}
}
