/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"testing"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

// Every RPC the service exposes must have an explicit entry in the
// validator registry. This is the guard that keeps "input validation on
// all RPC fields" true as the service grows: adding an RPC without
// deciding how its request is bounded fails here, at build time for the
// team, instead of silently shipping an unvalidated field.
func TestEveryRPCHasAValidator(t *testing.T) {
	var missing []string
	for _, m := range pb.ChatCLIService_ServiceDesc.Methods {
		if _, ok := requestValidators[m.MethodName]; !ok {
			missing = append(missing, "unary/"+m.MethodName)
		}
	}
	for _, s := range pb.ChatCLIService_ServiceDesc.Streams {
		if _, ok := requestValidators[s.StreamName]; !ok {
			missing = append(missing, "stream/"+s.StreamName)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("RPCs without a registered validator: %v\n"+
			"Add an entry to requestValidators — a real validator, or validateNoFields "+
			"if the request genuinely carries nothing to bound.", missing)
	}
}

// The reverse guard: a registry entry that no longer matches a real RPC is
// dead weight that reads like coverage.
func TestNoOrphanValidators(t *testing.T) {
	known := make(map[string]bool)
	for _, m := range pb.ChatCLIService_ServiceDesc.Methods {
		known[m.MethodName] = true
	}
	for _, s := range pb.ChatCLIService_ServiceDesc.Streams {
		known[s.StreamName] = true
	}
	for name := range requestValidators {
		if !known[name] {
			t.Errorf("requestValidators has %q, which is not an RPC on ChatCLIService", name)
		}
	}
}
