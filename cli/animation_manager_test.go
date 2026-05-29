/*
 * ChatCLI - tests for the thinking animation gating
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/ui/theme"
	"github.com/stretchr/testify/assert"
)

// TestAnimation_NoSpinnerOffTerminal verifies the production behavior that the
// thinking spinner does not start when stdout is not a terminal (pipe/CI):
// the message is recorded but no goroutine repaints into the stream.
func TestAnimation_NoSpinnerOffTerminal(t *testing.T) {
	t.Cleanup(func() { theme.SetProfile(theme.DetectProfile()) })
	theme.SetProfile(theme.ProfileNoTTY)

	am := NewAnimationManager()
	am.ShowThinkingAnimation("pensando")

	am.mu.Lock()
	running := am.isRunning
	msg := am.currentMessage
	am.mu.Unlock()

	assert.False(t, running, "no spinner goroutine off-terminal")
	assert.Equal(t, "pensando", msg, "message is still recorded for non-animated surfaces")

	// Stop must be a safe no-op when nothing started.
	am.StopThinkingAnimation()
}

// TestAnimation_SuppressedDoesNotRun confirms explicit suppression (used by
// the unattended gateway) also prevents the goroutine even on a terminal.
func TestAnimation_SuppressedDoesNotRun(t *testing.T) {
	t.Cleanup(func() { theme.SetProfile(theme.DetectProfile()) })
	theme.SetProfile(theme.ProfileTrueColor)

	am := NewAnimationManager()
	am.SetSuppressed(true)
	am.ShowThinkingAnimation("trabalhando")

	am.mu.Lock()
	running := am.isRunning
	am.mu.Unlock()
	assert.False(t, running, "suppressed manager must not start the spinner")
}
