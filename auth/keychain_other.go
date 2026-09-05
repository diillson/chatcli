//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"fmt"
	"runtime"
)

// The Windows Credential Manager has no counterpart on these platforms:
// macOS and Linux reach their keychains through the security and
// secret-tool binaries, which keychain.go handles directly.

func nativeAvailable() bool { return false }

func platformGet(string) ([]byte, error) {
	return nil, fmt.Errorf("credential manager not supported on %s", runtime.GOOS)
}

func platformSet(string, []byte) error {
	return fmt.Errorf("credential manager not supported on %s", runtime.GOOS)
}

func platformDelete(string) error {
	return fmt.Errorf("credential manager not supported on %s", runtime.GOOS)
}
