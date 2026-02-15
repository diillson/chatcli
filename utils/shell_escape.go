/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package utils

import "strings"

// ShellQuote wraps a string in POSIX-compliant single quotes, escaping any
// embedded single quotes. The result is safe to embed in a shell command
// without risk of injection.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
