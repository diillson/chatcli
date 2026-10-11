/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package workers

import (
	"strings"
	"testing"
)

// TestNativeToolArgsToFlags_DropsElevation: a model-supplied allow_unsafe or
// allow_sudo never reaches the engine as a flag, in any spelling.
func TestNativeToolArgsToFlags_DropsElevation(t *testing.T) {
	args := map[string]interface{}{
		"cmd":          "rm -rf build",
		"allow_unsafe": true,
		"allow_sudo":   true,
		"allow-unsafe": true,
		"allowSudo":    true,
	}
	flags := strings.Join(NativeToolArgsToFlags("exec", args), " ")
	if strings.Contains(strings.ToLower(flags), "allow") {
		t.Fatalf("elevation reached the engine flags: %q", flags)
	}
	if !strings.Contains(flags, "--cmd rm -rf build") {
		t.Fatalf("the command itself must still be mapped: %q", flags)
	}
}
