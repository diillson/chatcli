//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

// stdinReadCanceler is a no-op outside Windows: stdinPollReady uses poll(2),
// which has no false positives, so the reader goroutine never sits in a
// blocking os.Stdin.Read — it exits within one poll cycle of stdinDone
// closing. See stdin_cancel_windows.go for the Windows implementation.
type stdinReadCanceler struct{}

func newStdinReadCanceler() *stdinReadCanceler { return &stdinReadCanceler{} }

func (c *stdinReadCanceler) bind()   {}
func (c *stdinReadCanceler) unbind() {}

// cancel reports false: there is no cancellable read on this platform, so
// the caller falls back to plainly waiting out the poll cycle.
func (c *stdinReadCanceler) cancel() bool { return false }
