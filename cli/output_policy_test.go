/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/client"
)

func TestVerbosityDirectiveBlockFromEnv(t *testing.T) {
	t.Setenv("CHATCLI_OUTPUT_VERBOSITY", "concise")
	if !strings.Contains(verbosityDirectiveBlock(), "OUTPUT STYLE") {
		t.Fatal("concise should inject the directive")
	}
	t.Setenv("CHATCLI_OUTPUT_VERBOSITY", "full")
	if verbosityDirectiveBlock() != "" {
		t.Fatal("full should inject nothing")
	}
	t.Setenv("CHATCLI_OUTPUT_VERBOSITY", "minimal")
	if !strings.Contains(verbosityDirectiveBlock(), "Minimal output") {
		t.Fatal("minimal should inject the minimal directive")
	}
}

func TestRouteEffortForPromptOptInAndSafe(t *testing.T) {
	// Default off: never changes the base effort.
	t.Setenv("CHATCLI_OUTPUT_EFFORT_ROUTING", "off")
	if got := routeEffortForPrompt("what is a mutex", client.EffortUnset); got != client.EffortUnset {
		t.Fatalf("disabled routing must not change effort, got %v", got)
	}

	// Enabled: trivial + unset → Low.
	t.Setenv("CHATCLI_OUTPUT_EFFORT_ROUTING", "on")
	if got := routeEffortForPrompt("list the providers", client.EffortUnset); got != client.EffortLow {
		t.Fatalf("trivial prompt should downgrade to Low, got %v", got)
	}
	// Enabled but a base effort already chosen → never override (no hijack).
	if got := routeEffortForPrompt("list the providers", client.EffortHigh); got != client.EffortHigh {
		t.Fatalf("explicit effort must be preserved, got %v", got)
	}
	// Enabled + complex prompt → unchanged (never lowers a hard task).
	if got := routeEffortForPrompt("refactor the scheduler to fix the race condition", client.EffortUnset); got != client.EffortUnset {
		t.Fatalf("complex prompt must not be downgraded, got %v", got)
	}
}
