//go:build !windows

/*
 * ChatCLI - Coder turn-UI Unix fallback shim
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

// windowsAnsiAvailableImpl is a no-op on Unix: every TTY on Linux,
// macOS, *BSD honors the VT100 sequences the split UI relies on, so
// the Windows-specific probe is replaced with an always-true stub.
// The build tag keeps the real probe out of non-Windows binaries.
func windowsAnsiAvailableImpl() bool { return true }
