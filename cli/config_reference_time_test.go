/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"testing"
)

func TestReferenceTimeValue(t *testing.T) {
	t.Setenv("CHATCLI_NOW", "")
	if got := referenceTimeValue(); !strings.HasPrefix(got, defaultMarker) {
		t.Errorf("unset must render as the default wall clock, got %q", got)
	}

	t.Setenv("CHATCLI_NOW", "2023-05-30T23:40:00Z")
	if got := referenceTimeValue(); got != "2023-05-30T23:40:00Z" {
		t.Errorf("a pinned value renders as RFC 3339, got %q", got)
	}

	t.Setenv("CHATCLI_NOW", "ontem")
	if got := referenceTimeValue(); strings.HasPrefix(got, defaultMarker) || !strings.Contains(got, `"ontem"`) {
		t.Errorf("an invalid value must be named, not shown as the default, got %q", got)
	}
}
