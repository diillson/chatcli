/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDefaultSpinnerLabel_HappyPath pins the legacy fallback shape so
// any future refactor that touches the spinner label format trips this
// test if it accidentally changes the externally-visible string.
func TestDefaultSpinnerLabel_HappyPath(t *testing.T) {
	got := defaultSpinnerLabel("@coder", []string{"read", "--file", "main.go"})
	assert.Equal(t, "EXECUTANDO: @coder read", got)
}

// TestDefaultSpinnerLabel_EmptyArgs uses the "ação" placeholder.
func TestDefaultSpinnerLabel_EmptyArgs(t *testing.T) {
	got := defaultSpinnerLabel("@websearch", nil)
	assert.Equal(t, "EXECUTANDO: @websearch ação", got)
}
