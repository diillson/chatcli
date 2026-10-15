/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cmd

import (
	"testing"

	"github.com/diillson/chatcli/llm/tokenizer"
	"github.com/diillson/chatcli/pkg/testenv"
)

// TestMain isolates the home and latches the tokenizer's vocabulary
// directory there before any test runs. The engine prefetches the
// vocabulary in the background and the directory is fixed on first use:
// without this, it was the first engine test's t.TempDir, and the download
// finishing after that test wrote into a directory being removed
// ("TempDir RemoveAll cleanup: directory not empty").
func TestMain(m *testing.M) {
	testenv.Main(m, func() { tokenizer.Prefetch("gpt-4o") })
}
