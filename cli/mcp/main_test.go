/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package mcp

import (
	"testing"

	"github.com/diillson/chatcli/pkg/testenv"
)

// TestMain keeps this package's tests hermetic. The OAuth token store
// encrypts with the credential key, which is resolved through the OS
// keychain unless the file backend is pinned.
func TestMain(m *testing.M) {
	testenv.Main(m)
}
