/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cmd

import (
	"testing"

	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/cli/rpcserve"
)

type stubPermRequester struct{}

func (stubPermRequester) RequestPermission(agentevents.ToolCall, string) (bool, error) {
	return true, nil
}

// TestToRunOptsCarriesPermissions pins the RunOpts→RPCRunOpts mapping: the
// MCP elicitation bridge only works if the per-call requester survives the
// backend boundary into the loop options.
func TestToRunOptsCarriesPermissions(t *testing.T) {
	o := toRunOpts("sess", rpcserve.RunOpts{Permissions: stubPermRequester{}})
	if o.Permissions == nil {
		t.Fatal("toRunOpts must carry Permissions through to the loop options")
	}
	if o.Session != "sess" {
		t.Fatalf("session = %q, want sess", o.Session)
	}

	if o := toRunOpts("s", rpcserve.RunOpts{}); o.Permissions != nil {
		t.Fatal("absent requester must stay nil (legacy unattended contract)")
	}
}
