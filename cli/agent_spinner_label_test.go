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

// TestDefaultSpinnerLabel_IncludesToolName pins the fallback contract:
// the tool name MUST appear in the label so users can identify which
// plugin is running even when DescribeCall isn't available. The
// exact verb prefix is i18n-resolved (EXECUTANDO in pt-BR, RUNNING in
// en) so we anchor on the tool name and the first arg as subcommand.
func TestDefaultSpinnerLabel_IncludesToolName(t *testing.T) {
	got := defaultSpinnerLabel("@coder", []string{"read", "--file", "main.go"})
	assert.Contains(t, got, "@coder")
	assert.Contains(t, got, "read")
}

// TestDefaultSpinnerLabel_EmptyArgsUsesPlaceholder pins that when args
// is empty the placeholder fills in for the second %s. The placeholder
// text is locale-dependent ("ação"/"action"), so we assert only that
// the produced label is longer than the tool name alone.
func TestDefaultSpinnerLabel_EmptyArgsUsesPlaceholder(t *testing.T) {
	got := defaultSpinnerLabel("@websearch", nil)
	assert.Contains(t, got, "@websearch")
	assert.Greater(t, len(got), len("@websearch")+1,
		"placeholder must produce a label longer than just the tool name")
}
