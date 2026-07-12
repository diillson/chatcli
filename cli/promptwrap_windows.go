//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"

	prompt "github.com/c-bata/go-prompt"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

// newPlatformPromptWriter returns the ConsoleWriter go-prompt should use.
//
// When the console has VT processing enabled (Windows Terminal, or conhost
// after EnableVirtualTerminal at boot) it wraps the standard writer with the
// deferred-wrap compensator — see wrapAwareWriter. On a legacy console
// without VT the immediate-wrap behavior already matches go-prompt's Windows
// math, and injecting extra newlines would break it, so the default writer is
// returned untouched.
func newPlatformPromptWriter() prompt.ConsoleWriter {
	if !stdoutHasVTProcessing() {
		return prompt.NewStandardOutputWriter()
	}
	return newWrapAwareWriter(prompt.NewStandardOutputWriter(), func() int {
		if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
			return w
		}
		return 0
	})
}

// stdoutHasVTProcessing reports whether stdout is a console with
// ENABLE_VIRTUAL_TERMINAL_PROCESSING set — i.e. xterm-style deferred EOL
// wrap semantics.
func stdoutHasVTProcessing() bool {
	h := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	return mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0
}
