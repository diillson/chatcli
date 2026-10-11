//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import "os"

// ownedByCurrentUser: Windows exposes no POSIX owner through os.FileInfo,
// and a per-user profile directory is already private, so a regular file is
// accepted.
func ownedByCurrentUser(info os.FileInfo) bool {
	return info.Mode().IsRegular()
}
