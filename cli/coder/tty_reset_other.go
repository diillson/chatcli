//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package coder

// restoreCookedModeWindows only has an implementation on Windows; the Unix
// paths reset the terminal via `stty sane` instead.
func restoreCookedModeWindows() bool { return false }
