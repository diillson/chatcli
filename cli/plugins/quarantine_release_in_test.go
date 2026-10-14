/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

// One release command covers the tool-plugin and provider-plugin
// quarantines, whose states stay separate.
func TestReleaseInFindsTheRightQuarantine(t *testing.T) {
	t.Setenv("CHATCLI_PLUGIN_QUARANTINE", "24h")
	toolDir, providerDir := t.TempDir(), t.TempDir()
	tools, providers := NewQuarantine(toolDir), NewQuarantine(providerDir)

	bin := filepath.Join(providerDir, "acme-provider")
	if err := os.WriteFile(bin, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if admitted, _ := providers.Admit(bin); admitted {
		t.Fatal("a new unsigned binary must wait")
	}
	if err := ReleaseIn("acme-provider", tools, providers); err != nil {
		t.Fatal(err)
	}
	if admitted, _ := providers.Admit(bin); !admitted {
		t.Fatal("released binary still held")
	}
	if err := ReleaseIn("nope", tools, providers); err == nil {
		t.Fatal("releasing an unknown name must fail")
	}
	if err := ReleaseIn("x"); err == nil {
		t.Fatal("no quarantine at all must fail")
	}
}
