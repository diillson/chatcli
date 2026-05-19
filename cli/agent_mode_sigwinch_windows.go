//go:build windows

/*
 * ChatCLI - Agent-mode resize handler (Windows stub)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import "context"

// installResizeHandler is a no-op on Windows. SIGWINCH does not exist;
// the Windows console emits a different resize event mechanism that
// turnui does not currently subscribe to. The user can resize without
// crashing — the split layout will look slightly off until the next
// status tick redraws — but we do not actively re-layout. A follow-up
// could poll GetConsoleScreenBufferInfo from the readline goroutine
// to detect resize, but that is out of scope for the initial split-
// UI port.
func (a *AgentMode) installResizeHandler(_ context.Context) func() {
	return func() {}
}
