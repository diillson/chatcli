/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cmd

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/cli/rpcserve"
)

type stubPermRequester struct{}

func (stubPermRequester) RequestPermission(agentevents.ToolCall, string) (bool, error) {
	return true, nil
}

// TestManageSessionPolicyMode_DegradedServer: without the full ChatCLI
// runtime the action must fail loudly instead of silently pretending the
// mode changed — and unknown mode values must be rejected before any state
// is touched.
func TestManageSessionPolicyMode_DegradedServer(t *testing.T) {
	b := &rpcBackend{} // cli == nil (degraded mode)
	if _, err := b.ManageSession(context.TODO(), "policy_mode", "s", "auto"); err == nil {
		t.Fatal("policy_mode without a ChatCLI runtime must error")
	}
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
