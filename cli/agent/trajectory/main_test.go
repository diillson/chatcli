/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package trajectory

import (
	"testing"

	"github.com/diillson/chatcli/pkg/testenv"
)

// TestMain keeps this package's tests hermetic. Trajectory files and cost snapshots resolve ~/.chatcli on first use.
func TestMain(m *testing.M) {
	testenv.Main(m)
}
