//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

// enableStdinCbreak is a no-op on Windows: the console keeps its cooked
// line-input behavior, so mid-run typing works exactly as before (kernel
// buffered, delivered on Enter). Live type-ahead preview stays a Unix
// feature until console-mode handling is validated on-device — terminal
// rendering changes on Windows have burned us before (see the coder
// stdin/fspath post-mortem).
func enableStdinCbreak() func() {
	return func() {}
}
