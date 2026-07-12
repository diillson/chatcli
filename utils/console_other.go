//go:build !windows

/*
 * ChatCLI - Console bootstrap (non-Windows no-op).
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

// EnableVirtualTerminal is a no-op outside Windows: Unix terminals process
// ANSI escapes natively. See console_windows.go for the Windows opt-in.
func EnableVirtualTerminal() {}
