/*
 * ChatCLI - acpSink nil-requester fallback test.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rpcserve

import (
	"context"
	"errors"
	"testing"

	"github.com/diillson/chatcli/cli/agentevents"
)

// TestACPSinkDecision_NilRequesterReportsUnsupported: without a client
// requester wired there is no dialog to consult — the sink must report
// "unsupported" so the loop applies the unattended fallback, not a fake
// user denial for a dialog nobody could ever see.
func TestACPSinkDecision_NilRequesterReportsUnsupported(t *testing.T) {
	s := newACPSink(&ACP{}, "sess", context.Background())
	d, err := s.RequestPermissionDecision(agentevents.PermissionRequest{
		Tool: agentevents.ToolCall{Name: "@coder"},
	})
	if !errors.Is(err, agentevents.ErrPermissionUnsupported) {
		t.Fatalf("err = %v, want ErrPermissionUnsupported", err)
	}
	if d.Allowed() {
		t.Error("nil requester must not allow")
	}
}
