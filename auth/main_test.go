/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package auth

import (
	"testing"

	"github.com/diillson/chatcli/pkg/testenv"
)

// TestMain keeps this package's tests hermetic. The credential-encryption key is read from the OS keychain unless the file backend is pinned; on macOS that read prompts once per rebuilt test binary.
func TestMain(m *testing.M) {
	testenv.Main(m)
}
