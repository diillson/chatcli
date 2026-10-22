/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"go.uber.org/zap"
)

// TestBuiltinsCapabilityParity is the guard behind the capability files:
// every builtin the CLI registers must say which calls only observe
// (ReadOnlyAware — otherwise every call of it goes through the
// confirmation gate), which may run in parallel (ConcurrencySafeAware) and
// what to show while it runs (DescriberWithInput, never empty). A builtin
// added without them silently prompts the user for a pure listing.
func TestBuiltinsCapabilityParity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CHATCLI_AGENT_MODEL_TOOL", "1")
	t.Setenv("CHATCLI_AGENT_DASH_TOOL", "1")
	m, err := plugins.NewManager(zap.NewNop())
	if err != nil {
		t.Fatalf("plugin manager: %v", err)
	}
	registerCoreBuiltins(m)
	registerGatedBuiltins(m)
	all := m.GetPlugins()
	if len(all) < 40 {
		t.Fatalf("only %d builtins registered — registration changed?", len(all))
	}
	for _, p := range all {
		name := p.Name()
		if _, ok := p.(plugins.ReadOnlyAware); !ok {
			t.Errorf("%s: no IsReadOnly — every call of it is gated, even pure listings", name)
		}
		if _, ok := p.(plugins.ConcurrencySafeAware); !ok {
			t.Errorf("%s: no IsConcurrencySafe — it can never join a parallel batch", name)
		}
		d, ok := p.(plugins.DescriberWithInput)
		if !ok {
			t.Errorf("%s: no DescribeCall — the spinner falls back to the long description", name)
			continue
		}
		for _, args := range [][]string{nil, {`{"cmd":"list"}`}, {`{not json`}} {
			if strings.TrimSpace(plugins.DescribeCall(p, args)) == "" || strings.TrimSpace(d.DescribeCall(args)) == "" {
				t.Errorf("%s: DescribeCall(%v) is empty", name, args)
			}
		}
	}
}
