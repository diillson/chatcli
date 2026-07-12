//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	prompt "github.com/c-bata/go-prompt"
)

// newPlatformPromptWriter returns the standard writer on Unix: go-prompt's
// own lineWrap already materializes deferred wraps there, so no compensation
// is needed (see promptwrap_windows.go for the Windows story).
func newPlatformPromptWriter() prompt.ConsoleWriter {
	return prompt.NewStandardOutputWriter()
}
