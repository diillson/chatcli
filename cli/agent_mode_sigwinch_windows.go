//go:build windows

/*
 * ChatCLI - Agent-mode resize handler (Windows polling)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"os"
	"time"

	"go.uber.org/zap"
	"golang.org/x/term"

	"github.com/diillson/chatcli/cli/coder/turnui"
)

// windowsResizePollInterval is the cadence at which we re-query the
// console size. 500ms is a balance — too low burns CPU on a process
// that may run for hours; too high makes the split UI feel sluggish
// to recover from a resize. The user usually pauses a beat after
// dragging the terminal corner, so anything below 1s feels live.
const windowsResizePollInterval = 500 * time.Millisecond

// installResizeHandler subscribes to terminal resize on Windows by
// polling GetConsoleScreenBufferInfo (via golang.org/x/term.GetSize)
// at a fixed cadence. Windows has no SIGWINCH equivalent in user-
// mode Go without subscribing to console input events, which would
// conflict with the raw-mode stdin reader the split UI already owns.
// Polling is the conservative choice: it requires no new event-loop
// integration and gracefully no-ops when the size has not changed.
//
// The handler runs in its own goroutine, exits when ctx is canceled
// (the agent loop's defer fires), and is a no-op when turnUI is nil
// (legacy renderer doesn't care about resize because \r-based redraw
// always lands on the cursor's current row regardless of size).
func (a *AgentMode) installResizeHandler(ctx context.Context) func() {
	if a.turnUI == nil {
		return func() {}
	}

	lastRows, lastCols, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		// If we cannot even query the initial size, polling is
		// pointless — leave the handler off and let the user
		// see a slightly off layout if they resize. The split
		// UI on a non-queryable console is rare enough that
		// this is acceptable.
		a.logger.Debug("Windows resize poll: initial GetSize failed, skipping handler", zap.Error(err))
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(windowsResizePollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rows, cols, err := term.GetSize(int(os.Stdout.Fd()))
				if err != nil {
					continue
				}
				if rows == lastRows && cols == lastCols {
					continue
				}
				lastRows, lastCols = rows, cols
				a.onWindowsResize(rows, cols)
			}
		}
	}()

	return func() { <-done }
}

// onWindowsResize is the body of the resize callback, extracted so
// the polling loop stays focused on cadence and detection while the
// actual size-change handling lives in a testable function. Same
// contract as the Unix onTerminalResize counterpart: degrade
// gracefully on a size that is now below the split UI minimum.
func (a *AgentMode) onWindowsResize(rows, cols int) {
	if a.turnUI == nil {
		return
	}
	if cols < turnui.MinColsRequired || rows < turnui.MinRowsRequired {
		a.logger.Info("Windows resize: new size below split-UI minimum, keeping previous layout",
			zap.Int("rows", rows), zap.Int("cols", cols))
		return
	}
	if err := a.turnUI.Resize(rows, cols); err != nil {
		a.logger.Debug("Windows resize: turnui.Resize failed", zap.Error(err))
	}
}
