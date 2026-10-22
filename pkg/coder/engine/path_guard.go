/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package engine

import "io"

// ValidateReadPath applies the engine's read boundary (sensitive system
// paths, the workspace rooted at the current directory, the registered
// aux directories such as the tool-result overflow dir) to a path a
// builtin tool is about to read. Builtins outside the engine share one
// guard instead of each growing its own.
func ValidateReadPath(target string) error {
	return NewEngine(io.Discard, io.Discard, "").validatePath(target)
}

// ValidateWritePath is ValidateReadPath for a path about to be written:
// the system binary directories are refused as well.
func ValidateWritePath(target string) error {
	return NewEngine(io.Discard, io.Discard, "").validateWritePath(target)
}
