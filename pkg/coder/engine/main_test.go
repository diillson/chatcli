/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package engine

import (
	"testing"

	"github.com/diillson/chatcli/pkg/testenv"
)

// TestMain keeps this package's tests hermetic. Checkpoints init a git store per workspace under ~/.chatcli/checkpoints — one run of this package left hundreds of them in the developer's real home.
func TestMain(m *testing.M) {
	testenv.Main(m)
}
